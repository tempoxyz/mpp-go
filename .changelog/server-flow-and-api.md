---
github.com/tempoxyz/mpp-go: patch
---

Unified the server charge flow and added server APIs.

- One framework-neutral flow now runs `ChargeMiddleware`, `ComposeMiddleware` and the Gin, Echo and Fiber adapters; `ChargeMiddleware` behaves as `ComposeMiddleware` with one offer.
- Added `server.Compose` (replaces the deprecated `ComposeCharge`), the `server.Offer` alias of `ComposeConfig`, `(*Mpp).ChargeRequest` and `server.ComposeRequest`, which read the credential, body and route scope from an `*http.Request`, and `(*ChargeResult).ServeHTTP`, which writes the 402 or problem response. Added `(*VerifyResult).IsChallenge`.
- Added `ComposeMiddleware` to the Gin, Echo and Fiber adapters.
- Added `server.WithChallengeNonce`; `ChargeParams.EnableChallengeNonce` is deprecated and still enables the nonce for its call.
- Added `server.WithEvents` with `ChallengeCreated`, `PaymentSucceeded` and `PaymentFailed` hooks, like mppx's `onChallengeCreated`, `onPaymentSuccess` and `onPaymentFailed`.
- `server.New` now defaults an empty secret key to `MPP_SECRET_KEY` and rejects a typed nil `Method`. An empty realm stays empty; pass `server.DetectRealm()` for the environment's realm.
- Behavior changes: the initial `402` from `ComposeMiddleware` and the Fiber `ChargeMiddleware` no longer carries a problem body, like `ChargeMiddleware` (`fiberadapter.WritePaymentResponse` still writes one). `ChargeMiddleware` and the adapters answer a Credential for a payment method the route does not offer with `400` method-unsupported, like `ComposeMiddleware`, instead of a `402` invalid-challenge with a retry offer.
