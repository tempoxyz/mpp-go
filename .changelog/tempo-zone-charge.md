---
github.com/tempoxyz/mpp-go: minor
---

Added Zone payments to the Tempo charge method, as in mppx: servers accept them with `Config.Zones` or `ChargeParams.Zones` and verify `zone` credentials against the Zone portal's settlement on Tempo, and clients pay from a Zone balance with `Config.Zone`. A retried payment resumes a pending withdrawal instead of withdrawing again, and `Zone.MaxWithdrawalFee` caps the outbox fee.
