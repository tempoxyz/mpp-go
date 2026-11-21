---
github.com/tempoxyz/mpp-go: patch
---

`ParseChallenge` now reads a backslash in a quoted auth-param as an RFC 9110 quoted-pair and no longer decodes `\uXXXX`, so `realm="a\u2014b"` parses as `au2014b`. `FormatChallenge` now rejects characters above U+00FF in every auth-param but `description`, which keeps its `\uXXXX` escape.
