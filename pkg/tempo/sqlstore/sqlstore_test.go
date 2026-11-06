package sqlstore

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"

	"github.com/tempoxyz/mpp-go/pkg/tempo"
)

// Store must satisfy the stores the Tempo charge verifier accepts.
var _ tempo.ExpiringStore = (*Store)(nil)

// postgresDSNEnv names the environment variable with a Postgres DSN to also
// run the tests against, such as postgres://postgres:pg@localhost:5432/mpp_go.
// Without it, the tests run on SQLite only.
const postgresDSNEnv = "MPP_TEST_POSTGRES_DSN"

var tableCounter atomic.Int64

type backend struct {
	name    string
	dialect Dialect
	// open returns a database and a table name no other test uses.
	open func(t *testing.T) (*sql.DB, string)
}

func backends(t *testing.T) []backend {
	t.Helper()
	list := []backend{{
		name:    "sqlite",
		dialect: SQLite,
		open: func(t *testing.T) (*sql.DB, string) {
			t.Helper()
			path := filepath.Join(t.TempDir(), "store.db")
			db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)")
			require.NoError(t, err)
			t.Cleanup(func() { db.Close() })
			return db, DefaultTable
		},
	}}
	if dsn := os.Getenv(postgresDSNEnv); dsn != "" {
		list = append(list, backend{
			name:    "postgres",
			dialect: Postgres,
			open: func(t *testing.T) (*sql.DB, string) {
				t.Helper()
				db, err := sql.Open("pgx", dsn)
				require.NoError(t, err)
				table := fmt.Sprintf("mpp_go_sqlstore_test_%d_%d", os.Getpid(), tableCounter.Add(1))
				t.Cleanup(func() {
					db.Exec("DROP TABLE IF EXISTS " + table)
					db.Close()
				})
				return db, table
			},
		})
	}
	return list
}

// forEachBackend runs fn once per backend, with a fresh store.
func forEachBackend(t *testing.T, fn func(t *testing.T, store *Store, db *sql.DB, table string)) {
	t.Helper()
	for _, b := range backends(t) {
		t.Run(b.name, func(t *testing.T) {
			t.Parallel()
			db, table := b.open(t)
			store, err := New(db, b.dialect, Options{Table: table})
			require.NoError(t, err)
			fn(t, store, db, table)
		})
	}
}

// insertExpired writes key with an expiry in the past, as a claim that has
// lapsed.
func insertExpired(t *testing.T, store *Store, key, value string) {
	t.Helper()
	require.NoError(t, store.ensureTable(context.Background()))
	_, err := store.db.ExecContext(context.Background(),
		store.prepare("INSERT INTO "+store.table+" (key, value, expires) VALUES (?, ?, ?)"),
		key, value, millis(time.Now().Add(-time.Second)))
	require.NoError(t, err)
}

func TestNew(t *testing.T) {
	t.Parallel()
	db, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	defer db.Close()

	for _, table := range []string{"", "mppx_store", "_x1", "main.mppx_store"} {
		_, err := New(db, SQLite, Options{Table: table})
		assert.NoErrorf(t, err, "table %q", table)
	}
	for _, table := range []string{"1x", "a-b", "a.b.c", "x; DROP TABLE y", "x y", ".x", "x."} {
		_, err := New(db, SQLite, Options{Table: table})
		assert.ErrorContainsf(t, err, "invalid table name", "table %q", table)
	}
	_, err = New(db, Dialect(0), Options{})
	assert.ErrorContains(t, err, "unknown dialect")
	_, err = New(nil, SQLite, Options{})
	assert.Error(t, err)
}

func TestPreparePostgresPlaceholders(t *testing.T) {
	t.Parallel()
	pg := &Store{dialect: Postgres}
	lite := &Store{dialect: SQLite}
	query := "UPDATE t SET value = ? WHERE key = ? AND value = ?"
	assert.Equal(t, "UPDATE t SET value = $1 WHERE key = $2 AND value = $3", pg.prepare(query))
	assert.Equal(t, query, lite.prepare(query))
}

func TestStoreOperations(t *testing.T) {
	t.Parallel()
	forEachBackend(t, func(t *testing.T, store *Store, _ *sql.DB, _ string) {
		ctx := context.Background()

		value, ok, err := store.Get(ctx, "missing")
		require.NoError(t, err)
		assert.False(t, ok)
		assert.Empty(t, value)

		require.NoError(t, store.Put(ctx, "k", `{"a":1}`))
		value, ok, err = store.Get(ctx, "k")
		require.NoError(t, err)
		assert.True(t, ok)
		assert.Equal(t, `{"a":1}`, value)

		require.NoError(t, store.Put(ctx, "k", "replaced"))
		value, _, err = store.Get(ctx, "k")
		require.NoError(t, err)
		assert.Equal(t, "replaced", value)

		inserted, err := store.PutIfAbsent(ctx, "once", "first")
		require.NoError(t, err)
		assert.True(t, inserted)
		inserted, err = store.PutIfAbsent(ctx, "once", "second")
		require.NoError(t, err)
		assert.False(t, inserted)
		value, _, err = store.Get(ctx, "once")
		require.NoError(t, err)
		assert.Equal(t, "first", value)

		require.NoError(t, store.Delete(ctx, "k"))
		_, ok, err = store.Get(ctx, "k")
		require.NoError(t, err)
		assert.False(t, ok)
		require.NoError(t, store.Delete(ctx, "k"), "deleting a missing key is not an error")
	})
}

func TestStorePutIfAbsentUntil(t *testing.T) {
	t.Parallel()
	forEachBackend(t, func(t *testing.T, store *Store, _ *sql.DB, _ string) {
		ctx := context.Background()

		ok, err := store.PutIfAbsentUntil(ctx, "k", "v", time.Now().Add(-time.Second))
		require.NoError(t, err)
		assert.False(t, ok, "an expiry in the past must not claim")
		_, found, err := store.Get(ctx, "k")
		require.NoError(t, err)
		assert.False(t, found)

		ok, err = store.PutIfAbsentUntil(ctx, "k", "v", time.Now().Add(100*time.Millisecond))
		require.NoError(t, err)
		require.True(t, ok)
		ok, err = store.PutIfAbsentUntil(ctx, "k", "other", time.Now().Add(time.Hour))
		require.NoError(t, err)
		assert.False(t, ok, "an unexpired claim must not be replaced")
		ok, err = store.PutIfAbsent(ctx, "k", "other")
		require.NoError(t, err)
		assert.False(t, ok, "PutIfAbsent must respect an unexpired claim")
		value, found, err := store.Get(ctx, "k")
		require.NoError(t, err)
		assert.True(t, found)
		assert.Equal(t, "v", value)

		time.Sleep(150 * time.Millisecond)
		_, found, err = store.Get(ctx, "k")
		require.NoError(t, err)
		assert.False(t, found, "Get must report an expired claim as missing")
		ok, err = store.PutIfAbsentUntil(ctx, "k", "again", time.Now().Add(time.Hour))
		require.NoError(t, err)
		assert.True(t, ok, "an expired claim can be claimed again")
		value, _, err = store.Get(ctx, "k")
		require.NoError(t, err)
		assert.Equal(t, "again", value)
	})
}

func TestStorePutIfAbsentReplacesExpiredClaim(t *testing.T) {
	t.Parallel()
	forEachBackend(t, func(t *testing.T, store *Store, _ *sql.DB, _ string) {
		ctx := context.Background()
		insertExpired(t, store, "k", "stale")

		ok, err := store.PutIfAbsent(ctx, "k", "persistent")
		require.NoError(t, err)
		assert.True(t, ok)

		// The new entry never expires.
		ok, err = store.PutIfAbsentUntil(ctx, "k", "other", time.Now().Add(time.Hour))
		require.NoError(t, err)
		assert.False(t, ok)
		value, found, err := store.Get(ctx, "k")
		require.NoError(t, err)
		assert.True(t, found)
		assert.Equal(t, "persistent", value)
	})
}

func TestStorePutClearsExpiry(t *testing.T) {
	t.Parallel()
	forEachBackend(t, func(t *testing.T, store *Store, _ *sql.DB, _ string) {
		ctx := context.Background()
		insertExpired(t, store, "k", "stale")
		require.NoError(t, store.Put(ctx, "k", "kept"))
		value, found, err := store.Get(ctx, "k")
		require.NoError(t, err)
		assert.True(t, found)
		assert.Equal(t, "kept", value)
		ok, err := store.PutIfAbsentUntil(ctx, "k", "v", time.Now().Add(time.Hour))
		require.NoError(t, err)
		assert.False(t, ok, "Put's entry never expires")
	})
}

func TestStorePurgeExpired(t *testing.T) {
	t.Parallel()
	forEachBackend(t, func(t *testing.T, store *Store, _ *sql.DB, _ string) {
		ctx := context.Background()
		require.NoError(t, store.Put(ctx, "permanent", "1"))
		ok, err := store.PutIfAbsentUntil(ctx, "live", "2", time.Now().Add(time.Hour))
		require.NoError(t, err)
		require.True(t, ok)
		insertExpired(t, store, "stale", "3")

		n, err := store.PurgeExpired(ctx)
		require.NoError(t, err)
		assert.Equal(t, int64(1), n)
		for key, want := range map[string]string{"permanent": "1", "live": "2"} {
			value, found, err := store.Get(ctx, key)
			require.NoError(t, err)
			assert.True(t, found, key)
			assert.Equal(t, want, value)
		}
		n, err = store.PurgeExpired(ctx)
		require.NoError(t, err)
		assert.Equal(t, int64(0), n)
	})
}

func TestStoreConcurrentClaims(t *testing.T) {
	t.Parallel()
	for _, b := range backends(t) {
		t.Run(b.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			db, table := b.open(t)

			// Several stores, as in several processes, share the table and
			// create it concurrently.
			const workers = 16
			stores := make([]*Store, workers)
			for i := range stores {
				store, err := New(db, b.dialect, Options{Table: table})
				require.NoError(t, err)
				stores[i] = store
			}

			for round, expired := range []bool{false, true} {
				key := fmt.Sprintf("claim-%d", round)
				if expired {
					insertExpired(t, stores[0], key, "stale")
				}
				var wins atomic.Int64
				var wg sync.WaitGroup
				errs := make(chan error, workers)
				for i, store := range stores {
					wg.Add(1)
					go func() {
						defer wg.Done()
						var ok bool
						var err error
						if i%2 == 0 {
							ok, err = store.PutIfAbsentUntil(ctx, key, fmt.Sprint(i), time.Now().Add(time.Hour))
						} else {
							ok, err = store.PutIfAbsent(ctx, key, fmt.Sprint(i))
						}
						if err != nil {
							errs <- err
						}
						if ok {
							wins.Add(1)
						}
					}()
				}
				wg.Wait()
				close(errs)
				for err := range errs {
					require.NoError(t, err)
				}
				assert.Equalf(t, int64(1), wins.Load(), "round %d: exactly one claim must win", round)
			}
		})
	}
}

func TestStoreCustomTable(t *testing.T) {
	t.Parallel()
	for _, b := range backends(t) {
		t.Run(b.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			db, table := b.open(t)
			schema := "main"
			if b.dialect == Postgres {
				schema = "public"
			}
			table = schema + "." + table + "_custom"
			t.Cleanup(func() { db.Exec("DROP TABLE IF EXISTS " + table) })

			store, err := New(db, b.dialect, Options{Table: table})
			require.NoError(t, err)
			require.NoError(t, store.Put(ctx, "k", "v"))

			var value string
			require.NoError(t, db.QueryRowContext(ctx, store.prepare("SELECT value FROM "+table+" WHERE key = ?"), "k").Scan(&value))
			assert.Equal(t, "v", value)
		})
	}
}

func TestStoreSkipCreateTable(t *testing.T) {
	t.Parallel()
	for _, b := range backends(t) {
		t.Run(b.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			db, table := b.open(t)
			table += "_migrated"
			t.Cleanup(func() { db.Exec("DROP TABLE IF EXISTS " + table) })

			store, err := New(db, b.dialect, Options{Table: table, SkipCreateTable: true})
			require.NoError(t, err)
			_, _, err = store.Get(ctx, "k")
			require.Error(t, err, "the table must not be created")
			assert.True(t, strings.Contains(strings.ToLower(err.Error()), "no such table") ||
				strings.Contains(err.Error(), "does not exist"), err.Error())

			_, err = db.ExecContext(ctx, "CREATE TABLE "+table+
				" (key TEXT PRIMARY KEY, value TEXT NOT NULL, expires DOUBLE PRECISION)")
			require.NoError(t, err)
			ok, err := store.PutIfAbsent(ctx, "k", "v")
			require.NoError(t, err)
			assert.True(t, ok)
		})
	}
}

// blockingDB holds every statement until release is closed.
type blockingDB struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (d *blockingDB) ExecContext(ctx context.Context, _ string, _ ...any) (sql.Result, error) {
	d.once.Do(func() { close(d.started) })
	<-d.release
	return nil, nil
}

func (d *blockingDB) QueryRowContext(ctx context.Context, _ string, _ ...any) *sql.Row {
	panic("unexpected query")
}

func TestStoreTableCreationWaitHonorsContext(t *testing.T) {
	t.Parallel()
	db := &blockingDB{started: make(chan struct{}), release: make(chan struct{})}
	store, err := New(db, SQLite, Options{})
	require.NoError(t, err)

	done := make(chan error, 1)
	go func() { done <- store.ensureTable(context.Background()) }()
	<-db.started

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	require.ErrorIs(t, store.ensureTable(ctx), context.DeadlineExceeded,
		"a waiter must stop when its own context ends")

	close(db.release)
	require.NoError(t, <-done)
	require.NoError(t, store.ensureTable(ctx), "a created table needs no lock")
}
