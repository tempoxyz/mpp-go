---
github.com/tempoxyz/mpp-go: patch
---

Reject a co-signed fee payer transaction with `ValidBefore == 0` (or already expired) after remote co-signing, not just before it. `validateFeePayerTransaction` only enforces its sponsor-policy upper bound when `ValidBefore` is non-zero, and the standalone zero/expired guard applied to the original client-submitted transaction was never re-applied to the transaction returned by a remote fee payer (`FeePayerURL`), so a co-signed response with `ValidBefore` reset to 0 could bypass the sponsor's validity-window policy entirely.
