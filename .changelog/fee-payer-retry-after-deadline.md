---
github.com/tempoxyz/mpp-go: patch
---

Fixed a sponsored charge retry that arrives after its fee payer deadline being rejected for lack of remaining validity although its transaction was already included; with a local fee payer, the retry now recovers its receipt.
