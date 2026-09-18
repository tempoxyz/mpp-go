---
github.com/tempoxyz/mpp-go: patch
---

Decode `\uXXXX` escapes when parsing WWW-Authenticate quoted-strings. A header value cannot carry characters above Latin-1, so a challenge escapes them this way; the backslash was being dropped as a plain quoted-pair, leaving the literal text `u2014` in place of `—`. Surrogate pairs are recombined, and an unpaired surrogate decodes to U+FFFD.
