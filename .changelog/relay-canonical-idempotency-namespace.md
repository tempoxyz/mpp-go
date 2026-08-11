---
github.com/tempoxyz/mpp-go: patch
---

Send relay broadcast `Idempotency-Key` headers under the canonical `mpp_` namespace instead of `mppx_`. The key is otherwise byte-identical (Keccak-256 of the signed transaction, or SHA-256 of the canonical relay input), so the previous prefix prevented the relay from deduplicating equivalent canonical and Go submissions of the same transaction or credential retry. Matches `src/tempo/server/Relay.ts` in the canonical mppx implementation.
