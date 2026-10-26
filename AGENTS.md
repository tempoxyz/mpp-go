# mpp-go

Go SDK for the "Payment" HTTP Authentication Scheme (402 Protocol).

## Vision

mpp-go provides abstractions for the complete HTTP 402 payment flow — both client and server. The architecture has two layers:

### Core Abstractions

1. **`server.Mpp`** — Top-level payment handler. Groups a `Method` with realm and secret, handles the HTTP 402 flow (challenge/credential parsing, header serialization, verification).

2. **`server.Method`** — A payment method definition. Each method provides intents (e.g., `charge`) and builds challenge requests with method-specific parameters.

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
   │                                                   │
```

### Primitives

Low-level data structures in `pkg/mpp` that compose into the core abstractions:

- **`Challenge`** — Server-issued payment request (appears in `WWW-Authenticate` header). Contains `ID`, `Realm`, `Method`, `Intent`, `Request`, and optional `Expires`/`Digest`.
- **`Credential`** — Client-submitted payment proof (appears in `Authorization` header). Contains `Challenge` echo, `Payload` (method-specific proof), and optional `Source` (payer identity).
- **`Receipt`** — Server-issued settlement confirmation (appears in `Payment-Receipt` header). Contains `Status`, `Method`, `Timestamp`, and `Reference`.

Each type has one canonical Parse/Format pair; the other spellings
(`FormatAuthenticate*`, `ToAuthenticate*`, `FromAuthenticate`,
`FormatAuthorization*`, `ToAuthorization`, `FromAuthorization`,
`*PaymentReceipt`, `FormatReceipt`) are deprecated wrappers:

| Type | Parse | Format |
| --- | --- | --- |
| `Challenge` | `ParseChallenge` | `FormatChallenge(c) (string, error)`, writes `c.Realm` |
| `Credential` | `ParseCredential` | `FormatCredential(c) (string, error)` |
| `Receipt` | `ParseReceipt` | `FormatReceiptWithError(r) (string, error)` |

- Create challenges with `NewChallengeWithError` and check an echoed ID with
  `(*Challenge).VerifyID(secret)`, which binds `c.Realm`; also compare
  `c.Realm` with the realm you expect, which VerifyID does not. `NewChallenge`,
  `GenerateChallengeID` and `Verify(secret, realm)` are deprecated.
- The Format functions reject what their Parse counterpart rejects (missing
  or invalid fields, a receipt status other than "success", oversized
  headers), so the SDK never emits a header its own parser refuses.
- Responses: `ChallengesFromResponse` and `ReceiptFromResponse`. Expiry:
  `ExpiresIn`, `ParseExpires`, `CheckExpires`. Body digests:
  `ComputeBodyDigest`, `VerifyBodyDigest`. Amounts: `ParseAmount` (`*big.Int`).
  Intents: `IntentCharge`, `IntentSession`.
- Match problem types with `errors.Is(err, mpp.ErrorTypeX)`: `PaymentError`
  implements `Is` for `ErrorType` targets.
- Servers write every 402 and problem response with
  `server.WritePaymentResponse(w, err, challenges...)` (Fiber:
  `fiberadapter.WritePaymentResponse(c, err, challenges...)`).
  `WritePaymentError`, `WritePaymentErrorWithChallenge(s)` and
  `WriteChallenge(s)` are deprecated.

### Server charge flow

- One unexported, framework-neutral flow (`pkg/server/flow.go`) runs every
  charge: `ChargeMiddleware` is `ComposeMiddleware` with one offer, and the
  Gin, Echo and Fiber adapters call `(*Mpp).ChargeRequest` or
  `ComposeRequest` with their route scope, then write the result with
  `(*ChargeResult).ServeHTTP` (Fiber through a fasthttp-backed
  `http.ResponseWriter`). Only serving a verified payment (receipt
  deferral, context values) is framework-specific.
- HTTP handlers: `res, err := payment.ChargeRequest(r, params)`; when
  `err != nil || res.IsChallenge()`, `res.ServeHTTP(w, r)`. Other transports
  (MCP) use `Charge(ctx, params)` with the Credential in `params`, or
  `server.Compose(ctx, authorization, offers...)` (`Offer` is an alias of
  `ComposeConfig`; `ComposeCharge` is deprecated).
- `server.New` defaults an empty secret to `MPP_SECRET_KEY`. An empty realm
  stays empty: the realm is bound into every Challenge, so defaulting it would
  make upgraded and older replicas reject each other's Challenges; callers
  who want the environment's realm pass `server.DetectRealm()`.
- Server-wide options: `WithRequiresAuth`, `WithChallengeNonce` (replaces the
  deprecated per-call `ChargeParams.EnableChallengeNonce`, which still forces
  it on) and `WithEvents` (mppx's onChallengeCreated/onPaymentSuccess/
  onPaymentFailed). PaymentFailed fires once per distinct `Mpp` for every
  submitted Credential that is rejected, including one that cannot be parsed
  (nil Credential), whether it arrives through `Charge`, `ChargeRequest`,
  the middlewares, `Compose` or MCP `_meta`. Transports in this module
  report a Credential they reject before `Charge` through
  `internal/serverevents`.
- `_mppx_scope` stays a `{route, resource, query}` map inside the challenge
  request. mppx binds a `"GET /route"` string in the opaque metadata instead;
  aligning them is a wire change older Go verifiers reject, so it needs a
  product decision first.

### Package Layout

```
pkg/
├── client/          # Generic HTTP 402 retry transport
├── mcp/             # MPP over MCP for the official MCP Go SDK (matches mppx/mcp)
│   ├── client/      # Payment-aware MCP client middleware
│   ├── jsonrpc/     # Charge/Compose for raw MCP JSON-RPC messages (mppx Transport.mcp())
│   └── server/      # Charge/Compose for MCP tool handlers
├── mpp/             # Protocol primitives (Challenge, Credential, Receipt, parsing)
├── server/          # Generic challenge-or-verify flow
└── tempo/           # Tempo blockchain support
    ├── client/      # Tempo charge client method
    └── server/      # Tempo charge server method
```

## Import Conventions

**Use bare package names** — do not alias `pkg/server` as `mppserver` or `pkg/client` as `mppclient`. The Tempo-specific packages already use the `charge` alias, so there is no conflict:

```go
// ✅ Good
import (
    "github.com/tempoxyz/mpp-go/pkg/server"
    charge "github.com/tempoxyz/mpp-go/pkg/tempo/server"
)

payment, err := server.New(method, realm, secret)

// ❌ Bad
import (
    mppserver "github.com/tempoxyz/mpp-go/pkg/server"
)

payment, err := mppserver.New(method, realm, secret)
```

When a local variable would shadow the package name, use a short variable name (`c`, `srv`) instead of aliasing the import.

**Exception**: Internal library code in `pkg/tempo/server`, `pkg/tempo/client`, `pkg/mcp/server` and `pkg/mcp/client` must use `mppserver`/`mppclient` aliases because the file's own package name (`server`/`client`) would conflict.

## Spec Reference

Canonical specs live at [mpp-specs](https://tempoxyz.github.io/mpp-specs/) and [paymentauth.org](https://paymentauth.org).

### Spec Documents

| Layer         | Spec                                                                                                                                        | Description                                                             |
| ------------- | ------------------------------------------------------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------- |
| **Core**      | [draft-httpauth-payment-00](https://github.com/tempoxyz/payment-auth-spec/blob/main/specs/core/draft-httpauth-payment-00.md)                | 402 flow, `WWW-Authenticate`/`Authorization` headers, `Payment-Receipt` |
| **Intent**    | [draft-payment-intent-charge-00](https://github.com/tempoxyz/payment-auth-spec/blob/main/specs/intents/draft-payment-intent-charge-00.md)   | One-time immediate payment                                              |
| **Method**    | [draft-tempo-charge-00](https://github.com/tempoxyz/payment-auth-spec/blob/main/specs/methods/tempo/draft-tempo-charge-00.md)               | TIP-20 token transfers on Tempo                                         |

### Key Protocol Details

- **Challenge**: `WWW-Authenticate: Payment id="...", realm="...", method="...", intent="...", request="<base64url>"`
- **Credential**: `Authorization: Payment <base64url>` → `{ challenge, payload, source? }`
- **Receipt**: `Payment-Receipt: <base64url>` → `{ status, method, timestamp, reference }`
- **Encoding**: All JSON payloads use base64url without padding (RFC 4648)

### Challenge ID Binding

The challenge `id` is an HMAC-SHA256 over the challenge parameters, cryptographically binding the ID to its contents. This prevents tampering and ensures the server can verify challenge integrity without storing state.

**HMAC input** (concatenated, pipe-delimited):

```
realm | method | intent | request | expires | digest
```

**Generation:**

```
id = base64url(HMAC-SHA256(server_secret, input))
```

**Verification:** Server recomputes HMAC from echoed challenge parameters and compares to `id`. If mismatch, reject credential.

## Commands

```bash
go test ./...                                        # Run unit tests
go build ./...                                       # Build all packages
docker compose up -d                                 # Start local Tempo devnet
go run ./examples/charge-basic                       # Run charge-basic example
make integration                                     # Run live integration tests (needs Docker)
make test                                            # Unit tests with -race -shuffle=on -count=1 (as CI)
make lint                                            # golangci-lint (.golangci.yml), new issues since origin/main only
make lint-all                                        # golangci-lint, every issue including existing ones
make vuln                                            # govulncheck ./...
make mod-check                                       # go mod tidy -diff && go mod verify
make fuzz FUZZTIME=60s                               # Run every Fuzz* target under pkg/ (CI: nightly, fuzz.yml)
go test -run='^$' -fuzz='^FuzzParseChallenge$' -fuzztime=30s ./pkg/mpp  # Run one fuzz target
```

CI runs unit tests on the minimum Go version in go.mod (`go` line, with
`GOTOOLCHAIN=local`) and on the latest stable release, and cross-compiles
for darwin and windows. Install golangci-lint v2 to run `make lint`; CI
reports only issues on lines a change touches, so don't reformat untouched code
to fix existing findings.

### API compatibility

The API compatibility workflow (`.github/workflows/api-compat.yml`) runs
`apidiff` between a pull request's merge base and head and fails on an
incompatible change to an exported API under `pkg/` unless one of the pull
request's `.changelog/*.md` files declares `github.com/tempoxyz/mpp-go: minor`
(or `major`). Before v1, a breaking change is a minor bump. Prefer a compatible
change: add rather than remove or rename, and mark old APIs `// Deprecated:`.
To run it locally:

```bash
go install golang.org/x/exp/cmd/apidiff@v0.0.0-20261014143641-f0251eb4aa3e
git worktree add --detach /tmp/mpp-go-base origin/main
(cd /tmp/mpp-go-base && apidiff -m -w /tmp/base.api github.com/tempoxyz/mpp-go)
apidiff -m -w /tmp/head.api github.com/tempoxyz/mpp-go
apidiff -m -incompatible /tmp/base.api /tmp/head.api   # lines under ./pkg/ are breaking
git worktree remove /tmp/mpp-go-base
```

### Live integration tests

The live tests are a nested module, `tests/` (`github.com/tempoxyz/mpp-go/tests`,
with `replace` to the library), so testcontainers-go stays out of the library's
`go.mod`. They use the `integration` build tag:

```bash
cd tests && go test -race -tags integration ./...                    # Docker only
TEMPO_RPC_URL=http://localhost:8545 go test -tags integration ./...  # running node (compose, Moderato)
```

- Without `TEMPO_RPC_URL`, `TestMain` starts the image and command of the
  `tempo-node` service in `docker-compose.yml` (the single source of truth for
  the pinned image; `TEMPO_LOCALNET_IMAGE` overrides it) and terminates it at
  the end. It funds one shared payer once; tests using it run sequentially.
- They cover the HTTP charge flows, each framework adapter (net/http, chi, gin,
  echo, fiber), a paid MCP tool call, `tempo.RedisStore` replay protection
  against a real Redis container, and the examples: `examples/*/smoke_test.go`
  (also tagged `integration`) run each example's server and client, driven by
  `TestIntegrationExamples` as a child `go test ./examples/...`.
- `.github/workflows/smoke-cross-sdk.yml` pays the Go example server with the
  pinned mppx CLI and a small mppx server (`.github/smoke-cross-sdk/`) with the
  Go client.

## READMEs

Do not update README files (the root README.md or any example README) unless
explicitly asked to. Document new APIs in Go doc comments, AGENTS.md and the
changelog instead.

## Changelogs

Every PR should add a release note as a new Markdown file under `.changelog/`
(singular); do not use `.changelogs/` or edit `CHANGELOG.md` directly. Use the
full Go module path in frontmatter:

```markdown
---
github.com/tempoxyz/mpp-go: patch
---

Description of the change.
```
