---
github.com/tempoxyz/mpp-go: patch
---

Made the live integration tests self-contained: they moved to the nested module `tests/`, which starts the pinned Tempo localnet from `docker-compose.yml` with testcontainers when `TEMPO_RPC_URL` is unset. Added live tests for each framework adapter, a paid MCP tool call, `tempo.RedisStore` replay protection against a real Redis, and the examples' smoke tests, plus a cross-SDK smoke workflow against mppx. The examples' devnet helpers now stop when their context ends and wrap the last RPC error.
