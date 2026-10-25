---
github.com/tempoxyz/mpp-go: patch
---

`server.WritePaymentError` and the Fiber `WritePaymentError` now answer an error that is not an `mpp.PaymentError` with a 500 `internal-payment-error` problem and a fixed detail, instead of a 402 `verification-failed` problem that echoed the error's text to the client.
