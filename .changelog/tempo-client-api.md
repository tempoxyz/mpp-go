---
github.com/tempoxyz/mpp-go: patch
---

Simplify the Tempo and client APIs, additively.

- `tempo`: `ChainIDMainnet` and `ChainIDModerato`, `USDCEAddress` (`MainnetUSDCAddress` is deprecated) and `NewRPCClientWithOptions`. `DefaultRPCURLForChain`, which maps unknown chains to mainnet, is deprecated in favor of `RPCURLForChain`.
- `tempo/server`: `New(Config)` is the one constructor; `MethodFromConfig` and `NewMethod` are deprecated. `AllowedFeeTokens` and a single `FeePayerPolicy` (as in mppx) replace the deprecated `FeePayerPolicies` map, and `FeePayerPrivateKeyEnv` is deprecated. Setting both `Config.Intent` and `Relay` is now an error rather than silently dropping the Intent. RPC clients built from URLs are reused instead of rebuilt per credential.
- `tempo/client`: a `PaymentPolicy` type with `MaxAmount`, `MaxAtomicAmount`, `AllowChainIDs` and `AllPolicies`; `Config.TrustedProofRealms`; the method implements `client.OriginValidator`. The RPC client is built once.
- `client`: `NewHTTPClient` returns a standard `*http.Client` with same-origin redirects, and `NewHTTPTransport` reports configuration errors at construction; both take `Option`. New `WithMaxRetries`, `WithOrderChallenges`, `OrderChallenges`, `OriginValidator` and `ErrPaymentDeclined`, which an `OnChallenge` decline now matches. `NewTransport`, `TransportOption`, the `WithTransport*` options and `WithTrustedTempoProofRealm` are deprecated.
- `mcp/client`: `Middleware(Config)` installs payment handling without `Wrap`'s registry; `ErrPaymentDeclined` is `client.ErrPaymentDeclined`; `TrustedTempoProofRealms` is deprecated in favor of the Tempo method's `TrustedProofRealms`.
