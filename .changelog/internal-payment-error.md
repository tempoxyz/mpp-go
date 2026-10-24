---
github.com/tempoxyz/mpp-go: patch
---

Unexpected (non-`mpp.PaymentError`) failures from an intent's verification or settlement now return a 500 `internal-payment-error` problem with a fixed detail, instead of a 402 `verification-failed` problem that echoed the internal error message. Added `mpp.ErrInternalPayment` and `mpp.ErrorTypeInternalPayment`.
