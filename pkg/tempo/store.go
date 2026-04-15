package tempo

import (
	"context"
	"strings"
	"sync"
)

// Store is the minimal replay-protection contract used by ChargeIntent.
//
// Unlike pympp's broader key-value protocol, the Go verifier only needs writes
// and atomic insert-if-absent semantics, so Redis or SQL-backed stores can stay
// lightweight and dependency-free in this package.
type Store interface {
	Put(ctx context.Context, key, value string) error
	PutIfAbsent(ctx context.Context, key, value string) (bool, error)
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

func (s *MemoryStore) Put(_ context.Context, key, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.values[key] = value
	return nil
}

func (s *MemoryStore) PutIfAbsent(_ context.Context, key, value string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.values[key]; exists {
		return false, nil
	}
	s.values[key] = value
	return true, nil
}

// ChargeStoreKey normalizes a replay-protection key for a Tempo transaction hash.
func ChargeStoreKey(hash string) string {
	return ReplayKeyPrefix + strings.ToLower(hash)
}
