package tempo

import (
	"context"
	"strings"
	"sync"
	"time"
)

// Store is the minimal replay-protection contract used by the Tempo charge verifier.
//
// Unlike the verifier-specific helpers elsewhere in this package, Store stays
// close to common key-value backends so in-memory, Redis, and SQL-backed
// implementations can all satisfy the same interface. Implementations must not
// allow PutIfAbsent replay keys to become reusable after later expiry.
type Store interface {
	// Get loads a replay-protection value.
	Get(ctx context.Context, key string) (value string, ok bool, err error)
	// Put stores a replay-protection value, replacing any existing entry.
	Put(ctx context.Context, key, value string) error
	// PutIfAbsent stores a replay-protection value only when the key is unused.
	PutIfAbsent(ctx context.Context, key, value string) (bool, error)
	// Delete removes a replay-protection value.
	Delete(ctx context.Context, key string) error
}

// ExpiringStore is an optional extension of Store for replay keys that only
// need to outlive the credential they protect, like mppx's Store.tryClaim with
// a ReplayMarker expiry.
//
// The Tempo charge verifier calls PutIfAbsentUntil instead of PutIfAbsent only
// when configured with ExpireReplayKeys, passing a moment after which the
// credential behind key can no longer be accepted. Otherwise, and for stores
// that do not implement it, replay keys are kept forever with PutIfAbsent. A
// store that embeds MemoryStore or RedisStore and overrides PutIfAbsent must
// also override PutIfAbsentUntil before enabling ExpireReplayKeys.
type ExpiringStore interface {
	Store
	// PutIfAbsentUntil atomically stores value only when key is unused or its
	// previous entry has expired, and keeps the entry until expiresAt. It must
	// return false, storing nothing, when expiresAt is not in the future. An
	// entry must never be dropped before expiresAt, and Get must report it as
	// missing once it has expired.
	PutIfAbsentUntil(ctx context.Context, key, value string, expiresAt time.Time) (bool, error)
}

// memoryStoreSweepInterval bounds how often MemoryStore scans for expired
// entries that were never read again.
const memoryStoreSweepInterval = time.Minute

// MemoryStore is an opt-in development replay store. It does not protect across
// processes or restarts; production replicas must share a persistent Store.
// PutIfAbsent keeps keys until they are deleted; only keys stored with
// PutIfAbsentUntil expire.
type MemoryStore struct {
	mu        sync.Mutex
	values    map[string]string
	expiries  map[string]time.Time
	lastSweep time.Time
}

var _ ExpiringStore = (*MemoryStore)(nil)

// NewMemoryStore constructs an in-memory replay-protection store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{values: map[string]string{}}
}

// Get loads a replay-protection value.
func (s *MemoryStore) Get(_ context.Context, key string) (string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.evictExpiredLocked(key, time.Now())
	value, ok := s.values[key]
	return value, ok, nil
}

// Put stores a replay-protection value, replacing any existing entry.
func (s *MemoryStore) Put(_ context.Context, key, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.values[key] = value
	delete(s.expiries, key)
	return nil
}

// PutIfAbsent stores a replay-protection value only when the key is unused.
func (s *MemoryStore) PutIfAbsent(_ context.Context, key, value string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.evictExpiredLocked(key, time.Now())
	if _, exists := s.values[key]; exists {
		return false, nil
	}
	s.values[key] = value
	return true, nil
}

// PutIfAbsentUntil stores a replay-protection value only when the key is
// unused or expired, and keeps it until expiresAt. It stores nothing and
// returns false when expiresAt is not in the future.
func (s *MemoryStore) PutIfAbsentUntil(_ context.Context, key, value string, expiresAt time.Time) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	s.sweepExpiredLocked(now)
	s.evictExpiredLocked(key, now)
	if !expiresAt.After(now) {
		return false, nil
	}
	if _, exists := s.values[key]; exists {
		return false, nil
	}
	if s.expiries == nil {
		s.expiries = map[string]time.Time{}
	}
	s.values[key] = value
	s.expiries[key] = expiresAt
	return true, nil
}

// Delete removes a replay-protection value.
func (s *MemoryStore) Delete(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.values, key)
	delete(s.expiries, key)
	return nil
}

// evictExpiredLocked drops key when its expiry has passed.
func (s *MemoryStore) evictExpiredLocked(key string, now time.Time) {
	if expiresAt, ok := s.expiries[key]; ok && !now.Before(expiresAt) {
		delete(s.values, key)
		delete(s.expiries, key)
	}
}

// sweepExpiredLocked drops every expired entry, at most once per
// memoryStoreSweepInterval, so keys that are never read again do not
// accumulate.
func (s *MemoryStore) sweepExpiredLocked(now time.Time) {
	if now.Sub(s.lastSweep) < memoryStoreSweepInterval {
		return
	}
	s.lastSweep = now
	for key, expiresAt := range s.expiries {
		if !now.Before(expiresAt) {
			delete(s.values, key)
			delete(s.expiries, key)
		}
	}
}

// ChargeStoreKey normalizes a replay-protection key for a Tempo transaction hash.
func ChargeStoreKey(hash string) string {
	hash = strings.ToLower(strings.TrimSpace(hash))
	if !strings.HasPrefix(hash, "0x") {
		hash = "0x" + hash
	}
	return ReplayKeyPrefix + hash
}

// ChargeSponsoredTransactionStoreKey normalizes a replay-protection key for a
// sponsored Tempo transaction awaiting co-signature, keyed by the keccak256
// hash of the verified payer and canonical sender signing payload.
func ChargeSponsoredTransactionStoreKey(hash string) string {
	return ReplayKeyPrefix + "sponsor:" + strings.ToLower(hash)
}

// ChargeProofStoreKey normalizes a replay-protection key for a Tempo proof credential.
func ChargeProofStoreKey(challengeID string) string {
	return ReplayKeyPrefix + "proof:" + challengeID
}
