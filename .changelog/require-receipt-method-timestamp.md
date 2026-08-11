---
github.com/tempoxyz/mpp-go: patch
---

Reject payment receipts that omit the `method` or `timestamp` field when parsing. Both are required base receipt fields per draft-ietf-httpauth-payment §5.3 and the canonical mppx schema; previously an incomplete receipt parsed successfully with an empty method and zero timestamp.
