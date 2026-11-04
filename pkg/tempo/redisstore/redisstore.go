// Package redisstore provides a Redis-backed replay-protection store for the
// Tempo charge verifier (tempo.Store and tempo.ExpiringStore). It lives in its
// own package so that importing pkg/tempo does not link go-redis.
package redisstore

import (
	"context"
	"errors"
	"time"

	"github.com/redis/go-redis/v9"
)

// Store implements tempo.Store and tempo.ExpiringStore with Redis commands.
// It replaces tempo.RedisStore, which moved here.
type Store struct {
	client redis.Cmdable
	ttl    time.Duration
}

// New constructs a Redis-backed replay-protection store. It replaces
// tempo.NewRedisStore.
//
// When ttl is zero, stored values do not expire automatically. PutIfAbsent
// always keeps replay-protection keys persistent so used payment proofs and
// hashes cannot become valid again after a TTL window. Only PutIfAbsentUntil,
// which the Tempo charge verifier calls when configured with ExpireReplayKeys,
// stores keys that expire.
func New(client redis.Cmdable, ttl time.Duration) *Store {
	return &Store{client: client, ttl: ttl}
}

// Get loads a replay-protection value.
func (s *Store) Get(ctx context.Context, key string) (string, bool, error) {
	value, err := s.client.Get(ctx, key).Result()
	if errors.Is(err, redis.Nil) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return value, true, nil
}

// Put stores a replay-protection value, replacing any existing entry.
func (s *Store) Put(ctx context.Context, key, value string) error {
	return s.client.Set(ctx, key, value, s.ttl).Err()
}

// PutIfAbsent stores a replay-protection value only when the key is unused.
func (s *Store) PutIfAbsent(ctx context.Context, key, value string) (bool, error) {
	return s.client.SetNX(ctx, key, value, 0).Result()
}

// PutIfAbsentUntil stores a replay-protection value only when the key is
// unused, with SET NX EXAT, so Redis expires it at the absolute deadline
// expiresAt rather than after a TTL measured with this process's clock. It
// stores nothing and returns false when expiresAt is not in the future.
// EXAT requires Redis 6.2 or later.
func (s *Store) PutIfAbsentUntil(ctx context.Context, key, value string, expiresAt time.Time) (bool, error) {
	if !expiresAt.After(time.Now()) {
		return false, nil
	}
	// Round up to EXAT's whole seconds so the key never expires before
	// expiresAt.
	deadline := expiresAt.Truncate(time.Second)
	if deadline.Before(expiresAt) {
		deadline = deadline.Add(time.Second)
	}
	err := s.client.SetArgs(ctx, key, value, redis.SetArgs{Mode: "NX", ExpireAt: deadline}).Err()
	if errors.Is(err, redis.Nil) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// Delete removes a replay-protection value.
func (s *Store) Delete(ctx context.Context, key string) error {
	return s.client.Del(ctx, key).Err()
}
