---
github.com/tempoxyz/mpp-go: patch
---

Reserve sponsored charge transactions by the hash of the payer-signed envelope instead of the challenge ID, so distinct payers answering the same challenge are no longer rejected as replays. `tempo.ChargeSponsoredChallengeStoreKey` is replaced by `tempo.ChargeSponsoredTransactionStoreKey`.

Replay keys now use the verified payer and canonical sender signing payload. Existing challenge reservations remain honored. Quiesce old sponsor replicas before enabling the new version; mixed-version concurrent admission is not supported. Retain the shared replay store across the upgrade.
