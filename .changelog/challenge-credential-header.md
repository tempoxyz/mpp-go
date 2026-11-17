---
github.com/tempoxyz/mpp-go: patch
---

Restricted the challenge `header` auth-param to `Payment-Authorization`, as the spec requires: a received challenge with any other value (including `Authorization` or empty) now fails with `mpp.ErrUnsupportedCredentialHeader` and gets no credential, while clients still pay another challenge in the same response. `WithHeader` and `VerifyParams.Header` now reject names other than Authorization and Payment-Authorization.
