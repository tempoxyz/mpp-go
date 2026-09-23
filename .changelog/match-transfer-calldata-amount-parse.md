---
github.com/tempoxyz/mpp-go: patch
---

Reject calldata with a non-hex amount field in `MatchTransferCalldata` instead of silently treating it as a zero amount. The `big.Int.SetString` success flag was previously ignored, so malformed calldata that passed the selector and length checks could be parsed as `amount = 0` and spuriously match a zero-amount `ChargeRequest`.
