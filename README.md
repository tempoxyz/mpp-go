# mpp-go

Go SDK for the [Machine Payments Protocol](https://mpp.dev).

This repository is organized in a `tempo-go`-style `pkg/*` layout and focuses on the practical charge-only HTTP 402 flow:

- Generic protocol primitives in `pkg/mpp`
- Generic HTTP 402 client/server flow in `pkg/client` and `pkg/server`
- Tempo charge request types, attribution helpers, and replay stores in `pkg/tempo`
- Tempo charge credential creation in `pkg/tempo/client`
- Tempo charge verification, fee-payer co-signing, and receipt validation in `pkg/tempo/server`

The example programs under [`examples/`](./examples) are runnable end-to-end. Each example directory contains:

- `server.go` for the paid HTTP handler and Tempo verifier setup
- `client.go` for the automatic 402-aware caller
- `main.go` for the end-to-end demo that starts the server and then runs the client against it

All of the examples talk to the mock Tempo RPC in [`examples/internal/temposim`](./examples/internal/temposim), so you can inspect the full Challenge → Credential → Receipt flow without a devnet.

## Install

```bash
go get github.com/tempoxyz/mpp-go
```

## Quick Start

The examples below use `mppclient` and `mppserver` for the generic HTTP 402
helpers. The Tempo charge packages export as `chargeclient` and `chargeserver`
directly, so mixed client/server programs do not need awkward `tempoclient` or
`temposerver` aliases.

### 1. Server

```go
package main

import (
	"encoding/json"
	"net/http"

	mppserver "github.com/tempoxyz/mpp-go/pkg/server"
	"github.com/tempoxyz/mpp-go/pkg/tempo"
	chargeserver "github.com/tempoxyz/mpp-go/pkg/tempo/server"
)

func main() {
	intent, _ := chargeserver.NewChargeIntent(chargeserver.ChargeIntentConfig{
		RPCURL: "https://rpc.moderato.tempo.xyz",
	})
	method := chargeserver.NewMethod(chargeserver.MethodConfig{
		Intent:    intent,
		ChainID:   42431,
		Currency:  tempo.DefaultCurrencyForChain(42431),
		Recipient: "0x70997970c51812dc3a010c7d01b50e0d17dc79c8",
	})
	payment := mppserver.New(method, "api.example.com", "replace-me")

	handler := mppserver.ChargeMiddleware(payment, mppserver.ChargeParams{
		Amount:      "0.50",
		Description: "Paid content",
	})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"data":  "paid content",
			"payer": mppserver.CredentialFromContext(r.Context()).Source,
		})
	}))

	_ = http.ListenAndServe(":8080", handler)
}
```

### 2. Client

```go
package main

import (
	"context"
	"encoding/json"
	"fmt"

	mppclient "github.com/tempoxyz/mpp-go/pkg/client"
	"github.com/tempoxyz/mpp-go/pkg/mpp"
	chargeclient "github.com/tempoxyz/mpp-go/pkg/tempo/client"
)

func main() {
	method, _ := chargeclient.New(chargeclient.Config{
		PrivateKey: "0x59c6995e998f97a5a0044966f0945389dc9e86dae88c7a8412f4603b6b78690d",
		ChainID:    42431,
		RPCURL:     "https://rpc.moderato.tempo.xyz",
	})

	client := mppclient.New([]mppclient.Method{method})
	resp, err := client.Get(context.Background(), "https://api.example.com/paid")
	if err != nil {
		panic(err)
	}
	defer resp.Body.Close()

	receipt, _ := mpp.ParsePaymentReceipt(resp.Header.Get("Payment-Receipt"))
	var body struct {
		Data  string `json:"data"`
		Payer string `json:"payer"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&body)
	fmt.Printf("paid request for %q from %s with receipt %s\n", body.Data, body.Payer, receipt.Reference)
}
```

The server-side helper issues the `WWW-Authenticate: Payment ...` challenge automatically and the generic client retries automatically with a Tempo credential. You only configure the charge request on the server and the Tempo signing method on the client.

When a file imports both the generic HTTP 402 packages and the Tempo charge packages,
role-based aliases like `mppclient` and `mppserver` keep the generic helpers clear
next to the more specific `chargeclient` and `chargeserver` Tempo packages.

For the common Tempo charge flow, prefer `mppserver.ChargeMiddleware` on the
server. If you need to branch manually on challenge-versus-verified results,
call `(*mppserver.Mpp).Charge` directly.

## Tempo Charge Surface

The Tempo implementation in this repo covers the current charge-only feature set for this SDK:

- HTTP 402 challenge and retry flow
- Tempo `charge` intent only
- Transaction credential payloads
- Hash credential payloads
- Zero-amount proof credentials
- Access-key proof verification
- Split payments
- Fee-payer co-signing on the server
- Client-side attribution memo generation
- Server-side transfer/log validation
- Replay protection for charge hashes and proof challenges via `tempo.Store`

This pass intentionally does not include sessions, MCP, proxies, multi-method negotiation, discovery, or non-Tempo payment methods.

## Packages

| Package | Description |
|---------|-------------|
| `github.com/tempoxyz/mpp-go/pkg/mpp` | Challenge, Credential, Receipt, HMAC binding, expiry helpers, header parsing |
| `github.com/tempoxyz/mpp-go/pkg/client` | Generic HTTP 402-aware transport and client |
| `github.com/tempoxyz/mpp-go/pkg/server` | Generic verify-or-challenge flow and middleware helpers |
| `github.com/tempoxyz/mpp-go/pkg/tempo` | Shared Tempo charge types, defaults, attribution helpers, replay store |
| `github.com/tempoxyz/mpp-go/pkg/tempo/client` | Tempo charge credential creation for transaction and hash flows |
| `github.com/tempoxyz/mpp-go/pkg/tempo/server` | Tempo charge verification, replay checks, fee-payer co-signing |

## Replay Stores

`chargeserver.NewChargeIntent` defaults to `tempo.NewMemoryStore()`, which is enough for single-process demos and tests. For multi-instance deployments, pass a Redis-backed store so replay protection is shared across servers.

```go
package main

import (
	"github.com/redis/go-redis/v9"
	"github.com/tempoxyz/mpp-go/pkg/tempo"
	chargeserver "github.com/tempoxyz/mpp-go/pkg/tempo/server"
)

func main() {
	redisClient := redis.NewClient(&redis.Options{Addr: "127.0.0.1:6379"})
	intent, _ := chargeserver.NewChargeIntent(chargeserver.ChargeIntentConfig{
		RPCURL: "https://rpc.moderato.tempo.xyz",
		Store:  tempo.NewRedisStore(redisClient, 0),
	})
	_ = intent
}
```

The Redis store preserves the same replay-protection semantics as the in-memory implementation. Its optional TTL only applies to `Put`; replay keys inserted through `PutIfAbsent` stay persistent so used hashes and proofs cannot become valid again after expiry.

## Examples

Each example is a single runnable flow: `main.go` starts the example server and then runs the example client against it. The server setup lives in `server.go`, and the client-side retry logic lives in `client.go`.

| Example | Demonstrates | Run |
|---------|-------------|-----|
| [`examples/charge-basic`](./examples/charge-basic) | Generic Tempo charge flow using the high-level MPP client/server helpers | `go run ./examples/charge-basic` |
| [`examples/charge-hash`](./examples/charge-hash) | `push` mode with hash credentials and an automatic retry from the generic MPP client | `go run ./examples/charge-hash` |
| [`examples/charge-fee-payer`](./examples/charge-fee-payer) | Sponsored Tempo charge flow where the server co-signs as a fee payer | `go run ./examples/charge-fee-payer` |

All three examples are self-contained and use the same demo values from [`examples/internal/temposim`](./examples/internal/temposim), including a fixed demo payer key, a fixed fee-payer key, and a sample TIP-20 currency address.

## Testing

```bash
go test -mod=mod ./...
go vet ./...
```

For a true end-to-end Tempo charge test against a local node, this repo includes the
same Docker-based devnet pattern used in `tempo-go`.

```bash
# Start local Tempo node
docker compose up -d

# Run local-node integration tests
make integration

# Stop node
docker compose down
```

The integration suite exercises the full HTTP 402 retry flow against a live Tempo RPC,
including transaction credentials, fee-payer co-signing, hash credentials, and replay protection.

## Protocol

Built on the ["Payment" HTTP Authentication Scheme](https://datatracker.ietf.org/doc/draft-ryan-httpauth-payment/). See [mpp-specs](https://tempoxyz.github.io/mpp-specs/) for the full specification.

## License

MIT OR Apache-2.0
