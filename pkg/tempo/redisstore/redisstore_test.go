package redisstore

import (
	"context"
	"testing"
	"time"

	miniredis "github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tempoxyz/mpp-go/pkg/tempo"
)

// Store must satisfy the stores the Tempo charge verifier accepts.
var _ tempo.ExpiringStore = (*Store)(nil)

func TestStoreOperations(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		ttl  time.Duration
		run  func(*testing.T, *Store, *miniredis.Miniredis)
	}{
		{
			name: "missing key",
			run: func(t *testing.T, store *Store, _ *miniredis.Miniredis) {
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
			run: func(t *testing.T, store *Store, _ *miniredis.Miniredis) {
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
			run: func(t *testing.T, store *Store, _ *miniredis.Miniredis) {
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
			run: func(t *testing.T, store *Store, _ *miniredis.Miniredis) {
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
			run: func(t *testing.T, store *Store, server *miniredis.Miniredis) {
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
			run: func(t *testing.T, store *Store, server *miniredis.Miniredis) {
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

			tt.run(t, New(client, tt.ttl), server)
		})
	}
}

func TestStorePutIfAbsentUntil(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	server, err := miniredis.Run()
	require.NoError(t, err)
	defer server.Close()
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	defer client.Close()
	store := New(client, 0)

	ok, err := store.PutIfAbsentUntil(ctx, "k", "v", time.Now().Add(-time.Second))
	require.NoError(t, err)
	assert.False(t, ok, "an expiry in the past must not claim")
	assert.False(t, server.Exists("k"))

	ok, err = store.PutIfAbsentUntil(ctx, "k", "v", time.Now().Add(time.Minute))
	require.NoError(t, err)
	require.True(t, ok)
	ttl := server.TTL("k")
	assert.Greater(t, ttl, 59*time.Second)
	assert.LessOrEqual(t, ttl, time.Minute+time.Second)

	ok, err = store.PutIfAbsentUntil(ctx, "k", "other", time.Now().Add(time.Hour))
	require.NoError(t, err)
	assert.False(t, ok)
	ok, err = store.PutIfAbsent(ctx, "k", "other")
	require.NoError(t, err)
	assert.False(t, ok)

	server.FastForward(time.Minute + time.Second)
	_, found, err := store.Get(ctx, "k")
	require.NoError(t, err)
	assert.False(t, found)
	ok, err = store.PutIfAbsentUntil(ctx, "k", "again", time.Now().Add(time.Minute))
	require.NoError(t, err)
	assert.True(t, ok)
}

func TestStorePutIfAbsentUntilUsesAbsoluteDeadline(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	server, err := miniredis.Run()
	require.NoError(t, err)
	defer server.Close()
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	defer client.Close()
	store := New(client, 0)

	// The Redis server's clock is an hour behind the verifier's. A TTL
	// derived from the verifier's clock would expire the key an hour before
	// expiresAt by the server's clock; an absolute deadline does not.
	now := time.Now()
	server.SetTime(now.Add(-time.Hour))
	expiresAt := now.Add(time.Minute)
	ok, err := store.PutIfAbsentUntil(ctx, "k", "v", expiresAt)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Greater(t, server.TTL("k"), time.Hour)

	server.FastForward(time.Hour)
	_, found, err := store.Get(ctx, "k")
	require.NoError(t, err)
	assert.True(t, found, "the key must outlive the verifier's TTL estimate")
}
