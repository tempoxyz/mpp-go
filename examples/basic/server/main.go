package main

import (
	"context"
	"encoding/json"
	"log"
	"math/rand/v2"
	"net/http"
	"os"

	"github.com/ethereum/go-ethereum/crypto"
	"github.com/tempoxyz/mpp-go/examples/internal/devnet"
	mppserver "github.com/tempoxyz/mpp-go/pkg/server"
	"github.com/tempoxyz/mpp-go/pkg/tempo"
	charge "github.com/tempoxyz/mpp-go/pkg/tempo/server"
	temposigner "github.com/tempoxyz/tempo-go/pkg/signer"
)

var fortunes = []string{
	"A beautiful, smart, and loving person will come into your life.",
	"A dubious friend may be an enemy in camouflage.",
	"A faithful friend is a strong defense.",
	"A fresh start will put you on your way.",
	"A golden egg of opportunity falls into your lap this month.",
	"A good time to finish up old tasks.",
	"A hunch is creativity trying to tell you something.",
	"A lifetime of happiness lies ahead of you.",
	"A light heart carries you through all the hard times.",
	"A new perspective will come with the new year.",
}

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

	intent, err := charge.NewChargeIntent(charge.ChargeIntentConfig{RPCURL: rpcURL})
	if err != nil {
		log.Fatal(err)
	}

	method := charge.NewMethod(charge.MethodConfig{
		Intent:    intent,
		ChainID:   chainID,
		Currency:  devnet.Currency,
		Recipient: merchant.Address().Hex(),
	})

	secretKey := "basic-example-secret"
	if envSecret := getenv("MPP_SECRET_KEY"); envSecret != "" {
		secretKey = envSecret
	}

	payment := mppserver.New(method, devnet.Realm, secretKey)
	mux := http.NewServeMux()

	mux.HandleFunc("/api/health", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok"})
	})

	mux.Handle("/api/ping", mppserver.ChargeMiddleware(payment, mppserver.ChargeParams{
		Amount:      "0.01",
		Description: "Ping the API",
	})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"pong": true})
	})))

	mux.Handle("/api/fortune", mppserver.ChargeMiddleware(payment, mppserver.ChargeParams{
		Amount:      "1.00",
		Description: "Get a fortune",
	})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		credential := mppserver.CredentialFromContext(r.Context())
		fortune := fortunes[rand.IntN(len(fortunes))]
		_ = json.NewEncoder(w).Encode(map[string]any{
			"fortune": fortune,
			"payer":   credential.Source,
		})
	})))

	log.Printf("MPP basic server listening on http://localhost:3000")
	log.Printf("Recipient: %s", merchant.Address().Hex())
	log.Printf("RPC URL: %s", rpcURL)
	if err := http.ListenAndServe(":3000", mux); err != nil {
		log.Fatal(err)
	}
}

func getenv(key string) string {
	return os.Getenv(key)
}
