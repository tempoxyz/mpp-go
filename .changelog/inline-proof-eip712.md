---
github.com/tempoxyz/mpp-go: minor
---

`pkg/tempo` no longer imports go-ethereum's `signer/core/apitypes`: `ProofTypedDataHash` computes the EIP-712 digest of the fixed proof types inline, byte for byte as before, so `pkg/tempo` and the Tempo client and server no longer compile go-eth-kzg, gnark-crypto, c-kzg and their dependencies. `ProofTypedData` now returns the new `tempo.TypedData` (with `TypedDataDomain` and `TypedDataField`) instead of `apitypes.TypedData`; it keeps the same field names and JSON shape, so code reading `Domain`, `Types`, `PrimaryType` and `Message` compiles unchanged, but the return type changes, so this is a minor bump.
