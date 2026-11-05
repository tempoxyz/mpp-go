---
github.com/tempoxyz/mpp-go: patch
---

Fixed the Tempo charge server's final sponsored simulation billing the sender's fee token. Since Tempo v1.18 an `eth_call` checks the fee payer's fee token balance, and the node recovers the fee payer only from `feePayerSignature`, ignoring a bare `feePayer` address, so sponsored payments failed with "insufficient fee token balance" when the sponsor paid in a fee token the sender did not hold. The final simulation now carries the fee payer's signature: a hosted fee payer's as it signed it, and for a local fee payer a simulation-only signature made after the sender's simulation passed, over a copy with 10,000 more gas because a sponsored `eth_call` charges more gas than the mined transaction uses. The broadcast transaction's gas is unchanged.
