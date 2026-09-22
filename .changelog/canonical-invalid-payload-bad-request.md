---
github.com/tempoxyz/mpp-go: patch
---

Use the canonical `https://paymentauth.org/problems/` URIs for `invalid-payload` and `bad-request`, which the core spec now defines, and return 402 for `invalid-payload` so a rejected credential payload gets a fresh challenge to retry against.
