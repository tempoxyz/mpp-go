---
github.com/tempoxyz/mpp-go: patch
---

Fixed `ParseChallenge` to reject unquoted auth-param values that are empty or not tokens, as mppx does.
