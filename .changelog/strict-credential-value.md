---
github.com/tempoxyz/mpp-go: patch
---

`ParseCredential` started requiring the value to be exactly `Payment` followed by one base64url token, as mppx does, and its errors no longer quote the value. The HTTP client returned a 402 to the caller, without paying again, when a credential the caller set could not be parsed.
