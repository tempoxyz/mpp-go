---
github.com/tempoxyz/mpp-go: minor
---

Aligned servers and clients with the core and Tempo charge specifications: replays now get 402 invalid-challenge with a fresh Challenge, Challenges carry a 16-byte `_mpp_nonce` (`_mppx_nonce` still verifies), credentials are checked in the specified order with multiple credentials rejected as 400 and unselected or bare ones ignored, and `VerifyOrChallenge` refuses secrets under 32 bytes. Tempo servers no longer require the attribution memo but reject one bound to another challenge and enforce a new `methodDetails.memo` (`ChargeParams.Memo`), and an `OnChallenge` hook returning `client.ErrPaymentDeclined` now returns the 402 without paying.
