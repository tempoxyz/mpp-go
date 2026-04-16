package tempo

import (
	"context"
	"strings"
	"sync"
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

// MemoryStore is the default in-process replay-protection store.
type MemoryStore struct {
	mu     sync.Mutex
	values map[string]string
}

// NewMemoryStore constructs an in-memory replay-protection store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{values: map[string]string{}}
}

// Get loads a replay-protection value.
func (s *MemoryStore) Get(_ context.Context, key string) (string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.values[key]
	return value, ok, nil
}

// Put stores a replay-protection value, replacing any existing entry.
func (s *MemoryStore) Put(_ context.Context, key, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.values[key] = value
	return nil
}

// PutIfAbsent stores a replay-protection value only when the key is unused.
func (s *MemoryStore) PutIfAbsent(_ context.Context, key, value string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.values[key]; exists {
		return false, nil
	}
	s.values[key] = value
	return true, nil
}

// Delete removes a replay-protection value.
func (s *MemoryStore) Delete(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.values, key)
	return nil
}

// ChargeStoreKey normalizes a replay-protection key for a Tempo transaction hash.
func ChargeStoreKey(hash string) string {
	return ReplayKeyPrefix + strings.ToLower(hash)
}

// ChargeProofStoreKey normalizes a replay-protection key for a Tempo proof credential.
func ChargeProofStoreKey(challengeID string) string {
	return ReplayKeyPrefix + "proof:" + challengeID
}
