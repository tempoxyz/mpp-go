---
github.com/tempoxyz/mpp-go: minor
---

Remove the deprecated APIs that the canonical core, client, Tempo and server APIs replaced. Wire behavior is unchanged. Migrate as follows:

| Package | Removed | Use instead |
| --- | --- | --- |
| `mpp` | `FromAuthenticate` | `ParseChallenge` |
| `mpp` | `FormatAuthenticate`, `FormatAuthenticateStrict` | `FormatChallenge`, which writes `c.Realm` and returns an error |
| `mpp` | `(*Challenge).Verify(secret, realm)` | `(*Challenge).VerifyID(secret)` plus a `c.Realm == realm` check |
| `mpp` | `FromAuthorization` | `ParseCredential` |
| `mpp` | `FormatAuthorization` | `FormatCredential` |
| `mpp` | `FromPaymentReceipt` | `ParseReceipt` |
| `mpp` | `FormatReceipt`, `FormatPaymentReceipt`, `(*Receipt).ToPaymentReceipt` | `FormatReceiptWithError` |
| `mpp` | `WithExtra` | `WithExtensions` (top-level receipt fields, as mppx and mpp-rs emit) |
| `mpp` | `Expires.Seconds/Minutes/Hours/Days/Weeks(n)` | `ExpiresIn(n * time.Minute)` and so on |
| `mpp` | `BodyDigest.Compute`, `BodyDigest.Verify` | `ComputeBodyDigest`, `VerifyBodyDigest` with the raw body bytes |
| `mpp` | `ParseUnits` | `ParseAmount` (`*big.Int`, no int64 overflow) |
| `mpp` | `TransformUnits` | convert amounts with `ParseAmount` when building the request |
| `client` | `NewTransport`, `TransportOption` | `NewHTTPTransport(methods, inner, opts...)` (returns configuration errors) or `NewHTTPClient` |
| `client` | `WithTransportOnChallenge` | `WithOnChallenge` |
| `client` | `WithTransportPaymentPreferences` | `WithPaymentPreferences` |
| `client` | `WithTransportTrustedProofRealm`, `WithTrustedTempoProofRealm` | the Tempo charge client's `Config.TrustedProofRealms` |
| `mcp/client` | `Config.TrustedTempoProofRealms` | a `TrustedProofRealm` without an `Origin` in the Tempo charge client's `Config.TrustedProofRealms`; its `Realm` must match the challenge realm exactly, including case (the removed field compared case-insensitively) |
| `server` | `WriteChallenge`, `WriteChallenges` | `WritePaymentResponse(w, nil, challenges...)` |
| `server` | `WritePaymentErrorWithChallenge`, `WritePaymentErrorWithChallenges` | `WritePaymentResponse(w, err, challenges...)` |
| `server` | `WritePaymentError` | `WritePaymentResponse(w, err)` |
| `server` | `ComposeCharge` | `Compose` (same arguments) |
| `server/fiber` | `WriteChallenge`, `WriteChallenges`, `WritePaymentErrorWithChallenge`, `WritePaymentErrorWithChallenges`, `WritePaymentError` | `WritePaymentResponse(c, err, challenges...)` |
| `tempo` | `DefaultRPCURLForChain` | `RPCURLForChain`, which reports unknown chain IDs |
| `tempo` | `MainnetUSDCAddress` | `USDCEAddress` |
| `tempo` | `FeePayerWindow` | `SponsoredValidBeforeWindow` |
| `tempo/server` | `NewMethod`, `MethodConfig` | `New(Config{...})`; share one verifier with `Config.Intent` |
| `tempo/server` | `Config.FeePayerPolicies`, `IntentConfig.FeePayerPolicies` | `AllowedFeeTokens` (in the order to try) and `FeePayerPolicy` |
| `tempo/server` | `Config.FeePayerPrivateKeyEnv`, `IntentConfig.FeePayerPrivateKeyEnv` | `FeePayerPrivateKey: os.Getenv("NAME")` |

The realm written by the removed realm-taking writers is now always the Challenge's own `Realm`, which its ID binds. Without the removed realm pins, a zero-amount `tempo/charge` proof is paid only when the Tempo method's `OriginValidator` (its `Config.TrustedProofRealms`) accepts it, or, for methods without one, when the request origin owns the realm.

The deprecated APIs this release still kept are removed as well; see `remove-remaining-deprecated-apis.md`.
