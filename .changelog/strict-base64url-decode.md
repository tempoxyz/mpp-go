---
github.com/tempoxyz/mpp-go: patch
---

`B64Decode`, and so Challenge, Credential and Payment-Receipt parsing, started rejecting characters outside the base64url alphabet (including the CR and LF that `encoding/base64` skipped) and JSON other than an object, so a Challenge whose request decoded to `null` no longer parsed, matching mppx.
