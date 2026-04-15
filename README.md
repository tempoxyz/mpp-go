# mpp-go

Go SDK for the [Machine Payments Protocol](https://mpp.dev).

This repository is organized in a `tempo-go`-style `pkg/*` layout and focuses on the practical charge-only HTTP 402 flow:

- Generic protocol primitives in `pkg/mpp`
- Generic HTTP 402 client/server flow in `pkg/client` and `pkg/server`
- Tempo charge request types, attribution helpers, and replay stores in `pkg/tempo`
- Tempo charge credential creation in `pkg/tempo/client`
- Tempo charge verification, fee-payer co-signing, and receipt validation in `pkg/tempo/server`

The example programs under [`examples/`](./examples) are runnable end-to-end: each one starts a local HTTP server, uses the generic MPP client, and talks to a mock Tempo RPC so you can inspect the full Challenge → Credential → Receipt flow without a devnet.

## Install

```bash
go get github.com/tempoxyz/mpp-go
```

## Quick Start

### Server

```go
package main

import (
	"encoding/json"
	"net/http"

	"github.com/tempoxyz/mpp-go/pkg/mpp"
	mppserver "github.com/tempoxyz/mpp-go/pkg/server"
	"github.com/tempoxyz/mpp-go/pkg/tempo"
	"github.com/tempoxyz/mpp-go/pkg/tempo/server"
)

func main() {
	intent, _ := temposerver.NewChargeIntent(temposerver.ChargeIntentConfig{
		RPCURL: "https://rpc.moderato.tempo.xyz",
	})
	method := temposerver.NewMethod(temposerver.MethodConfig{
		Intent:    intent,
		ChainID:   42431,
		Currency:  tempo.DefaultCurrencyForChain(42431),
		Recipient: "0x70997970c51812dc3a010c7d01b50e0d17dc79c8",
	})
	payment := mppserver.New(method, "api.example.com", "replace-me")

	http.HandleFunc("/paid", func(w http.ResponseWriter, r *http.Request) {
		result, err := payment.Charge(r.Context(), mppserver.ChargeParams{
			Authorization: r.Header.Get("Authorization"),
			Amount:        "0.50",
		})
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		if result.IsChallenge() {
			w.Header().Set("WWW-Authenticate", result.Challenge.ToWWWAuthenticate("api.example.com"))
			w.WriteHeader(http.StatusPaymentRequired)
			json.NewEncoder(w).Encode(mpp.ErrPaymentRequired("api.example.com", "").ProblemDetails(result.Challenge.ID))
			return
		}

		w.Header().Set("Payment-Receipt", result.Receipt.ToPaymentReceipt())
		json.NewEncoder(w).Encode(map[string]any{
			"data":  "paid content",
			"payer": result.Credential.Source,
		})
	})

	_ = http.ListenAndServe(":8080", nil)
}
```

### Client

```go
package main

import (
	"context"
	"fmt"
	"io"

	mppclient "github.com/tempoxyz/mpp-go/pkg/client"
	"github.com/tempoxyz/mpp-go/pkg/tempo/client"
)

func main() {
	method, _ := tempoclient.New(tempoclient.Config{
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

	body, _ := io.ReadAll(resp.Body)
	fmt.Println(string(body))
}
```

## Tempo Charge Surface

The Tempo implementation in this repo covers the same first-pass feature set we aligned on across the SDKs:

- HTTP 402 challenge and retry flow
- Tempo `charge` intent only
- Transaction credential payloads
- Hash credential payloads
- Fee-payer co-signing on the server
- Client-side attribution memo generation
- Server-side transfer/log validation
- Replay protection via `tempo.Store`

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

## Examples

| Example | Description |
|---------|-------------|
| [`examples/charge-basic`](./examples/charge-basic) | End-to-end Challenge, Credential, and Receipt flow |
| [`examples/charge-hash`](./examples/charge-hash) | End-to-end hash credential flow (`push` mode) |
| [`examples/charge-fee-payer`](./examples/charge-fee-payer) | End-to-end sponsored transaction flow with a fee payer |

## Testing

```bash
go test ./...
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
