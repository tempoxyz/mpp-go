---
github.com/tempoxyz/mpp-go: patch
---

Reject empty required auth-params (`id`, `realm`, `method`, `intent`, `request`) in `FormatAuthenticateStrict`; an empty value was previously omitted from the header, producing a `WWW-Authenticate` challenge that `ParseChallenge` itself rejects. The lenient `FormatAuthenticate` path is unchanged.
