---
github.com/tempoxyz/mpp-go: minor
---

Report the currency that funded a Tempo charge in its receipt. Added `Receipt.FundingCurrency` (the `fundingCurrency` receipt field) and `mpp.WithFundingCurrency`. The Tempo server sets it to the charge currency on transaction-credential receipts, and on hash-credential receipts when the transaction was sent by the credential's payer and contains only the expected transfers; otherwise it is left unset without failing the payment.
