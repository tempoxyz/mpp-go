---
github.com/tempoxyz/mpp-go: minor
---

**Rolling upgrades: a fleet that mixes this release with replicas on 0.4.0 to 0.6.0 MUST create its servers with `server.WithChallengeNonce(false)` (or set `VerifyParams.DisableChallengeNonce`) until every replica runs this release.** Those released verifiers reject `_mppx_nonce` Challenges, possibly after a hash payment has settled. Drop the option once the upgrade is complete.

Every issued Challenge is now unique by default, as in mppx: the server binds a random `_mppx_nonce` (32 bytes as 0x-prefixed hex, mppx's `ChallengeMeta.withNonce`) into its opaque metadata, so identical charges no longer share a Challenge ID. `VerifyParams.EnableChallengeNonce` is replaced by `VerifyParams.DisableChallengeNonce`. Verification accepts Challenges without a nonce and ones an earlier release issued with `_mpp_nonce`, and both keys stay reserved in `ChargeParams.Meta`.
