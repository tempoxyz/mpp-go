package main

import (
	"context"
	"log"
	"net/http"
	"os"

	"github.com/ethereum/go-ethereum/crypto"
	fiberfw "github.com/gofiber/fiber/v2"
	"github.com/tempoxyz/mpp-go/examples/internal/devnet"
	"github.com/tempoxyz/mpp-go/pkg/server"
	fiberadapter "github.com/tempoxyz/mpp-go/pkg/server/fiber"
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

	secretKey := "fiber-example-secret"
	if envSecret := os.Getenv("MPP_SECRET_KEY"); envSecret != "" {
		secretKey = envSecret
	}

	payment := server.New(method, devnet.Realm, secretKey)
	app := fiberfw.New()

	app.Get("/health", func(c *fiberfw.Ctx) error {
		return c.Status(http.StatusOK).JSON(fiberfw.Map{"status": "ok"})
	})

	app.Get("/paid", fiberadapter.ChargeMiddleware(payment, server.ChargeParams{
		Amount:      "0.50",
		Description: "Fiber-paid endpoint",
	}), func(c *fiberfw.Ctx) error {
		credential := fiberadapter.Credential(c)
		receipt := fiberadapter.Receipt(c)
		return c.Status(http.StatusOK).JSON(fiberfw.Map{
			"data":    "paid content",
			"payer":   credential.Source,
			"receipt": receipt.Reference,
		})
	})

	log.Printf("MPP Fiber server listening on http://localhost:3001")
	log.Printf("Recipient: %s", merchant.Address().Hex())
	log.Printf("RPC URL: %s", rpcURL)
	log.Fatal(app.Listen(":3001"))
}
