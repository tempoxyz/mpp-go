---
github.com/tempoxyz/mpp-go: patch
---

Match challenge auth-param names case-insensitively as RFC 9110 §11.2 requires, so mixed-case headers parse correctly and duplicate names that differ only in case are rejected instead of silently ignored.
