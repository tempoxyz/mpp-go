---
github.com/tempoxyz/mpp-go: patch
---

Parsed `WWW-Authenticate` auth-param names case-insensitively. Challenges such as `ID="…", Realm="…"` no longer fail with missing required fields, and `id` plus `ID` is now rejected as a duplicate.
