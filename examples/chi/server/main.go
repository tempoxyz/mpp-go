package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"

	"github.com/ethereum/go-ethereum/crypto"
	"github.com/go-chi/chi/v5"
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

	merchantKey, err := crypto.GenerateKey()
	if err != nil {
		log.Fatal(err)
	}
	merchant := temposigner.NewSignerFromKey(merchantKey)
	if err := devnet.FundAddress(ctx, rpc, merchant.Address()); err != nil {
		log.Fatal(err)
	}

	method, err := charge.MethodFromConfig(charge.Config{
		RPCURL:    rpcURL,
		ChainID:   chainID,
		Currency:  devnet.Currency,
		Recipient: merchant.Address().Hex(),
	})
	if err != nil {
		log.Fatal(err)
	}

	secretKey := "chi-example-secret"
	if envSecret := os.Getenv("MPP_SECRET_KEY"); envSecret != "" {
		secretKey = envSecret
	}

	payment := server.New(method, devnet.Realm, secretKey)
	router := chi.NewRouter()

	router.Get("/health", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok"})
	})

	router.With(server.ChargeMiddleware(payment, server.ChargeParams{
		Amount:      "0.50",
		Description: "Chi-paid endpoint",
	})).Get("/paid", func(w http.ResponseWriter, r *http.Request) {
		credential := server.CredentialFromContext(r.Context())
		receipt := server.ReceiptFromContext(r.Context())
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data":    "paid content",
			"payer":   credential.Source,
			"receipt": receipt.Reference,
		})
	})

	log.Printf("MPP Chi server listening on http://localhost:3003")
	log.Printf("Recipient: %s", merchant.Address().Hex())
	log.Printf("RPC URL: %s", rpcURL)
	if err := http.ListenAndServe(":3003", router); err != nil {
		log.Fatal(err)
	}
}
