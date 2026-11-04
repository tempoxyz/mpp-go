---
github.com/tempoxyz/mpp-go: minor
---

**API break: `tempo.RedisStore` and `tempo.NewRedisStore` moved to the new `github.com/tempoxyz/mpp-go/pkg/tempo/redisstore` package as `redisstore.Store` and `redisstore.New`.** Replace `tempo.NewRedisStore(client, ttl)` with `redisstore.New(client, ttl)`; the behavior is unchanged. Importing `pkg/tempo` (and the Tempo client and server packages) no longer links `github.com/redis/go-redis/v9`: only programs that use the Redis store do.
