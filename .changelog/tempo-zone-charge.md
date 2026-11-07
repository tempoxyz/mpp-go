---
github.com/tempoxyz/mpp-go: minor
---

Added Zone payments to the Tempo charge method, as in mppx. A server accepts withdrawals from Zones with `Config.Zones` or `ChargeParams.Zones` (`tempo.Zones{IDs: []uint32{3, 7}}`, or `tempo.Zones{Any: true}` for any Zone), advertised as `methodDetails.zones`, and verifies `zone` credentials against the Zone portal's settlement on Tempo. A `zone` credential names no payer: its `source` is not reported, as in mppx. A client pays from a Zone balance with `Config.Zone`: it withdraws to the recipient with a challenge-bound memo, waits for the withdrawal to settle on Tempo, and sends a `zone` credential with the settlement transaction hash.

If the wait fails, the client returns `*ZoneSettlementPendingError` (or `*ZoneWithdrawalUnknownError` when the withdrawal's receipt cannot be found), carrying the challenge; passing that challenge to the same `Method`'s `CreateCredential` resumes the same withdrawal instead of paying again. The Transport does not pay again after a 402 to a `zone` credential.
