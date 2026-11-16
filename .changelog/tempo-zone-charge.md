---
github.com/tempoxyz/mpp-go: minor
---

Added Zone payments to the Tempo charge method, as in mppx: servers accept them with `Config.Zones` or `ChargeParams.Zones` and verify `zone` credentials against the Zone portal's settlement on Tempo, and clients pay from a Zone balance with `Config.Zone`. One payment intent never makes two withdrawals: a retried or new challenge for the same terms resumes the pending withdrawal (or fails with `ZoneSettlementPendingError`), the HTTP and MCP clients retry the same challenge while it settles, `ZoneCredentialGrace` lets servers accept a zone credential settled before its challenge expired, hash credentials never redeem a Zone portal's transfers, and the withdrawal fee counts against `PaymentPolicy` limits.
