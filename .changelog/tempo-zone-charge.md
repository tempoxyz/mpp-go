---
github.com/tempoxyz/mpp-go: minor
---

Added Zone payments to the Tempo charge method, as in mppx. A server accepts withdrawals from Zones with `Config.Zones` or `ChargeParams.Zones` (`tempo.Zones{IDs: []uint32{3, 7}}`, or `tempo.Zones{Any: true}` for any Zone), advertised as `methodDetails.zones`, and verifies `zone` credentials against the Zone portal's settlement on Tempo. A client pays from a Zone balance with `Config.Zone`: it withdraws to the recipient with a challenge-bound memo, waits for the withdrawal to settle on Tempo, and sends a `zone` credential with the settlement transaction hash.

If the wait fails, the client returns `*ZoneSettlementPendingError` and resumes the same withdrawal when the challenge is retried.
