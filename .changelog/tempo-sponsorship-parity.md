---
github.com/tempoxyz/mpp-go: minor
---

Aligned Tempo fee sponsorship with mppx:

- The fee payer is configured on the server only. `Config.FeePayerURL` (and the new `IntentConfig.FeePayerURL`) now names a hosted fee-payer service, and the new `HostedFeePayer{URL, Headers, HTTPClient}` adds request headers such as authorization. The server enforces its sponsor policy and simulates the sender's calls, asks the service to co-sign with `eth_fillTransaction` in mppx's request shape, keeps only the returned `feePayerSignature` and `feeToken` (checked against `AllowedFeeTokens` when set), recovers the sponsor from the signature, simulates the final transaction with it and broadcasts it itself. Challenges no longer carry `methodDetails.feePayerUrl`, servers ignore it in old challenges (it still parses), `eth_signRawTransaction` is no longer used, and setting the deprecated `ChargeParams.FeePayerURL` is an error.
- A Tempo method with a fee payer (local signer or hosted) sponsors every route unless the route sets the new `ChargeParams.DisableFeePayer` (mppx's `feePayer: false`), and never advertises sponsorship without one: `Config.FeePayer` or `ChargeParams.FeePayer` without a fee payer is an error, except with a Relay. Challenges issued by an older server for the same route without `feePayer` no longer match after upgrading; drain them first.
- Local co-signing accepts every Tempo sender signature (secp256k1, P256, WebAuthn, Keychain V1 and V2 with any inner signature, and multisig) and transactions carrying a keyAuthorization, and refuses any transaction with a non-empty authorizationList.
- Pre-broadcast simulations (`eth_call`) now carry the transaction's keyAuthorization as viem formats it.
- A Go client configured with `Config.CredentialType = tempo.CredentialTypeHash` now pays sponsored (`feePayer`) challenges in push mode, paying its own fees, as mppx does. Servers released before this change reject such a credential after the transfer is on chain, so upgrade servers before clients. The Transport no longer pays again after a 402 to a hash credential: it returns that response.
- The hosted fee payer's default HTTP client does not follow redirects, so its headers never reach another endpoint.
