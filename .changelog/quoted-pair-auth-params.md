---
github.com/tempoxyz/mpp-go: patch
---

Changed `ParseChallenge` to read a backslash in a quoted auth-param as an RFC 9110 quoted-pair, so `realm="a\u2014b"` parses as `au2014b`, and made `FormatChallenge` reject characters above U+00FF in every auth-param but `description`, which keeps its `\uXXXX` escape. A server whose realm holds such a character can no longer issue HTTP Challenges (each 402 becomes a 500), so change it to an ASCII realm before upgrading.
