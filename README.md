<br>
<br>

<p align="center">
  <a href="https://mpp.dev">
    <picture>
      <source media="(prefers-color-scheme: dark)" srcset="https://raw.githubusercontent.com/tempoxyz/mpp/refs/heads/main/public/lockup-light.svg">
      <img alt="Machine Payments Protocol" src="https://raw.githubusercontent.com/tempoxyz/mpp/refs/heads/main/public/lockup-dark.svg" width="auto" height="120">
    </picture>
  </a>
</p>

<br>
<br>

# mpp-go

Go SDK for the [**Machine Payments Protocol**](https://mpp.dev)

[![Website](https://img.shields.io/badge/website-mpp.dev-black)](https://mpp.dev)
[![Docs](https://img.shields.io/badge/docs-mpp.dev-blue)](https://mpp.dev/sdk/go)
[![Go Reference](https://pkg.go.dev/badge/github.com/tempoxyz/mpp-go.svg)](https://pkg.go.dev/github.com/tempoxyz/mpp-go)
[![License](https://img.shields.io/badge/license-MIT%20OR%20Apache--2.0-blue)](LICENSE)

[MPP](https://mpp.dev) lets any client — agents, apps, or humans — pay for any service in the same HTTP request. It standardizes [HTTP 402](https://mpp.dev/protocol/http-402) with an open [IETF specification](https://paymentauth.org), so servers can charge and clients can pay without API keys, billing accounts, or checkout flows.
## Documentation

You can get started today by reading the [Go SDK docs](https://mpp.dev/sdk/go), the module-level [Go doc reference](https://pkg.go.dev/github.com/tempoxyz/mpp-go), exploring the [protocol overview](https://mpp.dev/protocol/), or jumping straight to the [quickstart](https://mpp.dev/quickstart/).

Package docs:

- [`pkg/client`](https://pkg.go.dev/github.com/tempoxyz/mpp-go/pkg/client)
- [`pkg/server`](https://pkg.go.dev/github.com/tempoxyz/mpp-go/pkg/server)
- [`pkg/tempo`](https://pkg.go.dev/github.com/tempoxyz/mpp-go/pkg/tempo)
- [`pkg/tempo/client`](https://pkg.go.dev/github.com/tempoxyz/mpp-go/pkg/tempo/client)
- [`pkg/tempo/server`](https://pkg.go.dev/github.com/tempoxyz/mpp-go/pkg/tempo/server)

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

	"github.com/tempoxyz/mpp-go/pkg/server"
	"github.com/tempoxyz/mpp-go/pkg/tempo"
	charge "github.com/tempoxyz/mpp-go/pkg/tempo/server"
)

func main() {
	intent, _ := charge.NewIntent(charge.IntentConfig{
		RPCURL: "https://rpc.moderato.tempo.xyz",
	})

	method := charge.NewMethod(charge.MethodConfig{
		Intent:    intent,
		ChainID:   42431,
		Currency:  tempo.DefaultCurrencyForChain(42431),
		Recipient: "0x70997970c51812dc3a010c7d01b50e0d17dc79c8",
	})

	payment := server.New(method, "api.example.com", "replace-me")

	handler := server.ChargeMiddleware(payment, server.ChargeParams{
		Amount:      "0.50",
		Description: "Paid content",
	})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data":  "paid content",
			"payer": server.CredentialFromContext(r.Context()).Source,
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

	"github.com/tempoxyz/mpp-go/pkg/client"
	"github.com/tempoxyz/mpp-go/pkg/mpp"
	charge "github.com/tempoxyz/mpp-go/pkg/tempo/client"
)

func main() {
	method, _ := charge.New(charge.Config{
		PrivateKey: "0x59c6995e998f97a5a0044966f0945389dc9e86dae88c7a8412f4603b6b78690d",
		ChainID:    42431,
		RPCURL:     "https://rpc.moderato.tempo.xyz",
	})

	c := client.New([]client.Method{method})
	response, err := c.Get(context.Background(), "https://api.example.com/paid")
	if err != nil {
		panic(err)
	}
	defer response.Body.Close()

	receipt, _ := mpp.ParseReceipt(response.Header.Get("Payment-Receipt"))

	var body struct {
		Data  string `json:"data"`
		Payer string `json:"payer"`
	}
	_ = json.NewDecoder(response.Body).Decode(&body)

	fmt.Printf("paid request for %q from %s with receipt %s\n", body.Data, body.Payer, receipt.Reference)
}
```

## Examples

| Example | Description |
|---------|-------------|
| [basic](./examples/basic/) | Separate-process demo with a long-running server and standalone client, mirroring the `mpp-rs` sample layout |
| [charge-basic](./examples/charge-basic/) | Generic Tempo charge flow using the high-level MPP client and server helpers, available in both one-command and separate-process layouts |
| [charge-hash](./examples/charge-hash/) | Push-mode charge flow with a hash credential, available in both one-command and separate-process layouts |
| [charge-fee-payer](./examples/charge-fee-payer/) | Sponsored Tempo charge flow where the server co-signs as a fee payer, available in both one-command and separate-process layouts |

## Protocol

Built on the ["Payment" HTTP Authentication Scheme](https://paymentauth.org), an open specification proposed to the IETF. See [mpp.dev/protocol](https://mpp.dev/protocol/) for the full protocol overview, or the [IETF specification](https://paymentauth.org) for the wire format.

## Contributing

```
git clone https://github.com/tempoxyz/mpp-go
cd mpp-go
go test ./...
```

## Security

See [`SECURITY.md`](./SECURITY.md) for reporting vulnerabilities.

## License

Licensed under either of [Apache License, Version 2.0](./LICENSE-APACHE) or [MIT License](./LICENSE-MIT) at your option.

Unless you explicitly state otherwise, any contribution intentionally submitted
for inclusion in this project by you, as defined in the Apache-2.0 license,
shall be dual licensed as above, without any additional terms or conditions.
