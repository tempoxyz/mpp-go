---
github.com/tempoxyz/mpp-go: minor
---

`pkg/server` no longer imports `pkg/tempo`, so the core server, `pkg/mcp/jsonrpc` and the Gin, Echo and Fiber adapters no longer compile go-ethereum, gnark-crypto, go-eth-kzg, go-redis and tempo-go: `go list -deps ./pkg/server` now lists only `github.com/gowebpki/jcs` outside the standard library (13 modules before). `ChargeParams.Splits` and `ChargeParams.SupportedModes` use the new dependency-free `pkg/tempo/tempoparams` package, and `tempo.SplitParams`, `tempo.ChargeMode`, `tempo.ChargeModePull` and `tempo.ChargeModePush` are now aliases of its types and constants, so existing code compiles unchanged.

The change is source-compatible, but apidiff reports the moved types as incompatible (their package path changes, which `%T` and reflection show), so this is a minor bump.
