---
github.com/tempoxyz/mpp-go: patch
---

Reserve sponsored charge transactions by the hash of the payer-signed envelope instead of the challenge ID, so distinct payers answering the same challenge are no longer rejected as replays. `tempo.ChargeSponsoredChallengeStoreKey` is replaced by `tempo.ChargeSponsoredTransactionStoreKey`.
