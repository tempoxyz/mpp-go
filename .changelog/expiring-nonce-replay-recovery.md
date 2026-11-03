---
github.com/tempoxyz/mpp-go: patch
---

Fixed Tempo charge settlement failing after a paid transaction was accepted: a broadcast rejected with `ExpiringNonceReplay` (for example, a retried send) now waits for the transaction's receipt instead of failing.
