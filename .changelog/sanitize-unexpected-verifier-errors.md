---
github.com/tempoxyz/mpp-go: patch
---

Report unexpected (non-PaymentError) errors from payment verification as `internal-payment-error` with HTTP 500 and a fixed detail, instead of `verification-failed` 402 carrying the error text. The original error stays reachable through `errors.Is`/`errors.As`. Applies to the net/http, Gin, Echo, Fiber and compose middleware.
