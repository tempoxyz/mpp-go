---
github.com/tempoxyz/mpp-go: patch
---

Reject reused non-sponsored transaction credentials before receipt verification, including concurrent submissions.

A transaction reservation remains consumed if receipt lookup fails after submission; retrying that credential no longer authorizes fulfillment.
