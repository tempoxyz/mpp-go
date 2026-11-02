---
github.com/tempoxyz/mpp-go: patch
---

Accept the RFC 9530 byte-sequence form (`sha-256=:base64:`) in `VerifyBodyDigest`, alongside the existing bare form. `ComputeBodyDigest` still emits the bare form, as mppx does.
