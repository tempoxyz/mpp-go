---
github.com/tempoxyz/mpp-go: patch
---

Report unexpected (non-`PaymentError`) verifier errors as a sanitized HTTP 500 `internal-payment-error` problem instead of a 402 `verification-failed` that echoed the original error text, matching mppx. Added `mpp.ErrInternalPayment`, which keeps the original error available via `errors.Unwrap`.
