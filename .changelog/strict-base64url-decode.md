---
github.com/tempoxyz/mpp-go: patch
---

`B64Decode`, and so Challenge, Credential and Payment-Receipt parsing, now rejects characters outside the base64url alphabet (including CR and LF, which `encoding/base64` skipped) and JSON other than an object, so a Challenge whose request decodes to `null` no longer parses. This matches mppx.
