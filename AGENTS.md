# mpp-go

Go SDK for the Machine Payments Protocol (the "Payment" HTTP Authentication Scheme): client and server for the HTTP 402 flow, Tempo charge, MCP transport and adapters for net/http, Gin, Echo and Fiber. Module path: `github.com/tempoxyz/mpp-go`.

## Commands

```bash
make test                    # go test -race -shuffle=on -count=1 ./... (unit tests, as CI)
go build ./examples/...      # build every example (CI; also make build_examples)
make check                   # go mod verify, gofmt check, go vet, go test
make lint                    # golangci-lint v2 (.golangci.yml), new issues since origin/main only (as CI)
make lint-all                # golangci-lint, every issue including existing ones
make mod-check               # go mod tidy -diff && go mod verify
make vuln                    # govulncheck ./...
make fix                     # gofmt -s -w . && go mod tidy
make integration             # live integration tests in tests/ (Docker, or TEMPO_RPC_URL)
make fuzz FUZZTIME=60s       # every Fuzz* target under pkg/ (CI: nightly, fuzz.yml)
docker compose up -d --wait  # start the local Tempo localnet, wait until healthy
go run ./examples/charge-basic  # run one example against it
```

Run one test with `go test -run '^TestName$' ./pkg/server`, and one fuzz target with `go test -run='^$' -fuzz='^FuzzParseChallenge$' -fuzztime=30s ./pkg/mpp`. Unit tests need nothing external. The live tests in `tests/` (build tag `integration`) need Docker or a running node; see Testing.

CI (`.github/workflows/ci.yml`) runs unit tests on the minimum Go version in go.mod (`go` line, with `GOTOOLCHAIN=local`) and on the latest stable release, checks gofmt, `go vet`, `go mod tidy -diff` and `go mod verify`, and cross-compiles for darwin and windows. Install golangci-lint v2 to run `make lint`; CI reports only issues on lines a change touches, so don't reformat untouched code to fix existing findings. Other workflows: `smoke-cross-sdk.yml`, `govulncheck.yml` and `changelogs.yml`. mpp-tools' daily SDK Conformance Monitor runs the cross-SDK conformance suite against `main` with the `go` adapter and opens a `conformance-failure` issue in tempoxyz/mpp-tools when it fails; this repository's CI does not run it. Protocol changes add or update vectors there.

## Layout

```
pkg/
├── client/          # Generic HTTP 402 retry transport
├── mcp/             # MPP over MCP for the official MCP Go SDK (matches mppx/mcp)
│   ├── client/      # Payment-aware MCP client middleware
│   ├── jsonrpc/     # Charge/Compose for raw MCP JSON-RPC messages (mppx Transport.mcp())
│   └── server/      # Charge/Compose for MCP tool handlers
├── mpp/             # Protocol primitives (Challenge, Credential, Receipt, parsing, hmac.go)
├── server/          # Generic challenge-or-verify flow (flow.go); echo/, fiber/, gin/ adapters
├── internal/        # mpptest (test Challenges and headers), tempoproof (Tempo proof origin check)
└── tempo/           # Tempo blockchain support (MemoryStore, constants, RPC helpers)
    ├── client/      # Tempo charge client method
    ├── redisstore/  # Redis replay Store (own package: pkg/tempo does not link go-redis)
    ├── sqlstore/    # SQL (Postgres, SQLite) replay Store on database/sql (own package, no driver)
    ├── server/      # Tempo charge server method
    └── internal/    # rpccache (RPC clients cached per method)
internal/
├── localnet/        # WaitReady: wait until a Tempo node builds current blocks
└── serverevents/    # PaymentFailed reporting for transports that reject before Charge
examples/            # Runnable servers and clients (charge-basic, charge-mcp, gin, echo, fiber...); internal/devnet
tests/               # Nested module of live integration tests; tempotest/ wraps the Tempo localnet container
.changelog/          # One release note per PR
```

## Conventions

### Imports

Use bare package names: do not alias `pkg/server` as `mppserver` or `pkg/client` as `mppclient`. The Tempo-specific packages already use the `charge` alias, so there is no conflict:

```go
// Good
import (
    "github.com/tempoxyz/mpp-go/pkg/server"
    charge "github.com/tempoxyz/mpp-go/pkg/tempo/server"
)

payment, err := server.New(method, realm, secret)

// Bad
import (
    mppserver "github.com/tempoxyz/mpp-go/pkg/server"
)

payment, err := mppserver.New(method, realm, secret)
```

When a local variable would shadow the package name, use a short variable name (`c`, `srv`) instead of aliasing the import.

Exception: internal library code in `pkg/tempo/server`, `pkg/tempo/client`, `pkg/mcp/server` and `pkg/mcp/client` must use `mppserver`/`mppclient` aliases because the file's own package name (`server`/`client`) would conflict.

### API compatibility

An incompatible change to an exported API under `pkg/` needs a `.changelog/*.md` entry declaring `github.com/tempoxyz/mpp-go: minor` (or `major`). Before v1, a breaking change is a minor bump. Prefer a compatible change: add rather than remove or rename, and mark old APIs `// Deprecated:`. CI doesn't check this, so compare the API locally with `apidiff` when a change touches exported names:

```bash
go install golang.org/x/exp/cmd/apidiff@v0.0.0-20261014143641-f0251eb4aa3e
git worktree add --detach /tmp/mpp-go-base origin/main
(cd /tmp/mpp-go-base && apidiff -m -w /tmp/base.api github.com/tempoxyz/mpp-go)
apidiff -m -w /tmp/head.api github.com/tempoxyz/mpp-go
apidiff -m -incompatible /tmp/base.api /tmp/head.api   # lines under ./pkg/ are breaking
git worktree remove /tmp/mpp-go-base
```

### Deprecated APIs

No deprecated APIs remain: the ones the SDK improvement plan replaced were removed in one minor release (`.changelog/remove-deprecated-apis.md`) and the last ones, kept for the mpp-tools conformance adapter and the READMEs, in the next (`.changelog/remove-remaining-deprecated-apis.md`). Before removing an API marked `// Deprecated:`, check that the mpp-tools Go conformance adapter (`tempoxyz/mpp-tools/conformance/adapters/go`), which mpp-tools' daily conformance run builds against this SDK's `main`, and the READMEs no longer use it. `staticcheck -checks SA1019 ./...` reports nothing; tests build Challenges and headers with `pkg/internal/mpptest`.

### READMEs

Do not update README files (the root README.md or any example README) unless explicitly asked to. Document new APIs in Go doc comments, AGENTS.md and the changelog instead.

### Changelogs

Every PR adds a release note as a new Markdown file under `.changelog/` (singular); do not use `.changelogs/` or edit `CHANGELOG.md` directly. Use the full Go module path in frontmatter:

```markdown
---
github.com/tempoxyz/mpp-go: patch
---

Description of the change.
```

## Architecture

mpp-go provides abstractions for the complete HTTP 402 payment flow, client and server, in two layers.

### Core abstractions

1. **`server.Mpp`**: top-level payment handler. Groups a `Method` with realm and secret, handles the HTTP 402 flow (Challenge/Credential parsing, header serialization, verification).
2. **`server.Method`**: a payment method definition. Each method provides intents (for example `charge`) and builds Challenge requests with method-specific parameters.

```
┌────────────────────┐       ┌────────────────┐
│    server.Mpp      │ 1   1 │ server.Method  │
│    (handler)       ├───────┤  (definition)  │
└────────────────────┘ has   └────────────────┘
│ payment            │       │ tempo/charge   │
└────────────────────┘       └────────────────┘
```

```
Client (client.Client)                              Server (server.Mpp)
   │                                                   │
   │  (1) GET /resource                                │
   ├──────────────────────────────────────────────────>│
   │                                                   │
   │             (2) server.Charge(request, { ... })   │
   │                   402 + WWW-Authenticate: Payment │
   │<──────────────────────────────────────────────────┤
   │                                                   │
   │  (3) method.CreateCredential(challenge)           │
   │                                                   │
   │  (4) GET /resource                                │
   │      Authorization: Payment <credential>          │
   ├──────────────────────────────────────────────────>│
   │                                                   │
   │               (5) server.Charge(request)          │
   │                                                   │
   │               (6) 200 OK                          │
   │                   Payment-Receipt: <receipt>      │
   │<──────────────────────────────────────────────────┤
```

### Primitives

Low-level data structures in `pkg/mpp` that compose into the core abstractions:

- **`Challenge`**: server-issued payment request (`WWW-Authenticate`). Contains `ID`, `Realm`, `Method`, `Intent`, `Request`, and optional `Expires`/`Digest`.
- **`Credential`**: client-submitted payment proof (`Authorization`). Contains the `Challenge` echo, `Payload` (method-specific proof), and optional `Source` (payer identity).
- **`Receipt`**: server-issued settlement confirmation (`Payment-Receipt`). Contains `Status`, `Method`, `Timestamp`, and `Reference`.

Each type has one canonical Parse/Format pair:

| Type | Parse | Format |
| --- | --- | --- |
| `Challenge` | `ParseChallenge` | `FormatChallenge(c) (string, error)`, writes `c.Realm` |
| `Credential` | `ParseCredential` | `FormatCredential(c) (string, error)` |
| `Receipt` | `ParseReceipt` | `FormatReceiptWithError(r) (string, error)` |

- Create Challenges with `NewChallengeWithError` and check an echoed ID with `(*Challenge).VerifyID(secret)`, which binds `c.Realm`; also compare `c.Realm` with the realm you expect, which VerifyID does not. The binding itself is in `pkg/mpp/hmac.go` (`generateChallengeID`). Tests build fixed Challenges with `mpptest.NewChallenge`, which panics on an error.
- The Format functions reject what their Parse counterpart rejects (missing or invalid fields, a receipt status other than "success", oversized headers), so the SDK never emits a header its own parser refuses.
- Responses: `ChallengesFromResponse` and `ReceiptFromResponse`. Expiry: `ExpiresIn`, `ParseExpires`, `CheckExpires`. Body digests: `ComputeBodyDigest`, `VerifyBodyDigest`. Amounts: `ParseAmount` (`*big.Int`). Intents: `IntentCharge`, `IntentSession`.
- Match problem types with `errors.Is(err, mpp.ErrorTypeX)`: `PaymentError` implements `Is` for `ErrorType` targets.
- Servers write every 402 and problem response with `server.WritePaymentResponse(w, err, challenges...)` (Fiber: `fiberadapter.WritePaymentResponse(c, err, challenges...)`); it is the only writer.

### Server charge flow

- One unexported, framework-neutral flow (`pkg/server/flow.go`) runs every charge: `ChargeMiddleware` is `ComposeMiddleware` with one offer, and the Gin, Echo and Fiber adapters call `(*Mpp).ChargeRequest` or `ComposeRequest` with their route scope, then write the result with `(*ChargeResult).ServeHTTP` (Fiber through a fasthttp-backed `http.ResponseWriter`). Only serving a verified payment (receipt deferral, context values) is framework-specific.
- HTTP handlers: `res, err := payment.ChargeRequest(r, params)`, then `res.ServeHTTP(w, r)` for every result before writing anything: it writes the 402 or problem response when `err != nil || res.IsChallenge()`, and otherwise sets `Payment-Receipt` and `Cache-Control: private` for the resource the handler then writes. Other transports (MCP) use `Charge(ctx, params)` with the Credential in `params`, or `server.Compose(ctx, authorization, offers...)` (`Offer` is an alias of `ComposeConfig`).
- `server.New` defaults an empty secret to `MPP_SECRET_KEY`. An empty realm stays empty: the realm is bound into every Challenge, so defaulting it would make upgraded and older replicas reject each other's Challenges; callers who want the environment's realm pass `server.DetectRealm()`.
- Server-wide options: `WithRequiresAuth`, `WithChallengeNonce` and `WithEvents` (mppx's onChallengeCreated/onPaymentSuccess/onPaymentFailed). PaymentFailed fires once per distinct `Mpp` for every submitted Credential that is rejected, including one that cannot be parsed (nil Credential), whether it arrives through `Charge`, `ChargeRequest`, the middlewares, `Compose` or MCP `_meta`. Transports in this module report a Credential they reject before `Charge` through `internal/serverevents`. ChallengeCreated fires only for Challenges a response returns, once the whole set is issued and serializes (a composed flow reports them itself, through `chargeWithEvents(..., false)`). Hooks run under `safeCall`: a panic is recovered and ignored.
- Issued Challenges carry a random `_mpp_nonce` (16 bytes, base64url without padding) in their opaque metadata by default, the key every MPP SDK uses; `WithChallengeNonce(false)` (or `VerifyParams.DisableChallengeNonce`) turns it off. Verification also accepts the legacy `_mppx_nonce` (issued by mppx and releases 0.7.0 to 0.9.0) and no nonce, ignores both keys when matching opaque metadata against the route's, and both stay reserved in `ChargeParams.Meta`.
- `VerifyOrChallenge` checks a Credential in the core specification's order: malformed-credential, then binding, realm, route, opaque metadata and body digest (invalid-challenge), then expiry (payment-expired), then the method, which reports replays (`mpp.ErrCredentialReplayed`: 402 invalid-challenge with a fresh Challenge) before verifying the payment. It refuses a secret shorter than 32 bytes, as `server.New` does.
- HTTP flows read every `Authorization` and `Payment-Authorization` value: more than one Payment credential is `400` bad-request with no Challenge; a bare `Payment` scheme counts as absent; a credential over 65536 octets is malformed-credential.
- A composed HTTP flow keeps the header a Credential arrived in: an offer created `WithRequiresAuth` accepts it only from `Payment-Authorization`, any other offer only from `Authorization`. A Credential in a field its Challenge did not select (or no offer reads) is ignored, as if absent: `402` with every offer's Challenges and no problem type, as the core specification requires.
- A Credential for an offered method but an intent or request no offer matches is dispatched to that offer's `Charge`, which answers `402` with retry offers; only a method no offer has gets `400` method-unsupported.
- The route scope is mppx's: a `"GET /route"` string (upper-case method, space, route template, else the request path; never the query) bound into the HMAC-bound opaque metadata as `_mppx_scope` (`ChargeParams.MppxScope`, `ScopeFromHTTPRequest`). Verification compares it with the rest of the application metadata, so a Credential is accepted on any resource of the route it was issued for and rejected on another route. Adapters bind the request's scope only when neither `MppxScope` nor `Meta["_mppx_scope"]` is set (`ChargeParams.WithDefaultScope`); different values in both are a server error, as mppx's `Scope.merge`. Releases before this one bound a `{route, resource, query}` map in the request instead and reject these Challenges.

### Clients and Tempo methods

- HTTP clients: `client.NewHTTPClient(methods, opts...)` returns a standard `*http.Client` (same-origin redirects only, config errors at construction); `client.New` and `NewHTTPTransport` take the same `Option`s. Options include `WithMaxRetries` and `WithOrderChallenges`; a first 402 with no payable Challenge fails with `*client.NoMethodFoundError` (`client.ErrNoMethodFound`, mppx's message), while a 402 after a paid retry is returned; an `OnChallenge` hook that returns `client.ErrPaymentDeclined` before `createCredential` declines without paying and RoundTrip returns the 402 with a nil error; any other hook error is returned and matches `client.ErrPaymentDeclined` (also `mcpclient.ErrPaymentDeclined`), but an `OnChallenge` error returned after `createCredential` succeeded does not; a decline after `createCredential` failed is an error, not a 402. A 402 invalid-challenge answering a sent Credential (caller-set or created) is final: the HTTP Transport returns it, and the MCP client middleware returns a `*mcpclient.ReconciliationError` (`ErrPaymentReconciliationRequired`) holding the sent Credential, since a replayed Credential may already have settled. Read receipts with `mpp.ReceiptFromResponse`.
- Method-specific origin rules belong in the method, through `client.OriginValidator`; the generic clients only keep a built-in Tempo proof check for methods that do not implement it (`pkg/internal/tempoproof`). Origins compare with lowercase hosts and default ports dropped (`tempoproof.Origin`).
- Tempo server: construct with `charge.New(charge.Config{...})` and list accepted tokens in `Config.Currencies`. Sponsor fees with `AllowedFeeTokens` and `FeePayerPolicy`; a `FeeToken` must be in an explicit `AllowedFeeTokens` list. The fee payer is configured on the server, as in mppx: a local `FeePayerSigner`/`FeePayerPrivateKey`, or a hosted service with `FeePayerURL` or `HostedFeePayer{URL, Headers}` (co-signs with `eth_fillTransaction`; the server recovers the sponsor, simulates and broadcasts). With one, every route is sponsored unless `ChargeParams.DisableFeePayer`; without one, sponsorship is never advertised (relays excepted). Challenges never carry a fee payer URL. Zero-amount charges accept only proof credentials. The primary transfer carries `methodDetails.memo` (`ChargeParams.Memo`) exactly when set; otherwise push (hash) credentials must carry the Challenge's attribution memo (a hash names no signer, so the memo binds it), while pull (transaction) credentials may carry it, another memo or none (the chain refuses the client's signed transaction twice). A configured memo that is the realm's attribution memo for another Challenge never matches, so a challenge-bound replay key can expire safely.
- Tempo client: build `Config.PaymentPolicy` from `charge.MaxAmount`, `MaxAtomicAmount` and `AllowChainIDs` combined with `AllPolicies`; trust proof realms with `Config.TrustedProofRealms`. Use `tempo.ChainIDMainnet`/`ChainIDModerato`, `tempo.USDCEAddress` and `tempo.RPCURLForChain` (which reports unknown chains). RPC clients built from URLs are cached per method (`pkg/tempo/internal/rpccache`).

## Testing

The live tests are a nested module, `tests/` (`github.com/tempoxyz/mpp-go/tests`, with `replace` to the library), so testcontainers-go stays out of the library's `go.mod`. They use the `integration` build tag:

```bash
cd tests && go test -race -tags integration ./...                    # Docker only
TEMPO_RPC_URL=http://localhost:8545 go test -tags integration ./...  # running node (compose, Moderato)
```

- Without `TEMPO_RPC_URL`, `TestMain` starts the base Tempo test container, `tests/tempotest` (`Start`, or `Run(tb)` in one test): a thin wrapper over the Tempo team's published `ghcr.io/tempoxyz/tempo-localnet` image (see [tempoxyz/tempo docs/localnet.md](https://github.com/tempoxyz/tempo/blob/main/docs/localnet.md)), run with the image and command of the `tempo-node` service in `docker-compose.yml` (the single source of truth for the pinned digest; `TEMPO_LOCALNET_IMAGE` overrides it), on a loopback port, terminated at the end. It funds one shared payer once; tests using it run sequentially.
- The image's Docker `HEALTHCHECK` reports healthy only after the localnet's setup completes, so `tempotest.Start` waits for it (`wait.ForHealthCheck`) and `docker compose up -d --wait` does too.
- Then check a node with `internal/localnet.WaitReady` (the examples' `devnet.WaitForRPC`, `tempotest.Start` and `TestMain` for a `TEMPO_RPC_URL` node do), not `eth_chainId` alone: a fresh localnet answers RPC while its latest block is genesis, at timestamp 0, and rejects the faucet's and clients' expiring-nonce transactions (`valid_before ... too far in the future`) until it builds a block at the current time. `WaitReady` requires the latest block within 10s of the clock.
- They cover the HTTP charge flows, each framework adapter (net/http, chi, gin, echo, fiber), a paid MCP tool call, `redisstore.Store` replay protection against a real Redis container, and the examples: `examples/*/smoke_test.go` (also tagged `integration`) run each example's server and client, driven by `TestIntegrationExamples` as a child `go test ./examples/...`.
- `.github/workflows/smoke-cross-sdk.yml` pays the Go example server with the pinned mppx CLI and a small mppx server (`.github/smoke-cross-sdk/`) with the Go client.

<!-- BEGIN MPP SHARED: synced from tempoxyz/mpp-tools agents/shared.md. Edit it there; changes here are overwritten. -->

## MPP

Conventions every MPP repository shares, pulled together from their AGENTS.md files. Synced from [`agents/shared.md`](https://github.com/tempoxyz/mpp-tools/blob/main/agents/shared.md) in tempoxyz/mpp-tools: edit it there. The sections above it are this repository's own and win where they are more specific.

### This file

- Keep it to `# <repository>` and a short paragraph, then `## Commands`, `## Layout`, `## Conventions`, then only the sections this repository needs, then this section.
- Write rules an agent can act on: commands, paths, constraints. Leave out background, history and anything the code, specs or README already say.
- When you learn a convention the hard way, add it to the section it belongs to in the same pull request.

### Code

- Make the smallest change that does the job and follow the surrounding code. Start concrete; extract an abstraction only once several real uses share it.
- Keep the public API narrow. Add rather than remove or rename, and mark what you replace as deprecated before deleting it.
- Document every exported item (JSDoc, rustdoc, docstrings, Go doc comments). Comments say why, not what.
- Match mppx, the reference SDK: its names (Challenge, Credential, Receipt, Method), defaults, errors and behavior, unless the language forces otherwise; say why when it does.
- The IETF drafts in tempoxyz/mpp-specs (paymentauth.org) are the source of truth for the protocol, and mppx settles what they leave open. Link them instead of restating them.
- Add no dependency without a reason the pull request states. Prefer the standard library and what the repository already uses.
- Never log or commit keys, credentials, signatures, authorization headers or raw upstream errors.
- Tempo chain IDs: mainnet `4217`, Moderato testnet `42431`. Never `98865`.

### Tooling

Use each language's standard toolchain, run through the commands in `## Commands`, and keep it clean with warnings as errors:

- TypeScript: pnpm, strict `tsc`, ESM with `.js` relative imports, the repository's configured formatter and linter, Vitest.
- Python: uv, ruff (format and lint), a strict type checker (pyright or ty), pytest or unittest.
- Rust: cargo, `cargo fmt`, `cargo clippy -D warnings`, `cargo test`.
- Go: `gofmt`, `go vet`, golangci-lint, `go test -race`.
- Swift: SwiftPM, `swift test -Xswiftc -warnings-as-errors`.
- Run the checks that cover your change before opening a pull request, and add tests for behavior that matters, next to the existing ones.
- Pin GitHub Actions by full commit SHA. Workflows start with `tempoxyz/gh-actions/actions/secure-runner` and get write tokens from GitHub STS policies in `.github/sts/`, never stored secrets.

### Changes

- Commits and pull request titles use Conventional Commits: `feat:`, `fix:`, `docs:`, `refactor:`, `test:`, `chore:`, optionally scoped.
- Never edit `CHANGELOG.md` by hand. Record a release note the way `## Conventions` says (changeset, `.changelog/` fragment, or none when a workflow writes it), in the past tense.
- Before 1.0, bump `patch` for everything except a breaking public API change, which is `minor`.
- Don't edit README files unless asked.
- Never force-push.

### Docs and prose

- Active voice, present tense, second person. Sentence-case headings. No filler (just, simply, easily) and no Latin abbreviations.
- Capitalize Challenge, Credential and Receipt as MPP concepts. Say stablecoins, not crypto, and USDC.e for bridged USDC on Tempo. Write `mppx` in lowercase.

<!-- END MPP SHARED -->
