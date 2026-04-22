package main

import (
	"context"
	"log"
	"net/http"
	"os"

	"github.com/ethereum/go-ethereum/crypto"
	echofw "github.com/labstack/echo/v4"
	"github.com/tempoxyz/mpp-go/examples/internal/devnet"
	"github.com/tempoxyz/mpp-go/pkg/server"
	echoadapter "github.com/tempoxyz/mpp-go/pkg/server/echo"
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

	secretKey := "echo-example-secret"
	if envSecret := os.Getenv("MPP_SECRET_KEY"); envSecret != "" {
		secretKey = envSecret
	}

	payment := server.New(method, devnet.Realm, secretKey)
	e := echofw.New()

	e.GET("/health", func(c echofw.Context) error {
		return c.JSON(http.StatusOK, map[string]any{"status": "ok"})
	})

	e.GET("/paid", func(c echofw.Context) error {
		credential := echoadapter.Credential(c)
		receipt := echoadapter.Receipt(c)
		return c.JSON(http.StatusOK, map[string]any{
			"data":    "paid content",
			"payer":   credential.Source,
			"receipt": receipt.Reference,
		})
	}, echoadapter.ChargeMiddleware(payment, server.ChargeParams{
		Amount:      "0.50",
		Description: "Echo-paid endpoint",
	}))

	log.Printf("MPP Echo server listening on http://localhost:3002")
	log.Printf("Recipient: %s", merchant.Address().Hex())
	log.Printf("RPC URL: %s", rpcURL)
	if err := e.Start(":3002"); err != nil {
		log.Fatal(err)
	}
}
