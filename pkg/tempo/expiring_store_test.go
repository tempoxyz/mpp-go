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

func TestMemoryStorePutIfAbsentUntil(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := NewMemoryStore()

	ok, err := store.PutIfAbsentUntil(ctx, "k", "v", time.Now().Add(-time.Second))
	require.NoError(t, err)
	assert.False(t, ok, "an expiry in the past must not claim")
	_, found, _ := store.Get(ctx, "k")
	assert.False(t, found)

	ok, err = store.PutIfAbsentUntil(ctx, "k", "v", time.Now().Add(50*time.Millisecond))
	require.NoError(t, err)
	require.True(t, ok)
	ok, err = store.PutIfAbsentUntil(ctx, "k", "other", time.Now().Add(time.Hour))
	require.NoError(t, err)
	assert.False(t, ok, "an unexpired claim must not be replaced")
	ok, err = store.PutIfAbsent(ctx, "k", "other")
	require.NoError(t, err)
	assert.False(t, ok, "PutIfAbsent must respect an unexpired claim")
	value, found, err := store.Get(ctx, "k")
	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, "v", value)

	time.Sleep(60 * time.Millisecond)
	_, found, err = store.Get(ctx, "k")
	require.NoError(t, err)
	assert.False(t, found, "Get must report an expired claim as missing")
	ok, err = store.PutIfAbsent(ctx, "k", "persistent")
	require.NoError(t, err)
	assert.True(t, ok, "an expired claim can be claimed again")

	// PutIfAbsent keys never expire.
	time.Sleep(60 * time.Millisecond)
	value, found, err = store.Get(ctx, "k")
	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, "persistent", value)
}

func TestMemoryStorePutClearsExpiry(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := NewMemoryStore()

	ok, err := store.PutIfAbsentUntil(ctx, "k", "v", time.Now().Add(20*time.Millisecond))
	require.NoError(t, err)
	require.True(t, ok)
	require.NoError(t, store.Put(ctx, "k", "kept"))
	time.Sleep(30 * time.Millisecond)
	value, found, err := store.Get(ctx, "k")
	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, "kept", value)

	require.NoError(t, store.Delete(ctx, "k"))
	ok, err = store.PutIfAbsentUntil(ctx, "k", "v", time.Now().Add(time.Hour))
	require.NoError(t, err)
	assert.True(t, ok)
}

func TestMemoryStoreSweepsExpiredClaims(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := NewMemoryStore()

	ok, err := store.PutIfAbsentUntil(ctx, "stale", "v", time.Now().Add(10*time.Millisecond))
	require.NoError(t, err)
	require.True(t, ok)
	time.Sleep(20 * time.Millisecond)
	store.mu.Lock()
	store.lastSweep = time.Time{}
	store.mu.Unlock()

	ok, err = store.PutIfAbsentUntil(ctx, "fresh", "v", time.Now().Add(time.Hour))
	require.NoError(t, err)
	require.True(t, ok)
	store.mu.Lock()
	defer store.mu.Unlock()
	assert.NotContains(t, store.values, "stale")
	assert.NotContains(t, store.expiries, "stale")
	assert.Contains(t, store.values, "fresh")
}

func TestRedisStorePutIfAbsentUntil(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	server, err := miniredis.Run()
	require.NoError(t, err)
	defer server.Close()
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	defer client.Close()
	store := NewRedisStore(client, 0)

	ok, err := store.PutIfAbsentUntil(ctx, "k", "v", time.Now().Add(-time.Second))
	require.NoError(t, err)
	assert.False(t, ok, "an expiry in the past must not claim")
	assert.False(t, server.Exists("k"))

	ok, err = store.PutIfAbsentUntil(ctx, "k", "v", time.Now().Add(time.Minute))
	require.NoError(t, err)
	require.True(t, ok)
	ttl := server.TTL("k")
	assert.Greater(t, ttl, 59*time.Second)
	assert.LessOrEqual(t, ttl, time.Minute+time.Millisecond)

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
