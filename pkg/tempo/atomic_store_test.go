package tempo

import (
	"context"
	"testing"
	"time"

	miniredis "github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAtomicStores(t *testing.T) {
	stores := map[string]func(t *testing.T) AtomicStore{
		"memory": func(*testing.T) AtomicStore { return NewMemoryStore() },
		"redis": func(t *testing.T) AtomicStore {
			server := miniredis.RunT(t)
			return NewRedisStore(redis.NewClient(&redis.Options{Addr: server.Addr()}), 0)
		},
	}
	for name, newStore := range stores {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			store := newStore(t)
			old := "a"

			swapped, err := store.CompareAndSwap(ctx, "k", &old, "b")
			require.NoError(t, err)
			assert.False(t, swapped, "swap of a missing key with an expected value")

			swapped, err = store.CompareAndSwap(ctx, "k", nil, "a")
			require.NoError(t, err)
			assert.True(t, swapped, "swap of a missing key")

			swapped, err = store.CompareAndSwap(ctx, "k", nil, "c")
			require.NoError(t, err)
			assert.False(t, swapped, "absent-only swap of a present key")

			swapped, err = store.CompareAndSwap(ctx, "k", &old, "b")
			require.NoError(t, err)
			assert.True(t, swapped)
			value, _, err := store.Get(ctx, "k")
			require.NoError(t, err)
			assert.Equal(t, "b", value)

			deleted, err := store.CompareAndDelete(ctx, "k", "a")
			require.NoError(t, err)
			assert.False(t, deleted)
			deleted, err = store.CompareAndDelete(ctx, "k", "b")
			require.NoError(t, err)
			assert.True(t, deleted)
			_, ok, err := store.Get(ctx, "k")
			require.NoError(t, err)
			assert.False(t, ok)
		})
	}
}

func TestRedisStoreCompareAndSwapIgnoresTTL(t *testing.T) {
	ctx := context.Background()
	server := miniredis.RunT(t)
	store := NewRedisStore(redis.NewClient(&redis.Options{Addr: server.Addr()}), time.Minute)

	swapped, err := store.CompareAndSwap(ctx, "new", nil, "a")
	require.NoError(t, err)
	require.True(t, swapped)
	assert.Zero(t, server.TTL("new"), "CompareAndSwap must not apply the store's TTL")

	require.NoError(t, store.Put(ctx, "put", "a"))
	require.Positive(t, server.TTL("put"))
	old := "a"
	swapped, err = store.CompareAndSwap(ctx, "put", &old, "b")
	require.NoError(t, err)
	require.True(t, swapped)
	assert.Zero(t, server.TTL("put"), "a swapped value persists")

	server.FastForward(2 * time.Minute)
	value, ok, err := store.Get(ctx, "put")
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, "b", value)
}
