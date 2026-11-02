---
github.com/tempoxyz/mpp-go: patch
---

Restored expiry protection when a retry reselected the caller's original Credential after sending another one. An expired caller Credential is returned for reconciliation instead of authorizing a replacement payment.
