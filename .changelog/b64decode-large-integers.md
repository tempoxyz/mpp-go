---
github.com/tempoxyz/mpp-go: patch
---

Decode challenge and credential JSON with `json.Number` so integers above 2^53 keep their digits. Re-encoding a request through `float64` changed the bytes the challenge ID is an HMAC over, making challenges the server itself issued fail verification.
