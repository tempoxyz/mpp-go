---
github.com/tempoxyz/mpp-go: patch
---

Tempo charge now recovers a transaction credential whose broadcast returned an RPC error but was confirmed anyway: it looks up the receipt once (without rebroadcasting) and, when it succeeded and matches the charge, completes the payment instead of failing it.
