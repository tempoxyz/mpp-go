---
github.com/tempoxyz/mpp-go: minor
---

Every issued Challenge is now unique by default, as in mppx: the server binds a random `_mppx_nonce` (32 bytes as 0x-prefixed hex, mppx's `ChallengeMeta.withNonce`) into its opaque metadata, so identical charges no longer share a Challenge ID. `server.WithChallengeNonce(false)` turns issuance nonces off while some replica still runs a verifier that rejects them; `VerifyParams.EnableChallengeNonce` is replaced by `VerifyParams.DisableChallengeNonce`. Verification accepts Challenges without a nonce and ones an earlier release issued with `_mpp_nonce`, and both keys stay reserved in `ChargeParams.Meta`. Replicas on a release before this one reject `_mppx_nonce` Challenges, so upgrade every replica, or pass `WithChallengeNonce(false)` during the rollout.
