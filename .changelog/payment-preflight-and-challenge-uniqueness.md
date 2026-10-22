---
github.com/tempoxyz/mpp-go: patch
---

Add `mpp.FormatAuthorizationStrict` and preflight Tempo hash credential size before submitting a transaction. Fiber charge middleware now reads streamed request bodies within the 4 MiB limit and closes connections after oversized or failed reads.

When `EnableChallengeNonce` is enabled, server-issued challenges include a random, HMAC-bound `_mpp_nonce` in opaque metadata, including when an explicit expiry is reused. This metadata key is reserved. Composed offer selection still compares application metadata. Previously issued challenges remain valid until their existing expiry; low-level `mpp.NewChallenge` remains deterministic.

Unique challenges prevent unrelated callers from obtaining identical challenges, but do not authenticate the redeemer of a stolen complete credential. Applications requiring caller identity must bind trusted session/account context through `ChargeParams.Meta` and enforce their own authorization. Keep payment credentials confidential and use a shared atomic replay store across replicas.

Nonce issuance is opt-in: first roll out the default `EnableChallengeNonce: false` to every replica, then set `ChargeParams.EnableChallengeNonce: true` (or `VerifyParams.EnableChallengeNonce`) after every verifier is upgraded. The default preserves legacy challenge construction but retains shared-challenge collision risk until operators enable issuance. Old verifiers reject nonced hash payments after submission, so enabling before all replicas are ready is unsafe. Version-sticky retries are an alternative. Transient verification failures reissue the authenticated challenge, preserving client credential reuse; invalid-payload failures still issue fresh challenges. Strict formatting propagates serialization errors.
