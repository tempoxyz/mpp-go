---
github.com/tempoxyz/mpp-go: patch
---

`B64Decode`, and so Challenge, Credential and Payment-Receipt parsing, started rejecting characters outside the base64url alphabet, including the CR and LF that `encoding/base64` skipped. Challenge parsing and formatting rejected a request that decoded to JSON `null`, matching mppx, and the HTTP client returned a 402 unpaid when a credential the caller set could not be parsed.
