---
github.com/tempoxyz/mpp-go: patch
---

Refused to sign a sponsored Tempo charge transaction when slow preparation (gas or access-key RPCs) left the challenge or the transaction's `valid_before` with less than 15 seconds of validity: the client checks both again right before signing and reports the slow-preparation case with `ErrSponsoredPreparationTooSlow`.
