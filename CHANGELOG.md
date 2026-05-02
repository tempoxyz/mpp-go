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

## [0.1.0] - 2025-03-18

- Initial release
