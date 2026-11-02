---
github.com/tempoxyz/mpp-go: patch
---

The Tempo client now runs a configured `PaymentPolicy` before signing a zero-amount proof too (with amount "0" and the chain verified with the RPC), as mppx's client policy does, so `AllowChainIDs` and currency or recipient allowlists also gate proofs. Without a policy, proofs are still signed.
