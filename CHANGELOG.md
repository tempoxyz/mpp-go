# Changelog

## `github.com/tempoxyz/mpp-go@0.4.0`

### Minor Changes

- Require client payment methods to declare their supported intent and match challenges by method and intent. (by @BrendanRyan, [#138](https://github.com/tempoxyz/mpp-go/pull/138))
- Advertise and rank client payment capabilities with `Accept-Payment`, and support multiple challenge-aware handlers for the same method and intent. (by @BrendanRyan, [#139](https://github.com/tempoxyz/mpp-go/pull/139))
- Offer ordered Tempo charge currencies from the server. By default the Tempo server method now issues one charge Challenge per accepted currency: OUSD then USDC.e on mainnet, and OUSD then pathUSD on Moderato. Unknown chains keep their previous single default. Added `MethodConfig.Currencies` and `Config.Currencies` for an ordered list that replaces the defaults, and deprecated the singular `Currency`, which still restricts acceptance to that one token. Setting both fields, passing an empty list, or passing an invalid address is rejected. Added `tempo.OUSDAddress`, `tempo.PathUSDAddress`, and `tempo.DefaultCurrenciesForChain`; `tempo.DefaultCurrencyForChain` and the default fee-payer policy values are unchanged.
- Added `server.ChargeOffersBuilder`, `ChargeResult.Challenges`, `server.WriteChallenges`, and `server.WritePaymentErrorWithChallenges` (plus Fiber equivalents). `Mpp.Charge`, `ChargeMiddleware`, `ComposeMiddleware`, and the Echo, Gin, and Fiber adapters advertise every offer in order and verify a Credential against the offer it echoes. A per-request `ChargeParams.Currency` still issues a single offer.
- Decoupled the local fee payer's fee token from the charge currency, matching mppx. A local fee payer now pays gas in the new `IntentConfig.FeeToken` / `Config.FeeToken` when set, otherwise in the first allowed fee token it holds a balance of (falling back to the first allowed token), and applies that token's fee-payer policy. Allowed fee tokens are the configured `FeePayerPolicies` tokens, or by default pathUSD plus USDC.e on mainnet and pathUSD on Moderato, so sponsored OUSD charges pay gas in pathUSD or USDC.e. Client-supplied fee tokens must be allowed. A local fee payer on Moderato therefore no longer pays gas in AlphaUSD by default; list it in `FeePayerPolicies` to keep it. Remote fee payers (`FeePayerURL`) select an allowed gas token independently of the charge currency; the returned token and its fee policy are validated before broadcast. Default unconfigured servers to mainnet offers and require a local signer for an explicit `FeeToken`. (by @DerekCofausper, [#178](https://github.com/tempoxyz/mpp-go/pull/178))
- Require an explicit payment policy before signing or broadcasting nonzero client payments. Provide the verified chain ID to the policy and document bounded payment approval. (by @BrendanRyan, [#191](https://github.com/tempoxyz/mpp-go/pull/191))
- Preserve method-defined top-level extension fields (such as `originTxHash`) when parsing and formatting payment receipts. draft-ietf-httpauth-payment §5.3 allows payment methods to define additional receipt fields, and the canonical mppx schema keeps them across a parse/format round trip; previously any top-level field outside the base set was silently dropped. Such fields are now retained on the new `Receipt.Extensions` map and re-emitted at the top level. (by @Erhnysr, [#115](https://github.com/tempoxyz/mpp-go/pull/115))
- Reject server secret keys shorter than 32 bytes at construction instead of accepting forgeable HMAC-bound challenge IDs. `server.New` now returns `(*Mpp, error)`. (by @Erhnysr, [#112](https://github.com/tempoxyz/mpp-go/pull/112))
- Remove custom primary Tempo charge memos from server configuration and request parameters. Clients always generate attribution memos bound to the challenge and realm, and servers require that binding for both calldata and receipt validation. Split-specific memos remain supported. (by @DerekCofausper, [#162](https://github.com/tempoxyz/mpp-go/pull/162))
- Require explicit replay storage when constructing local payment verifiers. Reject missing and typed-nil stores before issuing payable challenges; replicated servers must share persistent replay keys. (by @BrendanRyan, [#190](https://github.com/tempoxyz/mpp-go/pull/190))
- Require the payment method when constructing successful receipts so they always contain this required base field. (by @BrendanRyan, [#150](https://github.com/tempoxyz/mpp-go/pull/150))
- Add split credential validation and broadcast lifecycle hooks, with Tempo API relay configuration for server-side charges. (by @BrendanRyan, [#110](https://github.com/tempoxyz/mpp-go/pull/110))

### Patch Changes

- Retry supported payment challenges up to three times and emit initial payment challenges without an error body. (by @BrendanRyan, [#140](https://github.com/tempoxyz/mpp-go/pull/140))
- Update the changelog release action to use Tempo's approved vendored pull request action. (by @DerekCofausper, [#206](https://github.com/tempoxyz/mpp-go/pull/206))
- Decode challenge and credential JSON with `json.Number` so integers above 2^53 keep their digits. Re-encoding a request through `float64` changed the bytes the challenge ID is an HMAC over, making challenges the server itself issued fail verification. (by @monem, [#141](https://github.com/tempoxyz/mpp-go/pull/141))
- Reject automatic zero-amount Tempo proof challenges when the realm differs from the HTTP server hostname. (by @DerekCofausper, [#211](https://github.com/tempoxyz/mpp-go/pull/211))
- Limit payment middleware request bodies to 4 MiB and return HTTP 413 for larger fixed-length or chunked bodies. Fiber applications should configure their transport BodyLimit at or below this bound because Fiber buffers before middleware. Configure HTTP read timeouts as well.
- Fiber payment middleware rejects encoded bodies with HTTP 415 before decompression. Oversized bodies use a generic HTTP 413 problem response. (by @BrendanRyan, [#194](https://github.com/tempoxyz/mpp-go/pull/194))
- Rejected authorization headers exceeding 16 KiB before extracting credentials. (by @BrendanRyan, [#197](https://github.com/tempoxyz/mpp-go/pull/197))
- Buffered GitHub Actions updates to satisfy the scanner's seven-day minimum age. (by @DerekCofausper, [44a724a](https://github.com/tempoxyz/mpp-go/commit/44a724a))
- Update Go to 1.26.10. (by @DerekCofausper, [#205](https://github.com/tempoxyz/mpp-go/pull/205))
- Use canonical problem URIs for invalid-payload and bad-request, and return 402 with a retry challenge for invalid credential payloads. Preserve wrapped payment error statuses and problem details during verification and HTTP response serialization, including Fiber.
- Regenerate rejected client credentials on invalid-payload responses even when a fixed expiry keeps the challenge ID unchanged. (by @BrendanRyan, [#177](https://github.com/tempoxyz/mpp-go/pull/177))
- Build all example packages in CI and via `make build_examples` so framework and relay examples cannot compile-drift unnoticed. (by @Crazy, [#145](https://github.com/tempoxyz/mpp-go/pull/145))
- Fix the client challenge filter so a challenge whose `expires` cannot be parsed is skipped instead of treated as valid, preventing the client from paying a challenge the server is guaranteed to reject. (by @WinterRong, [#88](https://github.com/tempoxyz/mpp-go/pull/88))
- Check that a request can be sent again before creating a payment credential for it. A request whose body cannot be replayed (no `GetBody`, or `GetBody` failing) now returns an error without calling the payment method, instead of creating a credential the retry could never deliver. (by @Erhnysr, [#188](https://github.com/tempoxyz/mpp-go/pull/188))
- Return fresh WWW-Authenticate challenges from ComposeMiddleware when a Payment credential is malformed, matching the retry behavior of ChargeMiddleware and VerifyOrChallenge. (by @Crazy, [#132](https://github.com/tempoxyz/mpp-go/pull/132))
- Update Go dependencies in the weekly Dependabot batch. (by @dependabot[bot], [#109](https://github.com/tempoxyz/mpp-go/pull/109))
- Update Go dependencies in the weekly Dependabot batch. (by @dependabot[bot], [#109](https://github.com/tempoxyz/mpp-go/pull/109))
- Update Go dependencies in the weekly Dependabot batch. (by @dependabot[bot], [#109](https://github.com/tempoxyz/mpp-go/pull/109))
- Batch routine dependency updates weekly and automatically merge patch and minor
- updates after all pull request checks pass. (by @BrendanRyan, [#104](https://github.com/tempoxyz/mpp-go/pull/104))
- Raised the Authorization and Payment credential size limit from 16 KiB to 64 KiB, matching mppx. A credential echoes its challenge, so credentials for challenges with large `request` or `opaque` values were rejected even though mppx accepts them. Oversized values are still rejected before scheme extraction and decoding. Challenge and receipt limits are unchanged.
- Upgrade servers before clients. An older mpp-go server rejects credentials over 16 KiB, and a Tempo hash-credential client broadcasts its payment before the server sees the credential, so an upgraded client paying an older server can pay without being served. (by @BrendanRyan, [#212](https://github.com/tempoxyz/mpp-go/pull/212))
- Accept Tempo CLI fee-payer credentials signed by keychain access keys. (by @BrendanRyan, [#136](https://github.com/tempoxyz/mpp-go/pull/136))
- Return a fresh `WWW-Authenticate: Payment` challenge when payment credential verification fails, allowing clients to retry with the current challenge. (by @PranjalPaliwal, [#74](https://github.com/tempoxyz/mpp-go/pull/74))
- Allow push-mode hash credentials for Tempo charge challenges with an explicit memo, matching the application-provided correlation value exactly during receipt verification. (by @BrendanRyan, [#142](https://github.com/tempoxyz/mpp-go/pull/142))
- Decode a bare `0x` (and empty) hex quantity as zero in `ParseHexUint64`, matching `ParseHexBigInt` so both JSON-RPC integer decoders agree on zero-value forms returned by lenient nodes. (by @Salad, [#91](https://github.com/tempoxyz/mpp-go/pull/91))
- Use RFC 8785 JSON Canonicalization Scheme (JCS) for challenge-bound request and opaque values, keeping challenge IDs reproducible across SDKs for Unicode data and JCS number formatting. (by @BrendanRyan, [#144](https://github.com/tempoxyz/mpp-go/pull/144))
- Use the changelogs action's locked install so release builds honor its committed dependency versions. (by @DerekCofausper, [#207](https://github.com/tempoxyz/mpp-go/pull/207))
- Accept legacy challenge descriptions containing unescaped quotes. (by @BrendanRyan, [#105](https://github.com/tempoxyz/mpp-go/pull/105))
- Reject a negative `decimals` in `ParseUnits` (and thus `TransformUnits`) instead of panicking on the fractional-part slice. (by @Alex, [#87](https://github.com/tempoxyz/mpp-go/pull/87))
- Add `mpp.FormatAuthorizationStrict` and preflight Tempo hash credential size before submitting a transaction. Fiber charge middleware now reads streamed request bodies within the 4 MiB limit and closes connections after oversized or failed reads.
- When `EnableChallengeNonce` is enabled, server-issued challenges include a random, HMAC-bound `_mpp_nonce` in opaque metadata, including when an explicit expiry is reused. This metadata key is reserved. Composed offer selection still compares application metadata. Previously issued challenges remain valid until their existing expiry; low-level `mpp.NewChallenge` remains deterministic.
- Unique challenges prevent unrelated callers from obtaining identical challenges, but do not authenticate the redeemer of a stolen complete credential. Applications requiring caller identity must bind trusted session/account context through `ChargeParams.Meta` and enforce their own authorization. Keep payment credentials confidential and use a shared atomic replay store across replicas.
- Nonce issuance is opt-in: first roll out the default `EnableChallengeNonce: false` to every replica, then set `ChargeParams.EnableChallengeNonce: true` (or `VerifyParams.EnableChallengeNonce`) after every verifier is upgraded. The default preserves legacy challenge construction but retains shared-challenge collision risk until operators enable issuance. Old verifiers reject nonced hash payments after submission, so enabling before all replicas are ready is unsafe. Version-sticky retries are an alternative. Transient verification failures reissue the authenticated challenge, preserving client credential reuse; invalid-payload failures still issue fresh challenges. Strict formatting propagates serialization errors. (by @BrendanRyan, [#210](https://github.com/tempoxyz/mpp-go/pull/210))
- Refresh shared GitHub Actions pins to the latest verified release. (by @ShanedaSilva, [#174](https://github.com/tempoxyz/mpp-go/pull/174))
- Update shared GitHub Actions and workflows to the 2026-09-25 release. (by @ShanedaSilva, [#173](https://github.com/tempoxyz/mpp-go/pull/173))
- Reject reused non-sponsored transaction credentials before receipt verification, including concurrent submissions.
- A transaction reservation remains consumed if receipt lookup fails after submission; retrying that credential no longer authorizes fulfillment. (by @BrendanRyan, [#156](https://github.com/tempoxyz/mpp-go/pull/156))
- Send relay broadcast `Idempotency-Key` headers under the canonical `mpp_` namespace instead of `mppx_`. The key is otherwise byte-identical (Keccak-256 of the signed transaction, or SHA-256 of the canonical relay input), so the previous prefix prevented the relay from deduplicating equivalent canonical and Go submissions of the same transaction or credential retry. Matches `src/tempo/server/Relay.ts` in the canonical mppx implementation. (by @Erhnysr, [#114](https://github.com/tempoxyz/mpp-go/pull/114))
- Reject payment receipts that omit the `method` or `timestamp` field when parsing. Both are required base receipt fields per draft-ietf-httpauth-payment §5.3 and the canonical mppx schema; previously an incomplete receipt parsed successfully with an empty method and zero timestamp. (by @Erhnysr, [#113](https://github.com/tempoxyz/mpp-go/pull/113))
- Run the required GitHub Actions scan on every pull request to prevent missing checks from blocking merges. (by @BrendanRyan, [#163](https://github.com/tempoxyz/mpp-go/pull/163))
- Add a `requiresAuth` server option that advertises `header="Payment-Authorization"` so Payment credentials can coexist with ordinary `Authorization`. (by @RyanAubrey, [#143](https://github.com/tempoxyz/mpp-go/pull/143))
- Harden CI jobs and add dependency scanning for pull requests. (by @ShanedaSilva, [#161](https://github.com/tempoxyz/mpp-go/pull/161))
- Reject ambiguous challenge ID fields and omit payment receipts from failed downstream responses. (by @BrendanRyan, [#153](https://github.com/tempoxyz/mpp-go/pull/153))
- Require sponsored transactions to retain at least 15 seconds of validity during validation, before co-signing, and before broadcasting. (by @BrendanRyan, [#193](https://github.com/tempoxyz/mpp-go/pull/193))
- Reserve sponsored charge transactions by the hash of the payer-signed envelope instead of the challenge ID, so distinct payers answering the same challenge are no longer rejected as replays. `tempo.ChargeSponsoredChallengeStoreKey` is replaced by `tempo.ChargeSponsoredTransactionStoreKey`.
- Replay keys now use the verified payer and canonical sender signing payload. Existing challenge reservations remain honored. Quiesce old sponsor replicas before enabling the new version; mixed-version concurrent admission is not supported. Retain the shared replay store across the upgrade. (by @BrendanRyan, [#201](https://github.com/tempoxyz/mpp-go/pull/201))
- Reject malformed `WWW-Authenticate: Payment` auth-param lists instead of silently accepting trailing bare parameters or missing separators. (by @MarkHarrison, [#92](https://github.com/tempoxyz/mpp-go/pull/92))
- Strip CR and LF characters in the default `FormatAuthenticate` path so a `Challenge` field containing `\r\n` (e.g. `Description`, `Realm`) can no longer split the `WWW-Authenticate` header and inject a response. `FormatAuthenticateStrict` continues to reject such values with an error. (by @Tleao, [#86](https://github.com/tempoxyz/mpp-go/pull/86))
- Use the bootstrapped Tempo localnet image for reproducible integration tests. (by @BrendanRyan, [#129](https://github.com/tempoxyz/mpp-go/pull/129))
- Expose public split-intent interfaces and support non-mutating validation with the built-in Tempo charge intent. (by @BrendanRyan, [#137](https://github.com/tempoxyz/mpp-go/pull/137))
- Refuse standalone `Transport` auto-pay after a redirect. A `Transport` used with a bare `http.Client` (no `CheckRedirect`) had none of `Client.Do`'s cross-origin redirect protection, so a redirect to an attacker origin could be auto-paid. The Transport now fails closed on any redirect-produced request when no trusted origin is pinned in the context. (by @Mattew, [#84](https://github.com/tempoxyz/mpp-go/pull/84))
- Preserve Unicode in challenge auth-params by encoding non-Latin-1 text as UTF-16 escapes and decoding those escapes while parsing. (by @BrendanRyan, [#168](https://github.com/tempoxyz/mpp-go/pull/168))
- Update shared GitHub Actions workflow and secure-runner pins to the latest reviewed revision. (by @DerekCofausper, [#164](https://github.com/tempoxyz/mpp-go/pull/164))
- Update shared GitHub Actions and reusable workflow pins. (by @ShanedaSilva, [#170](https://github.com/tempoxyz/mpp-go/pull/170))
- Update release automation to reconcile revoked GitHub STS tokens with the service ledger. (by @ShanedaSilva, [#148](https://github.com/tempoxyz/mpp-go/pull/148))
- Reject non-hexadecimal 32-byte memos in `EncodeTransferWithMemo` instead of producing invalid Tempo calldata. (by @MarkHarrison, [#100](https://github.com/tempoxyz/mpp-go/pull/100))
- Run Cyclops warden reviews on same-repo PR pushes and keep following a PR after an authorized plain audit command, including drafts. (by @DerekCofausper, [#196](https://github.com/tempoxyz/mpp-go/pull/196))
- Require proof credentials for zero-amount sponsored requests, including alternate zero encodings and surrounding whitespace, before signing or broadcasting a transaction. (by @BrendanRyan, [#192](https://github.com/tempoxyz/mpp-go/pull/192))

## `github.com/tempoxyz/mpp-go@0.3.0`

### Minor Changes

- Add `Hint` field to `PaymentError` problem details. `PaymentRequired`, `MalformedCredential`, and `MethodUnsupported` errors now include a default hint pointing users to wallet documentation. (by @bensandler-stripe, [#73](https://github.com/tempoxyz/mpp-go/pull/73))

### Patch Changes

- Preserve `subscriptionId` when parsing and formatting payment receipts. (by @Osraka, [#81](https://github.com/tempoxyz/mpp-go/pull/81))
- Use the shared Tempo workflow to publish pull request audit events. (by @Mablr, [#82](https://github.com/tempoxyz/mpp-go/pull/82))
- Fix Tempo receipt verification so paired TransferWithMemo logs cannot reuse the same base Transfer log during deduplication. (by @BrendanRyan, [#77](https://github.com/tempoxyz/mpp-go/pull/77))

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
