---
github.com/tempoxyz/mpp-go: minor
---

Bind zero-amount Tempo proofs on MCP to the session that received the challenge: the client signs or reuses one only when the session's Streamable HTTP endpoint origin is trusted for the realm (by default an HTTPS endpoint whose hostname is the realm), so a server can no longer relay another server's challenge through a shared client. Sessions without an endpoint origin (stdio, in-memory) need the optional Config.ValidateTempoProofSession, and origin-less Config.TrustedProofRealms entries no longer cover Streamable HTTP sessions. A cached Credential is reused only for an identical re-offered challenge.
