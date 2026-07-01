# Changelog

## `github.com/tempoxyz/mpp-go@0.2.0`

### Minor Changes

- Bind server charge challenges to request bodies and framework route scope. (by @figtracer, [#55](https://github.com/tempoxyz/mpp-go/pull/55))

### Patch Changes

- Convert Go tests to use testify assertions. (by @BrendanRyan, [#48](https://github.com/tempoxyz/mpp-go/pull/48))
- Run the pull request changelog check with read-only permissions so forked PRs can pass after adding a changelog. (by @BrendanRyan, [#71](https://github.com/tempoxyz/mpp-go/pull/71))
- Reject credentials that omit `expires` when verifying challenges with the default expiry policy. (by @EfeBaranDurmaz, [#39](https://github.com/tempoxyz/mpp-go/pull/39))
- Reject padded Tempo transfer calldata by requiring exact TIP-20 ABI lengths during shared calldata matching and server-side transaction validation. (by @BrendanRyan, [#70](https://github.com/tempoxyz/mpp-go/pull/70))
- Align middleware invalid-challenge test expectations with the core problem details status code. (by @BrendanRyan, [#72](https://github.com/tempoxyz/mpp-go/pull/72))
- Align spec-listed MPP Problem Details type URIs with the canonical `https://paymentauth.org/problems/` base URI, and return 402 for malformed credentials and invalid challenges. (by @PranjalPaliwal, [#67](https://github.com/tempoxyz/mpp-go/pull/67))
- Reject requests that include multiple `Authorization: Payment` credentials instead of silently selecting the first credential. (by @PranjalPaliwal, [#66](https://github.com/tempoxyz/mpp-go/pull/66))
- Mark paid server responses that include `Payment-Receipt` as `Cache-Control: private`. (by @EfeBaranDurmaz, [#37](https://github.com/tempoxyz/mpp-go/pull/37))
- Wallet-bind Tempo zero-amount proofs to close cross-account replay. The EIP-712 `Proof` message now leads with the payer `account` address (then `challengeId`, `realm`) and the MPP domain version is `"3"`. Verifiers rebuild the digest from the credential `source`, so client and server must both use v3. `ProofTypedDataHash` now takes an `account common.Address`; `ProofTypedData` exposes the typed data.
- Note: v3 is not yet interoperable with the mppx (TypeScript) SDK, which still uses v2 (`Proof = [challengeId, realm]`, no `account`). (by @stevencartavia, [#57](https://github.com/tempoxyz/mpp-go/pull/57))

## `github.com/tempoxyz/mpp-go@0.1.2`

### Patch Changes

- Reject CR/LF in `WWW-Authenticate` challenge formatting and built-in server challenge responses. (by @EmmaJamieson-Hoare, [#49](https://github.com/tempoxyz/mpp-go/pull/49))

## `github.com/tempoxyz/mpp-go@0.1.1`

### Patch Changes

- Harden Tempo charge verification by rejecting mismatched challenge chain IDs, requiring expiring challenge echoes, and reserving transaction hashes before non-sponsored broadcasts. (by @EmmaJamieson-Hoare, [#43](https://github.com/tempoxyz/mpp-go/pull/43))

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

## [0.1.0] - 2025-03-18

- Initial release
