# mpp-go

Go SDK for the [Machine Payments Protocol](https://mpp.dev).

This SDK focuses on the Tempo `charge` flow for HTTP 402 payments, including transaction, hash, proof, split-payment, and fee-payer support.

## Documentation

Full documentation, API reference, and guides are available at **[mpp.dev/sdk/go](https://mpp.dev/sdk/go)**.

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

	mppserver "github.com/tempoxyz/mpp-go/pkg/server"
	"github.com/tempoxyz/mpp-go/pkg/tempo"
	charge "github.com/tempoxyz/mpp-go/pkg/tempo/server"
)

func main() {
	intent, _ := charge.NewChargeIntent(charge.ChargeIntentConfig{
		RPCURL: "https://rpc.moderato.tempo.xyz",
	})

	method := charge.NewMethod(charge.MethodConfig{
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
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data":  "paid content",
			"payer": mppserver.CredentialFromContext(r.Context()).Source,
		})
	}))

	_ = http.ListenAndServe(":8080", handler)
}
```

### Client

```go
package main

import (
	"context"
	"encoding/json"
	"fmt"

	mppclient "github.com/tempoxyz/mpp-go/pkg/client"
	"github.com/tempoxyz/mpp-go/pkg/mpp"
	charge "github.com/tempoxyz/mpp-go/pkg/tempo/client"
)

func main() {
	method, _ := charge.New(charge.Config{
		PrivateKey: "0x59c6995e998f97a5a0044966f0945389dc9e86dae88c7a8412f4603b6b78690d",
		ChainID:    42431,
		RPCURL:     "https://rpc.moderato.tempo.xyz",
	})

	client := mppclient.New([]mppclient.Method{method})
	response, err := client.Get(context.Background(), "https://api.example.com/paid")
	if err != nil {
		panic(err)
	}
	defer response.Body.Close()

	receipt, _ := mpp.ParsePaymentReceipt(response.Header.Get("Payment-Receipt"))

	var body struct {
		Data  string `json:"data"`
		Payer string `json:"payer"`
	}
	_ = json.NewDecoder(response.Body).Decode(&body)

	fmt.Printf("paid request for %q from %s with receipt %s\n", body.Data, body.Payer, receipt.Reference)
}
```

The server issues the `WWW-Authenticate: Payment ...` challenge automatically, and the generic client retries automatically with a Tempo credential.

## Examples

| Example | Description |
|---------|-------------|
| [charge-basic](./examples/charge-basic/) | Generic Tempo charge flow using the high-level MPP client and server helpers |
| [charge-hash](./examples/charge-hash/) | Push-mode charge flow with a hash credential |
| [charge-fee-payer](./examples/charge-fee-payer/) | Sponsored Tempo charge flow where the server co-signs as a fee payer |

The examples run against the local Tempo devnet in [`docker-compose.yml`](./docker-compose.yml).

```bash
docker compose up -d

go run ./examples/charge-basic
go run ./examples/charge-hash
go run ./examples/charge-fee-payer
```

Set `TEMPO_RPC_URL` if you want the examples to target a different Tempo RPC.

## Protocol

Built on the ["Payment" HTTP Authentication Scheme](https://datatracker.ietf.org/doc/draft-ryan-httpauth-payment/). See [mpp-specs](https://tempoxyz.github.io/mpp-specs/) for the full specification.

## License

MIT OR Apache-2.0
