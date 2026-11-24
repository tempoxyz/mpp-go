---
github.com/tempoxyz/mpp-go: patch
---

Zone clients paid from a Zone whose RPC redacts the outbox's `WithdrawalRequested` event from receipts, deriving the sender tag from the outbox's `lastFallbackNonce`; a failed read of it is retried with the challenge without withdrawing again. `ZoneSettlementPendingError` gained `SenderTags`, every candidate tag, with `SenderTag` empty when more than one remains.
