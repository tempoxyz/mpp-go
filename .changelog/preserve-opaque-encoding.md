---
github.com/tempoxyz/mpp-go: patch
---

Echo a challenge's `opaque` parameter exactly as the issuer encoded it. Parsing decoded `opaque` into a map and formatting re-encoded it as canonical JSON, so a non-canonical value (for example from an `mppx` server that sets `opaque` directly) changed in the `WWW-Authenticate` header and in the credential's challenge echo, and the issuer's challenge-ID check rejected the credential.
