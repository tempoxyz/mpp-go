package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"

	"github.com/tempoxyz/mpp-go/examples/internal/devnet"
	mppserver "github.com/tempoxyz/mpp-go/pkg/server"
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

	intent, err := charge.NewChargeIntent(charge.ChargeIntentConfig{RPCURL: rpcURL})
	if err != nil {
		log.Fatal(err)
	}
	method := charge.NewMethod(charge.MethodConfig{
		Intent:    intent,
		ChainID:   chainID,
		Currency:  devnet.Currency,
		Recipient: devnet.Recipient,
	})
	payment := mppserver.New(method, devnet.Realm, "example-secret")

	mux := http.NewServeMux()
	mux.Handle("/paid", mppserver.ChargeMiddleware(payment, mppserver.ChargeParams{
		Amount:      "0.50",
		Description: "Basic Tempo charge example",
	})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		credential := mppserver.CredentialFromContext(r.Context())
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
