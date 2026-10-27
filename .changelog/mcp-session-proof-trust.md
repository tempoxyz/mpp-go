---
github.com/tempoxyz/mpp-go: minor
---

Require Config.ValidateTempoProofSession to authorize the actual MCP session and realm before signing or reusing zero-amount Tempo proofs. Realm allowlists alone no longer authorize proofs on MCP; payment-method origin validation still applies.
