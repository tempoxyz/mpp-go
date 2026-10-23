---
github.com/tempoxyz/mpp-go: patch
---

Stopped sending idempotency keys on Tempo relay broadcasts, so a cached relay receipt can no longer authorize a reused credential again; the relay consumes each credential once. Recover uncertain settlement outcomes separately from running the protected operation.
