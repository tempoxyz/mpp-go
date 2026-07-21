---
github.com/tempoxyz/mpp-go: patch
---

Strip CR and LF characters in the default `FormatAuthenticate` path so a `Challenge` field containing `\r\n` (e.g. `Description`, `Realm`) can no longer split the `WWW-Authenticate` header and inject a response. `FormatAuthenticateStrict` continues to reject such values with an error.
