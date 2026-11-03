package tempo

import (
	"context"
	"time"
)

// AtomicStore is a Store that can replace or delete a key only while it still
// holds an expected value. The Tempo subscription store needs it to claim
// activations and renewals once across replicas, as mppx's AtomicStore.update.
//
// A wrapper that changes how keys or values are stored (a key prefix,
// encryption) must override CompareAndSwap and CompareAndDelete as well as
// Get and Put: methods promoted from an embedded store bypass the wrapper.
type AtomicStore interface {
	Store
	// CompareAndSwap stores next under key only while key holds *old, or,
	// with old nil, only while key is unset. It reports whether it stored
	// next. Values it stores never expire, whatever TTL the store applies to
	// Put, so subscriptions, access keys and claims written with it persist.
	CompareAndSwap(ctx context.Context, key string, old *string, next string) (bool, error)
	// CompareAndDelete deletes key only while it holds old. It reports
	// whether it deleted key.
	CompareAndDelete(ctx context.Context, key, old string) (bool, error)
}

var (
	_ AtomicStore = (*MemoryStore)(nil)
	_ AtomicStore = (*RedisStore)(nil)
)

// CompareAndSwap stores next under key only while key holds *old, or, with
// old nil, only while key is unset. The value never expires.
func (s *MemoryStore) CompareAndSwap(_ context.Context, key string, old *string, next string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.evictExpiredLocked(key, time.Now())
	current, exists := s.values[key]
	if old == nil && exists || old != nil && (!exists || current != *old) {
		return false, nil
	}
	s.values[key] = next
	delete(s.expiries, key)
	return true, nil
}

// CompareAndDelete deletes key only while it holds old.
func (s *MemoryStore) CompareAndDelete(_ context.Context, key, old string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.evictExpiredLocked(key, time.Now())
	if current, exists := s.values[key]; !exists || current != old {
		return false, nil
	}
	delete(s.values, key)
	delete(s.expiries, key)
	return true, nil
}

// compareAndSwapScript sets KEYS[1] to ARGV[3], without a TTL, only while it
// is unset (ARGV[1] = "0") or holds ARGV[2].
const compareAndSwapScript = `
local current = redis.call('GET', KEYS[1])
if ARGV[1] == '0' then
  if current then return 0 end
elseif current ~= ARGV[2] then
  return 0
end
redis.call('SET', KEYS[1], ARGV[3])
return 1
`

// compareAndDeleteScript deletes KEYS[1] only while it holds ARGV[1].
const compareAndDeleteScript = `
if redis.call('GET', KEYS[1]) == ARGV[1] then
  return redis.call('DEL', KEYS[1])
end
return 0
`

// CompareAndSwap stores next under key only while key holds *old, or, with
// old nil, only while key is unset, atomically with a Lua script. Unlike
// Put, it never applies the store's TTL, as PutIfAbsent does not: the
// subscription store writes every value through it, so a RedisStore with a
// TTL for replay protection does not silently expire subscriptions.
func (s *RedisStore) CompareAndSwap(ctx context.Context, key string, old *string, next string) (bool, error) {
	hasOld, oldValue := "0", ""
	if old != nil {
		hasOld, oldValue = "1", *old
	}
	result, err := s.client.Eval(ctx, compareAndSwapScript, []string{key}, hasOld, oldValue, next).Int()
	if err != nil {
		return false, err
	}
	return result == 1, nil
}

// CompareAndDelete deletes key only while it holds old, atomically with a Lua
// script.
func (s *RedisStore) CompareAndDelete(ctx context.Context, key, old string) (bool, error) {
	result, err := s.client.Eval(ctx, compareAndDeleteScript, []string{key}, old).Int()
	if err != nil {
		return false, err
	}
	return result == 1, nil
}
