---
github.com/tempoxyz/mpp-go: patch
---

Unified the server charge flow and added server APIs.

- One framework-neutral flow now runs `ChargeMiddleware`, `ComposeMiddleware` and the Gin, Echo and Fiber adapters; `ChargeMiddleware` behaves as `ComposeMiddleware` with one offer.
- Added `server.Compose` (replaces the deprecated `ComposeCharge`), the `server.Offer` alias of `ComposeConfig`, `(*Mpp).ChargeRequest` and `server.ComposeRequest`, which read the credential, body and route scope from an `*http.Request`, and `(*ChargeResult).ServeHTTP`, which writes the 402 or problem response. Added `(*VerifyResult).IsChallenge`.
- Added `ComposeMiddleware` to the Gin, Echo and Fiber adapters.
- Added `server.WithChallengeNonce`; `ChargeParams.EnableChallengeNonce` is deprecated and still enables the nonce for its call.
- Added `server.WithEvents` with `ChallengeCreated`, `PaymentSucceeded` and `PaymentFailed` hooks, like mppx's `onChallengeCreated`, `onPaymentSuccess` and `onPaymentFailed`. `ChallengeCreated` fires only for Challenges a response returns, after the whole set is issued, and a panic in any hook is recovered so it cannot abort a request.
- Call `res.ServeHTTP(w, r)` on every `ChargeRequest` or `ComposeRequest` result before writing the resource: for a verified payment it sets `Payment-Receipt` and `Cache-Control: private`.
- `server.New` now defaults an empty secret key to `MPP_SECRET_KEY` and rejects a typed nil `Method`. An empty realm stays empty; pass `server.DetectRealm()` for the environment's realm.
- Behavior changes: the initial `402` from `ComposeMiddleware` and the Fiber `ChargeMiddleware` no longer carries a problem body, like `ChargeMiddleware` (`fiberadapter.WritePaymentResponse` still writes one). `ChargeMiddleware` and the adapters answer a Credential for a payment method the route does not offer with `400` method-unsupported, like `ComposeMiddleware`, instead of a `402` invalid-challenge with a retry offer; a Credential for an offered method with an intent or request no offer matches still gets the `402` with retry offers. In a composition mixing handlers with and without `WithRequiresAuth`, each offer accepts a Credential only from the header it reads (`Payment-Authorization` or `Authorization`); one sent in the other header gets a `402` invalid-challenge.
