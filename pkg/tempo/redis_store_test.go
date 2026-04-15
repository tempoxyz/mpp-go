package tempo

import (
	"context"
	"testing"
	"time"

	miniredis "github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
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
				if value, ok, err := store.Get(context.Background(), "missing"); err != nil || ok || value != "" {
					t.Fatalf("Get() = (%q, %t, %v), want (\"\", false, nil)", value, ok, err)
				}
			},
		},
		{
			name: "put and get",
			run: func(t *testing.T, store *RedisStore, _ *miniredis.Miniredis) {
				t.Helper()
				if err := store.Put(context.Background(), "k", "v"); err != nil {
					t.Fatalf("Put() error = %v", err)
				}
				value, ok, err := store.Get(context.Background(), "k")
				if err != nil {
					t.Fatalf("Get() error = %v", err)
				}
				if !ok || value != "v" {
					t.Fatalf("Get() = (%q, %t), want (\"v\", true)", value, ok)
				}
			},
		},
		{
			name: "put if absent only inserts once",
			run: func(t *testing.T, store *RedisStore, _ *miniredis.Miniredis) {
				t.Helper()
				inserted, err := store.PutIfAbsent(context.Background(), "k", "first")
				if err != nil {
					t.Fatalf("first PutIfAbsent() error = %v", err)
				}
				if !inserted {
					t.Fatal("first PutIfAbsent() = false, want true")
				}
				inserted, err = store.PutIfAbsent(context.Background(), "k", "second")
				if err != nil {
					t.Fatalf("second PutIfAbsent() error = %v", err)
				}
				if inserted {
					t.Fatal("second PutIfAbsent() = true, want false")
				}
				value, ok, err := store.Get(context.Background(), "k")
				if err != nil {
					t.Fatalf("Get() error = %v", err)
				}
				if !ok || value != "first" {
					t.Fatalf("Get() = (%q, %t), want (\"first\", true)", value, ok)
				}
			},
		},
		{
			name: "delete removes key",
			run: func(t *testing.T, store *RedisStore, _ *miniredis.Miniredis) {
				t.Helper()
				if err := store.Put(context.Background(), "k", "v"); err != nil {
					t.Fatalf("Put() error = %v", err)
				}
				if err := store.Delete(context.Background(), "k"); err != nil {
					t.Fatalf("Delete() error = %v", err)
				}
				if value, ok, err := store.Get(context.Background(), "k"); err != nil || ok || value != "" {
					t.Fatalf("Get() after delete = (%q, %t, %v), want (\"\", false, nil)", value, ok, err)
				}
			},
		},
		{
			name: "ttl expires key",
			ttl:  time.Minute,
			run: func(t *testing.T, store *RedisStore, server *miniredis.Miniredis) {
				t.Helper()
				if err := store.Put(context.Background(), "k", "v"); err != nil {
					t.Fatalf("Put() error = %v", err)
				}
				server.FastForward(time.Minute + time.Second)
				if value, ok, err := store.Get(context.Background(), "k"); err != nil || ok || value != "" {
					t.Fatalf("Get() after expiry = (%q, %t, %v), want (\"\", false, nil)", value, ok, err)
				}
			},
		},
		{
			name: "put if absent stays persistent with ttl",
			ttl:  time.Minute,
			run: func(t *testing.T, store *RedisStore, server *miniredis.Miniredis) {
				t.Helper()
				inserted, err := store.PutIfAbsent(context.Background(), "k", "v")
				if err != nil {
					t.Fatalf("PutIfAbsent() error = %v", err)
				}
				if !inserted {
					t.Fatal("PutIfAbsent() = false, want true")
				}
				server.FastForward(time.Minute + time.Second)
				value, ok, err := store.Get(context.Background(), "k")
				if err != nil {
					t.Fatalf("Get() error = %v", err)
				}
				if !ok || value != "v" {
					t.Fatalf("Get() = (%q, %t), want (\"v\", true)", value, ok)
				}
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			server, err := miniredis.Run()
			if err != nil {
				t.Fatalf("miniredis.Run() error = %v", err)
			}
			defer server.Close()

			client := redis.NewClient(&redis.Options{Addr: server.Addr()})
			defer client.Close()

			tt.run(t, NewRedisStore(client, tt.ttl), server)
		})
	}
}
