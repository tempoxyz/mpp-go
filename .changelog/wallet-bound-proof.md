---
github.com/tempoxyz/mpp-go: patch
---

Wallet-bind Tempo zero-amount proofs to close cross-account replay. The EIP-712 `Proof` message now leads with the payer `account` address (then `challengeId`, `realm`) and the MPP domain version is `"3"`. Verifiers rebuild the digest from the credential `source`, so client and server must both use v3. `ProofTypedDataHash` now takes an `account common.Address`; `ProofTypedData` exposes the typed data.

Note: v3 is not yet interoperable with the mppx (TypeScript) SDK, which still uses v2 (`Proof = [challengeId, realm]`, no `account`).
