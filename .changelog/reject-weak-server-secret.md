---
github.com/tempoxyz/mpp-go: minor
---

Reject server secret keys shorter than 32 bytes at construction instead of accepting forgeable HMAC-bound challenge IDs. `server.New` now returns `(*Mpp, error)`.
