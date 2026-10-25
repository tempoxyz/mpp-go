package tempo

import (
	"context"
	"errors"
	"time"

	"github.com/redis/go-redis/v9"
)

// RedisStore implements Store with Redis commands.
type RedisStore struct {
	client redis.Cmdable
	ttl    time.Duration
}

var _ ExpiringStore = (*RedisStore)(nil)

// NewRedisStore constructs a Redis-backed replay-protection store.
//
// When ttl is zero, stored values do not expire automatically. PutIfAbsent
// always keeps replay-protection keys persistent so used payment proofs and
// hashes cannot become valid again after a TTL window. Only PutIfAbsentUntil,
// which the Tempo charge verifier calls when configured with ExpireReplayKeys,
// stores keys that expire.
func NewRedisStore(client redis.Cmdable, ttl time.Duration) *RedisStore {
	return &RedisStore{client: client, ttl: ttl}
}

// Get loads a replay-protection value.
func (s *RedisStore) Get(ctx context.Context, key string) (string, bool, error) {
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
func (s *RedisStore) Put(ctx context.Context, key, value string) error {
	return s.client.Set(ctx, key, value, s.ttl).Err()
}

// PutIfAbsent stores a replay-protection value only when the key is unused.
func (s *RedisStore) PutIfAbsent(ctx context.Context, key, value string) (bool, error) {
	return s.client.SetNX(ctx, key, value, 0).Result()
}

// PutIfAbsentUntil stores a replay-protection value only when the key is
// unused, with SET NX and a TTL that ends no earlier than expiresAt. It stores
// nothing and returns false when expiresAt is not in the future.
func (s *RedisStore) PutIfAbsentUntil(ctx context.Context, key, value string, expiresAt time.Time) (bool, error) {
	ttl := time.Until(expiresAt)
	if ttl <= 0 {
		return false, nil
	}
	// Round up to Redis's millisecond precision so the key never expires
	// before expiresAt.
	ttl = ttl.Truncate(time.Millisecond) + time.Millisecond
	return s.client.SetNX(ctx, key, value, ttl).Result()
}

// Delete removes a replay-protection value.
func (s *RedisStore) Delete(ctx context.Context, key string) error {
	return s.client.Del(ctx, key).Err()
}
