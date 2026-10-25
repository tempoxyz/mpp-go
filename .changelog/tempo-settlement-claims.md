---
github.com/tempoxyz/mpp-go: patch
---

Tempo charge settlement no longer locks out a paid credential. When the receipt wait fails after a successful broadcast, the verifier releases its replay claims and returns a verification error saying the outcome is unknown; a retry with the same credential re-broadcasts the signed transaction, which the node includes at most once, and recovers its receipt, as in mppx. The receipt wait now outlives a client disconnect within its existing budget, sponsored charges release their claim the same way, and claims are kept only for a transaction that was included (reverted, or not satisfying the charge). The verifier rejects a broadcast hash that differs from the signed transaction's hash, and a failure to release a claim after a failed broadcast is now reported instead of ignored.

Add the optional `tempo.ExpiringStore` interface (`PutIfAbsentUntil`), implemented by `MemoryStore` and `RedisStore`. When the replay store implements it, the verifier lets replay keys expire ten minutes after the challenge's expiry, the moment after which the credential can no longer be accepted. Other stores keep replay keys forever. `RedisStore.Get` matches `redis.Nil` with `errors.Is`.
