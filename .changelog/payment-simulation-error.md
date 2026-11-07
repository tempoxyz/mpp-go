---
github.com/tempoxyz/mpp-go: patch
---

Added `PaymentSimulationError` to the Tempo charge server, as mppx's: a fee-sponsored transaction whose sender or sponsored preflight simulation the node rejects now carries one as the cause of the unchanged "transaction preflight failed" verification error, naming the failing phase, the sender, the fee payer (sponsored phase) and the payment token (single TIP-20 transfer or transferWithMemo call), with the node's error as its cause. Transport failures are not tagged.
