package tempo

import (
	"context"
	"time"

	"github.com/redis/go-redis/v9"
)

// RedisStore implements Store with Redis commands.
type RedisStore struct {
	client redis.Cmdable
	ttl    time.Duration
}

// NewRedisStore constructs a Redis-backed replay-protection store.
//
// When ttl is zero, stored values do not expire automatically. PutIfAbsent
// always keeps replay-protection keys persistent so used payment proofs and
// hashes cannot become valid again after a TTL window.
func NewRedisStore(client redis.Cmdable, ttl time.Duration) *RedisStore {
	return &RedisStore{client: client, ttl: ttl}
}

// Get loads a replay-protection value.
func (s *RedisStore) Get(ctx context.Context, key string) (string, bool, error) {
	value, err := s.client.Get(ctx, key).Result()
	if err == redis.Nil {
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

// Delete removes a replay-protection value.
func (s *RedisStore) Delete(ctx context.Context, key string) error {
	return s.client.Del(ctx, key).Err()
}
