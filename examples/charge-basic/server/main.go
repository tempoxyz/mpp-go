package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"

	"github.com/tempoxyz/mpp-go/examples/internal/devnet"
	"github.com/tempoxyz/mpp-go/pkg/server"
	"github.com/tempoxyz/mpp-go/pkg/tempo"
	charge "github.com/tempoxyz/mpp-go/pkg/tempo/server"
)

func main() {
	ctx := context.Background()
	rpcURL := devnet.RPCURL()
	rpc := tempo.NewRPCClient(rpcURL)
	chainID, err := devnet.WaitForRPC(ctx, rpc)
	if err != nil {
		log.Fatal(err)
	}

	method, err := charge.MethodFromConfig(charge.Config{
		RPCURL:    rpcURL,
		ChainID:   chainID,
		Currency:  devnet.Currency,
		Recipient: devnet.Recipient,
	})
	if err != nil {
		log.Fatal(err)
	}
	payment := server.New(method, devnet.Realm, "example-secret")

	mux := http.NewServeMux()
	mux.Handle("/paid", server.ChargeMiddleware(payment, server.ChargeParams{
		Amount:      "0.50",
		Description: "Basic Tempo charge example",
	})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		credential := server.CredentialFromContext(r.Context())
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data":  "paid content",
			"payer": credential.Source,
		})
	})))

	log.Printf("charge-basic server listening on http://localhost:3000")
	if err := http.ListenAndServe(":3000", mux); err != nil {
		log.Fatal(err)
	}
}
