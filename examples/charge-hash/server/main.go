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
		Intent:         intent,
		ChainID:        chainID,
		Currency:       devnet.Currency,
		Recipient:      devnet.Recipient,
		SupportedModes: []tempo.ChargeMode{tempo.ChargeModePush},
	})
	payment := mppserver.New(method, devnet.Realm, "example-secret")

	mux := http.NewServeMux()
	mux.Handle("/paid", mppserver.ChargeMiddleware(payment, mppserver.ChargeParams{
		Amount:         "0.50",
		Description:    "Hash credential Tempo charge example",
		SupportedModes: []tempo.ChargeMode{tempo.ChargeModePush},
	})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"tx": mppserver.ReceiptFromContext(r.Context()).Reference})
	})))

	log.Printf("charge-hash server listening on http://localhost:3000")
	if err := http.ListenAndServe(":3000", mux); err != nil {
		log.Fatal(err)
	}
}
