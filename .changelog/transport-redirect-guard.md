---
github.com/tempoxyz/mpp-go: patch
---

Refuse standalone `Transport` auto-pay after a redirect. A `Transport` used with a bare `http.Client` (no `CheckRedirect`) had none of `Client.Do`'s cross-origin redirect protection, so a redirect to an attacker origin could be auto-paid. The Transport now fails closed on any redirect-produced request when no trusted origin is pinned in the context.
