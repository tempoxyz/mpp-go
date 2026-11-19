---
github.com/tempoxyz/mpp-go: patch
---

Payment retries no longer create a Credential once the call's context is cancelled, such as during `OnPaymentRequired` approval.

