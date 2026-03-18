# mpp-go

Go SDK for the [Machine Payments Protocol](https://mpp.dev).

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

	"github.com/tempoxyz/mpp-go/mpp"
	"github.com/tempoxyz/mpp-go/server"
)

func main() {
	m := server.New(myMethod, "api.example.com", "my-secret-key")

	http.HandleFunc("/resource", func(w http.ResponseWriter, r *http.Request) {
		result, err := m.Charge(r.Context(), server.ChargeParams{
			Authorization: r.Header.Get("Authorization"),
			Amount:        "500000",
			Currency:      "0x20c0000000000000000000000000000000000000",
			Recipient:     "0xf39Fd6e51aad88F6F4ce6aB8827279cffFb92266",
		})
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		if result.IsChallenge() {
			w.Header().Set("WWW-Authenticate", result.Challenge.ToWWWAuthenticate("api.example.com"))
			w.Header().Set("Content-Type", "application/problem+json")
			w.WriteHeader(http.StatusPaymentRequired)
			pe := mpp.ErrPaymentRequired("api.example.com", "")
			json.NewEncoder(w).Encode(pe.ProblemDetails(result.Challenge.ID))
			return
		}

		w.Header().Set("Payment-Receipt", result.Receipt.ToPaymentReceipt())
		json.NewEncoder(w).Encode(map[string]any{
			"data":  "paid content",
			"payer": result.Credential.Source,
		})
	})

	http.ListenAndServe(":8080", nil)
}
```

### Client

```go
package main

import (
	"context"
	"fmt"
	"io"

	"github.com/tempoxyz/mpp-go/client"
)

func main() {
	c := client.New([]client.Method{myTempoMethod})
	resp, err := c.Get(context.Background(), "https://api.example.com/resource")
	if err != nil {
		panic(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	fmt.Println(string(body))
}
```

### Middleware

```go
mux := http.NewServeMux()

protected := server.PaymentMiddleware(m, "500000")

mux.Handle("/paid", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
	cred := server.CredentialFromContext(r.Context())
	receipt := server.ReceiptFromContext(r.Context())
	json.NewEncoder(w).Encode(map[string]any{
		"data":    "paid content",
		"payer":   cred.Source,
		"receipt": receipt.Reference,
	})
})))
```

## Packages

| Package | Description |
|---------|-------------|
| `mpp` | Core types — Challenge, Credential, Receipt, errors, header parsing |
| `client` | HTTP client with automatic 402 payment handling |
| `server` | Server-side verification, middleware, challenge generation |

## Core Types

```go
// Challenge — server-issued payment challenge (WWW-Authenticate)
challenge := mpp.NewChallenge(secretKey, realm, "tempo", "charge", request,
	mpp.WithExpires(mpp.Expires.Minutes(5)),
	mpp.WithDescription("API access"),
)

// Credential — client payment proof (Authorization)
cred, err := mpp.FromAuthorization(header)

// Receipt — server payment confirmation (Payment-Receipt)
receipt := mpp.Success("0x...", mpp.WithReceiptMethod("tempo"))
```

## Protocol

Built on the ["Payment" HTTP Authentication Scheme](https://datatracker.ietf.org/doc/draft-ryan-httpauth-payment/). See [mpp-specs](https://tempoxyz.github.io/mpp-specs/) for the full spec.

## License

MIT OR Apache-2.0
