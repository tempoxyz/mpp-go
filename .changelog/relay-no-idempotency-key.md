---
github.com/tempoxyz/mpp-go: patch
---

Stopped sending idempotency keys on Tempo relay broadcasts, so a cached relay receipt can no longer authorize a reused credential again; the relay consumes each credential once. When the broadcast exchange fails, the 402 now carries the settlement_unknown detail code so apps can reconcile the outcome separately instead of re-running the protected operation.
