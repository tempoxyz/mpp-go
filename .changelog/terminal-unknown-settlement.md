---
github.com/tempoxyz/mpp-go: patch
---

Return ambiguous relay settlement as a terminal payment-outcome-unknown response without retry offers, including unusable successful receipts. Preserve settlement_unknown metadata for application reconciliation.
