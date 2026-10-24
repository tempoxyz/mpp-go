---
github.com/tempoxyz/mpp-go: patch
---

Broadcast Tempo charge transactions with secp256k1 sender signatures, including the inner secp256k1 signature of keychain (access key) envelopes, in the node's canonical encoding (recovery ID 27/28 instead of 0/1), so the reserved replay key matches the transaction hash the node derives and reports as the receipt reference. Matches `src/tempo/server/Charge.ts` in the canonical mppx implementation (wevm/mppx#818).
