---
github.com/tempoxyz/mpp-go: minor
---

Bind zero-amount Tempo proofs on MCP to the session that received the challenge: the client signs or reuses one only when the session's Streamable HTTP endpoint origin is trusted for the realm (by default an HTTPS endpoint whose hostname is the realm) and its HTTP client refuses cross-origin redirects (pass the new mcpclient.HTTPClient as the transport's HTTPClient), so a server can no longer relay another server's challenge through a shared client. Sessions without an endpoint (stdio, in-memory) need the optional Config.ValidateTempoProofSession, and sessions whose client follows cross-origin redirects are refused; origin-less Config.TrustedProofRealms entries no longer cover Streamable HTTP sessions. A cached Credential is reused only for an identical re-offered challenge.
