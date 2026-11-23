---
github.com/tempoxyz/mpp-go: patch
---

`ParseCredential` started requiring the value to be exactly `Payment` followed by one base64url token, as mppx does, and rejected a comma-separated list of credentials or text after the token. Servers still extracted the Payment credential from a merged `Authorization` header with `FindPaymentAuthorizationStrict` before parsing it.
