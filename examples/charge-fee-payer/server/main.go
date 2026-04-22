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
	temposigner "github.com/tempoxyz/tempo-go/pkg/signer"
)

func main() {
	ctx := context.Background()
	rpcURL := devnet.RPCURL()
	rpc := tempo.NewRPCClient(rpcURL)
	chainID, err := devnet.WaitForRPC(ctx, rpc)
	if err != nil {
		log.Fatal(err)
	}

	feePayer, err := temposigner.NewSigner(devnet.FeePayerPrivateKey)
	if err != nil {
		log.Fatal(err)
	}
	if err := devnet.FundAddress(ctx, rpc, feePayer.Address()); err != nil {
		log.Fatal(err)
	}

	method, err := charge.New(charge.Config{
		RPCURL:             rpcURL,
		ChainID:            chainID,
		Currency:           devnet.Currency,
		FeePayerPrivateKey: devnet.FeePayerPrivateKey,
		FeePayer:           true,
		Recipient:          devnet.Recipient,
	})
	if err != nil {
		log.Fatal(err)
	}
	payment := server.New(method, devnet.Realm, "example-secret")

	mux := http.NewServeMux()
	mux.Handle("/paid", server.ChargeMiddleware(payment, server.ChargeParams{
		Amount:      "0.50",
		Description: "Fee-payer Tempo charge example",
		FeePayer:    true,
	})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"tx": server.ReceiptFromContext(r.Context()).Reference})
	})))

	log.Printf("charge-fee-payer server listening on http://localhost:3000")
	if err := http.ListenAndServe(":3000", mux); err != nil {
		log.Fatal(err)
	}
}
