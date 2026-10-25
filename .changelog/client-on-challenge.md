---
github.com/tempoxyz/mpp-go: minor
---

Added an `OnChallenge` hook to the client, matching mppx's `onChallenge`: set it with `client.WithOnChallenge` or `client.WithTransportOnChallenge`. It runs after a 402 challenge is selected and before a credential is created. Return `(nil, nil)` to let the client pay as usual, return a credential (for example from the supplied `createCredential`, which takes a per-request context) to send it, or return an error to decline: the request then fails with that error and nothing is paid.
