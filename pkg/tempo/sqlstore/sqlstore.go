// Package sqlstore provides a SQL-backed replay-protection store for the Tempo
// charge verifier (tempo.Store and tempo.ExpiringStore) on database/sql, for
// Postgres and SQLite 3.35 or later. It lives in its own package and imports no
// driver: pass a database opened with the driver of your choice.
//
// Values live in one table (mppx_store by default, as in mppx's Store.sql) with
// the columns key TEXT PRIMARY KEY, value TEXT NOT NULL and expires DOUBLE
// PRECISION (milliseconds since the Unix epoch, or NULL for entries that never
// expire). Every write is a single statement, so the store needs no
// transactions and stays atomic across processes sharing the database.
package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// DefaultTable is the table New uses when Options.Table is empty.
const DefaultTable = "mppx_store"

// Dialect selects the SQL placeholder style.
type Dialect int

const (
	// Postgres numbers placeholders ($1, $2, ...).
	Postgres Dialect = iota + 1
	// SQLite uses ? placeholders. It needs SQLite 3.35 or later (RETURNING).
	SQLite
)

// DB is the subset of *sql.DB the store uses; *sql.Conn and *sql.Tx satisfy
// it too.
type DB interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// Options configures New.
type Options struct {
	// Table holds the values, optionally schema-qualified (schema.table).
	// DefaultTable when empty.
	Table string
	// SkipCreateTable stops the store from running CREATE TABLE IF NOT EXISTS
	// on first use, for databases whose migrations create the table.
	SkipCreateTable bool
}

// tableName matches a table name, optionally schema-qualified, that is safe
// to interpolate into SQL.
var tableName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*(\.[A-Za-z_][A-Za-z0-9_]*)?$`)

// Store implements tempo.Store and tempo.ExpiringStore with SQL statements.
type Store struct {
	db      DB
	dialect Dialect
	table   string
	create  bool

	mu    sync.Mutex
	ready bool
}

// New constructs a SQL-backed replay-protection store. It returns an error for
// a nil db, an unknown dialect, or a table name that is not an identifier,
// optionally schema-qualified. Unless Options.SkipCreateTable is set, the
// table is created on first use.
//
// PutIfAbsent keeps replay-protection keys until they are deleted. Only
// PutIfAbsentUntil, which the Tempo charge verifier calls when configured with
// ExpireReplayKeys, stores keys that expire.
func New(db DB, dialect Dialect, options Options) (*Store, error) {
	if db == nil {
		return nil, errors.New("sqlstore: nil DB")
	}
	if dialect != Postgres && dialect != SQLite {
		return nil, fmt.Errorf("sqlstore: unknown dialect %d", dialect)
	}
	table := options.Table
	if table == "" {
		table = DefaultTable
	}
	if !tableName.MatchString(table) {
		return nil, fmt.Errorf("sqlstore: invalid table name %q", table)
	}
	return &Store{db: db, dialect: dialect, table: table, create: !options.SkipCreateTable}, nil
}

// Get loads a replay-protection value. An entry whose expiry has passed is
// reported as missing.
func (s *Store) Get(ctx context.Context, key string) (string, bool, error) {
	var value string
	found, err := s.queryRow(ctx, &value,
		"SELECT value FROM "+s.table+" WHERE key = ? AND (expires IS NULL OR expires > ?)",
		key, millis(time.Now()))
	if err != nil || !found {
		return "", false, err
	}
	return value, true, nil
}

// Put stores a replay-protection value, replacing any existing entry and its
// expiry.
func (s *Store) Put(ctx context.Context, key, value string) error {
	if err := s.ensureTable(ctx); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, s.prepare("INSERT INTO "+s.table+" (key, value, expires) VALUES (?, ?, NULL) "+
		"ON CONFLICT (key) DO UPDATE SET value = excluded.value, expires = NULL"), key, value)
	return err
}

// PutIfAbsent stores a replay-protection value only when the key is unused or
// its previous entry has expired. The entry never expires.
func (s *Store) PutIfAbsent(ctx context.Context, key, value string) (bool, error) {
	return s.claim(ctx, key, value, nil)
}

// PutIfAbsentUntil stores a replay-protection value only when the key is
// unused or its previous entry has expired, and keeps it until expiresAt. One
// INSERT ... ON CONFLICT DO UPDATE ... WHERE statement inserts the entry or
// replaces an expired one, so an unexpired entry is never replaced. It stores
// nothing and returns false when expiresAt is not in the future. Expiry is
// measured with this process's clock.
func (s *Store) PutIfAbsentUntil(ctx context.Context, key, value string, expiresAt time.Time) (bool, error) {
	if !expiresAt.After(time.Now()) {
		return false, nil
	}
	expires := millis(expiresAt)
	claimed, err := s.claim(ctx, key, value, &expires)
	// The claim's own expiry may have passed while the statement ran.
	return claimed && expiresAt.After(time.Now()), err
}

// Delete removes a replay-protection value.
func (s *Store) Delete(ctx context.Context, key string) error {
	if err := s.ensureTable(ctx); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, s.prepare("DELETE FROM "+s.table+" WHERE key = ?"), key)
	return err
}

// PurgeExpired deletes entries whose expiry has passed and reports how many it
// removed. Get already reports them missing and the next claim of the same key
// replaces them, but replay keys are rarely claimed twice, so call it
// periodically (for example from a ticker) to keep the table from growing.
// Entries that never expire and live claims are never removed. mppx's Store.sql
// has no equivalent.
func (s *Store) PurgeExpired(ctx context.Context) (int64, error) {
	if err := s.ensureTable(ctx); err != nil {
		return 0, err
	}
	res, err := s.db.ExecContext(ctx,
		s.prepare("DELETE FROM "+s.table+" WHERE expires IS NOT NULL AND expires <= ?"),
		millis(time.Now()))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// claim inserts key, or replaces an entry whose expiry has passed, in one
// statement, and reports whether it wrote. A nil expires never expires.
func (s *Store) claim(ctx context.Context, key, value string, expires *float64) (bool, error) {
	var expiresArg any
	if expires != nil {
		expiresArg = *expires
	}
	var returned string
	return s.queryRow(ctx, &returned,
		"INSERT INTO "+s.table+" AS existing (key, value, expires) VALUES (?, ?, ?) "+
			"ON CONFLICT (key) DO UPDATE SET value = excluded.value, expires = excluded.expires "+
			"WHERE existing.expires IS NOT NULL AND existing.expires <= ? RETURNING key",
		key, value, expiresArg, millis(time.Now()))
}

// queryRow runs a statement returning at most one single-column row into dest
// and reports whether it returned one.
func (s *Store) queryRow(ctx context.Context, dest *string, query string, args ...any) (bool, error) {
	if err := s.ensureTable(ctx); err != nil {
		return false, err
	}
	err := s.db.QueryRowContext(ctx, s.prepare(query), args...).Scan(dest)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// ensureTable runs CREATE TABLE IF NOT EXISTS once. Concurrent creation from
// other processes can fail on Postgres (duplicate catalog row) although the
// table now exists, so a failure is followed by a probe of the table; a
// failure is retried on the next call.
func (s *Store) ensureTable(ctx context.Context) error {
	if !s.create {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ready {
		return nil
	}
	_, err := s.db.ExecContext(ctx, "CREATE TABLE IF NOT EXISTS "+s.table+
		" (key TEXT PRIMARY KEY, value TEXT NOT NULL, expires DOUBLE PRECISION)")
	if err != nil {
		if _, probeErr := s.db.ExecContext(ctx, "SELECT 1 FROM "+s.table+" WHERE 1 = 0"); probeErr != nil {
			return fmt.Errorf("sqlstore: create table %s: %w", s.table, err)
		}
	}
	s.ready = true
	return nil
}

// prepare rewrites the ? placeholders as $1, $2, ... for Postgres. The
// statements contain no other question marks.
func (s *Store) prepare(query string) string {
	if s.dialect != Postgres {
		return query
	}
	var b strings.Builder
	n := 0
	for _, r := range query {
		if r != '?' {
			b.WriteRune(r)
			continue
		}
		n++
		b.WriteByte('$')
		b.WriteString(strconv.Itoa(n))
	}
	return b.String()
}

// millis converts t to milliseconds since the Unix epoch, the unit of the
// expires column (as in mppx), keeping sub-millisecond precision.
func millis(t time.Time) float64 {
	return float64(t.UnixMicro()) / 1000
}
