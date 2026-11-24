---
github.com/tempoxyz/mpp-go: patch
---

Added `SpendingLimitExceededError` and `InsufficientBalanceError` to the Tempo charge client. When the gas estimate before signing reverts with the access key's `SpendingLimitExceeded` or the token's `InsufficientBalance`, the client now returns one of them instead of signing (or broadcasting) a transaction that would revert on chain, as mppx does. The estimate names the signing access key (`keyId`) so the node checks its spending limits.
