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

### Package Layout

```
pkg/
├── client/          # Generic HTTP 402 retry transport
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

payment := server.New(method, realm, secret)

// ❌ Bad
import (
    mppserver "github.com/tempoxyz/mpp-go/pkg/server"
)

payment := mppserver.New(method, realm, secret)
```

When a local variable would shadow the package name, use a short variable name (`c`, `srv`) instead of aliasing the import.

**Exception**: Internal library code in `pkg/tempo/server` and `pkg/tempo/client` must use `mppserver`/`mppclient` aliases because the file's own package name (`server`/`client`) would conflict.

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
TEMPO_RPC_URL=http://localhost:8545 make integration  # Run integration tests
```

