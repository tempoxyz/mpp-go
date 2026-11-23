---
github.com/tempoxyz/mpp-go: patch
---

`B64Decode`, and so Challenge, Credential and Payment-Receipt parsing, started rejecting characters outside the base64url alphabet, including the CR and LF that `encoding/base64` skipped. `ParseChallenge` rejected a request that decoded to JSON `null`, matching mppx.
