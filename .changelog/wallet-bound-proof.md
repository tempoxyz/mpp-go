---
github.com/tempoxyz/mpp-go: patch
---

Breaking Tempo proof credential wire format: zero-amount proofs are now wallet-bound. The canonical EIP-712 `Proof` message is now `account address` first, followed by `challengeId` and `realm`, and the MPP domain version is `"3"`, matching the mppx (TypeScript) SDK. Verifiers rebuild the digest from the credential `source` payer address, so proofs produced with the previous message shape/domain version are rejected; Go and TypeScript clients/servers must use the v3 proof format together. This closes cross-account replay for access keys authorized on multiple accounts.

Breaking Go API: `ProofTypedDataHash` now requires an `account common.Address` parameter. `ProofTypedData` exposes the canonical wallet-bound typed data. Added cross-SDK digest/signature conformance coverage.
