---
github.com/tempoxyz/mpp-go: minor
---

Add a client-side `Authorize` hook (`Transport.Authorize` field and `client.WithAuthorize` option) that is called with the matched challenge before a payment credential is created. Returning an error declines the payment and returns the server's original 402 response unmodified. This gives Go clients the interception point the core spec's "Amount Verification" requirements assume, matching mppx's `onChallenge`, mpp-rs's `challenge.received` handlers, and pympp's `on_challenge_received`.
