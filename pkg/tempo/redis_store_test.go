package tempo

import (
	"context"
	"testing"
	"time"

	miniredis "github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
)

func TestRedisStoreOperations(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		ttl  time.Duration
		run  func(*testing.T, *RedisStore, *miniredis.Miniredis)
	}{
		{
			name: "missing key",
			run: func(t *testing.T, store *RedisStore, _ *miniredis.Miniredis) {
				t.Helper()
				{
					value, ok, err := store.Get(context.Background(), "missing")
					if !assert.Falsef(t, err != nil || ok || value != "",
						"Get() = (%q, %t, %v), want (\"\", false, nil)", value, ok, err) {
						return
					}
				}

			},
		},
		{
			name: "put and get",
			run: func(t *testing.T, store *RedisStore, _ *miniredis.Miniredis) {
				t.Helper()
				{
					err := store.Put(context.Background(), "k", "v")
					if !assert.NoErrorf(t, err,
						"Put() error = %v", err) {
						return
					}
				}

				value, ok, err := store.Get(context.Background(), "k")
				if !assert.NoErrorf(t, err,
					"Get() error = %v", err) {
					return
				}
				if !assert.Falsef(t, !ok || value != "v",
					"Get() = (%q, %t), want (\"v\", true)", value, ok) {
					return
				}

			},
		},
		{
			name: "put if absent only inserts once",
			run: func(t *testing.T, store *RedisStore, _ *miniredis.Miniredis) {
				t.Helper()
				inserted, err := store.PutIfAbsent(context.Background(), "k", "first")
				if !assert.NoErrorf(t, err,
					"first PutIfAbsent() error = %v", err) {
					return
				}
				if !assert.True(t, inserted,
					"first PutIfAbsent() = false, want true") {
					return
				}

				inserted, err = store.PutIfAbsent(context.Background(), "k", "second")
				if !assert.NoErrorf(t, err,
					"second PutIfAbsent() error = %v", err) {
					return
				}
				if !assert.False(t, inserted,
					"second PutIfAbsent() = true, want false") {
					return
				}

				value, ok, err := store.Get(context.Background(), "k")
				if !assert.NoErrorf(t, err,
					"Get() error = %v", err) {
					return
				}
				if !assert.Falsef(t, !ok || value != "first",
					"Get() = (%q, %t), want (\"first\", true)", value, ok) {
					return
				}

			},
		},
		{
			name: "delete removes key",
			run: func(t *testing.T, store *RedisStore, _ *miniredis.Miniredis) {
				t.Helper()
				{
					err := store.Put(context.Background(), "k", "v")
					if !assert.NoErrorf(t, err,
						"Put() error = %v", err) {
						return
					}
				}
				{

					err := store.Delete(context.Background(), "k")
					if !assert.NoErrorf(t, err,
						"Delete() error = %v", err) {
						return
					}
				}
				{

					value, ok, err := store.Get(context.Background(), "k")
					if !assert.Falsef(t, err != nil || ok || value != "",
						"Get() after delete = (%q, %t, %v), want (\"\", false, nil)", value, ok, err) {
						return
					}
				}

			},
		},
		{
			name: "ttl expires key",
			ttl:  time.Minute,
			run: func(t *testing.T, store *RedisStore, server *miniredis.Miniredis) {
				t.Helper()
				{
					err := store.Put(context.Background(), "k", "v")
					if !assert.NoErrorf(t, err,
						"Put() error = %v", err) {
						return
					}
				}

				server.FastForward(time.Minute + time.Second)
				{
					value, ok, err := store.Get(context.Background(), "k")
					if !assert.Falsef(t, err != nil || ok || value != "",
						"Get() after expiry = (%q, %t, %v), want (\"\", false, nil)", value, ok, err) {
						return
					}
				}

			},
		},
		{
			name: "put if absent stays persistent with ttl",
			ttl:  time.Minute,
			run: func(t *testing.T, store *RedisStore, server *miniredis.Miniredis) {
				t.Helper()
				inserted, err := store.PutIfAbsent(context.Background(), "k", "v")
				if !assert.NoErrorf(t, err,
					"PutIfAbsent() error = %v", err) {
					return
				}
				if !assert.True(t, inserted,
					"PutIfAbsent() = false, want true") {
					return
				}

				server.FastForward(time.Minute + time.Second)
				value, ok, err := store.Get(context.Background(), "k")
				if !assert.NoErrorf(t, err,
					"Get() error = %v", err) {
					return
				}
				if !assert.Falsef(t, !ok || value != "v",
					"Get() = (%q, %t), want (\"v\", true)", value, ok) {
					return
				}

			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			server, err := miniredis.Run()
			if !assert.NoErrorf(t, err,
				"miniredis.Run() error = %v", err) {
				return
			}

			defer server.Close()

			client := redis.NewClient(&redis.Options{Addr: server.Addr()})
			defer client.Close()

			tt.run(t, NewRedisStore(client, tt.ttl), server)
		})
	}
}
