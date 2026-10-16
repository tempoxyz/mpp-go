---
github.com/tempoxyz/mpp-go: patch
---

Check that a request can be sent again before creating a payment credential for it. A request whose body cannot be replayed (no `GetBody`, or `GetBody` failing) now returns an error without calling the payment method, instead of creating a credential the retry could never deliver.
