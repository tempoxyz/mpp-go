---
github.com/tempoxyz/mpp-go: patch
---

Payment retries stopped creating a Credential once the call's context was canceled, such as during `OnPaymentRequired` approval. A cancel after a pending settlement now returns the cause joined with the pending error.

