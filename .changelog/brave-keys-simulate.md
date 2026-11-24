---
github.com/tempoxyz/mpp-go: patch
---

Added `SpendingLimitExceededError` and `InsufficientBalanceError` to the Tempo charge client. The client now returns one of them, instead of signing, when the gas estimate before signing reverts because the access key's spending limit or the account's balance cannot cover the payment.
