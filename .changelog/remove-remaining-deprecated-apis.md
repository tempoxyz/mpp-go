---
github.com/tempoxyz/mpp-go: minor
---

Remove the last deprecated APIs, which were kept for the mpp-tools conformance adapter and the READMEs. Migrate as follows:

| Package | Removed | Use instead |
| --- | --- | --- |
| `mpp` | `NewChallenge` | `NewChallengeWithError`, which returns the error `NewChallenge` panicked with |
| `mpp` | `GenerateChallengeID` | `GenerateChallengeIDWithError` |
| `mpp` | `(*Challenge).ToAuthenticate(realm)`, `(*Challenge).ToAuthenticateStrict(realm)` | `FormatChallenge(c)`, which writes `c.Realm` and returns an error |
| `mpp` | `(*Credential).ToAuthorization()`, `FormatAuthorizationStrict` | `FormatCredential(c)` |
| `mpp` | `ParsePaymentReceipt` | `ParseReceipt` |
| `mpp` | `Receipt.Extra` | `Receipt.Extensions["extra"]` (set it with `WithExtensions`) |
| `server` | `ChargeParams.EnableChallengeNonce` | `server.New(..., server.WithChallengeNonce(true))`; `VerifyParams.EnableChallengeNonce` is unchanged |
| `tempo` | `ChargeSponsoredChallengeStoreKey` | `ChargeSponsoredTransactionStoreKey` (verification still rejects Challenges reserved under the legacy key) |
| `tempo/server` | `MethodFromConfig` | `New` (same `Config`) |
| `tempo/server` | `Config.Currency` | `Config.Currencies: []string{currency}` |

A top-level `"extra"` receipt field is now an ordinary method-defined extension: `ParseReceipt` keeps it in `Receipt.Extensions["extra"]` (any JSON value, not only an object) and `FormatReceiptWithError` writes it back at the top level, so the wire is unchanged. An empty `"extra"` object is still dropped.

`Config.Currencies: []string{currency}` requires a 0x-prefixed address; the removed `Currency` field accepted any string verbatim.
