---
github.com/tempoxyz/mpp-go: patch
---

Prevented duplicate Zone payments when a previous withdrawal remains unresolved, went out late or settled after its challenge's redemption window: matching challenges now fail closed until the caller reconciles it. Added `Method.ResetZoneWithdrawal` to permit a new payment after manual reconciliation, `Method.ZoneWithdrawalTransactionHash`, and `ZoneSettlementPendingError.TransactionHash`.
