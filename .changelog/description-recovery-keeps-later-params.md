---
github.com/tempoxyz/mpp-go: patch
---

Fixed WWW-Authenticate parsing to keep the parameters that follow a legacy description carrying unescaped quotes. Parsing stopped at the malformed value, so `digest`, `expires`, `header` and `opaque` — all of which the canonical order emits after `description` — were silently dropped and the challenge parsed as though they had never been sent.
