---
github.com/tempoxyz/mpp-go: minor
---

Added Zone payments to the Tempo charge method, as in mppx: servers accept them with `Config.Zones` or `ChargeParams.Zones` and verify `zone` credentials against the Zone portal's settlement on Tempo, and clients pay from a Zone balance with `Config.Zone`. Retrying the same challenge (`ZoneSettlementPendingError.Challenge`) resumes a pending withdrawal instead of withdrawing again, as in mppx; a new request's fresh challenge withdraws again, and `Zone.MaxWithdrawalFee` caps the outbox fee.
