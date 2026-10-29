---
github.com/tempoxyz/mpp-go: patch
---

Fixed a sponsored charge retry being rejected as `sponsored transaction already used` when the final replay claim failed after the transaction was included; the retry now recovers its receipt.
