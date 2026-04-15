package tempo

import (
	"context"
	"strings"
	"sync"
)

type Store interface {
	Put(ctx context.Context, key, value string) error
	PutIfAbsent(ctx context.Context, key, value string) (bool, error)
}

type MemoryStore struct {
	mu     sync.Mutex
	values map[string]string
}

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

func ChargeStoreKey(hash string) string {
	return ReplayKeyPrefix + strings.ToLower(hash)
}
