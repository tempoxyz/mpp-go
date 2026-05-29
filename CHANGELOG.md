# Changelog

## `github.com/tempoxyz/mpp-go@0.1.0`

### Minor Changes

- Initial public release of `mpp-go`, the Go SDK for the [Machine Payments Protocol](https://mpp.dev).
- ### Added
- `ComposeMiddleware` for multi-method routes with automatic method selection
- Web framework adapters for Gin, Echo, and Chi
- JSON codecs for challenges and credentials
- Tempo charge proof support and hardened charge flow
- Cross-SDK challenge test vectors and example parity tests
- Integration tests against a live Tempo container in CI
- ### Changed
- Streamlined Tempo charge setup with config-based constructors
- Simplified public API and tooling
- Hardened payment challenge verification
- Bumped go-ethereum from 1.17.0 to 1.17.2 (by @BrendanRyan, [#18](https://github.com/tempoxyz/mpp-go/pull/18))

All notable changes to this project will be documented in this file.

## [Unreleased]

### Added

- `ComposeMiddleware` for multi-method routes with automatic method selection
- Go web framework adapters for Gin, Echo, and Chi
- JSON codecs for challenges and credentials
- Tempo charge proof support and hardened charge flow
- Cross-SDK challenge test vectors and example parity tests
- Integration tests against live Tempo container in CI

### Changed

- Streamlined Tempo charge setup with config-based constructors
- Simplified public API and tooling
- Hardened payment challenge verification
- Bumped go-ethereum from 1.17.0 to 1.17.2

### Fixed

- Tempo charge transactions signed with a Keychain (0x04) envelope are now
  accepted instead of being uniformly rejected as `transaction signature is
  invalid`. The verifier extracts the embedded root account via the upstream
  `keychain.VerifyAccessKeySignature` helper and treats it as the authorising
  sender; access-key authorisation is enforced on-chain at RPC submission, so
  counterfactual smart accounts (whose access key is registered as part of
  first-tx execution) work too.
  The legacy Ethereum `{27, 28}` YParity encoding emitted by tempo-cli ≤ 1.6
  is normalised in a copy of the envelope before verification — the original
  `tx.Signature.Raw` bytes are passed through to `SendRawTransaction`
  unchanged.
- `transactionMatches` no longer rejects payments where the smart-account
  sender authorises a session key via `tx.KeyAuthorization`. The field scopes
  *which* key may execute the tx, not what gets paid; payment correctness is
  established by walking `tx.Calls` regardless. Tempo-cli AA wallets always
  populate this field, so the prior reject made every keychain-signed
  payment fail before signature verification could even run.

## [0.1.0] - 2025-03-18

- Initial release
