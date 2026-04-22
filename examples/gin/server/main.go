package main

import (
	"context"
	"log"
	"net/http"
	"os"

	"github.com/ethereum/go-ethereum/crypto"
	ginfw "github.com/gin-gonic/gin"
	"github.com/tempoxyz/mpp-go/examples/internal/devnet"
	"github.com/tempoxyz/mpp-go/pkg/server"
	ginadapter "github.com/tempoxyz/mpp-go/pkg/server/gin"
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

	method, err := charge.New(charge.Config{
		RPCURL:    rpcURL,
		ChainID:   chainID,
		Currency:  devnet.Currency,
		Recipient: merchant.Address().Hex(),
	})
	if err != nil {
		log.Fatal(err)
	}

	secretKey := "gin-example-secret"
	if envSecret := os.Getenv("MPP_SECRET_KEY"); envSecret != "" {
		secretKey = envSecret
	}

	payment := server.New(method, devnet.Realm, secretKey)
	router := ginfw.Default()

	router.GET("/health", func(c *ginfw.Context) {
		c.JSON(http.StatusOK, ginfw.H{"status": "ok"})
	})

	router.GET("/paid", ginadapter.ChargeMiddleware(payment, server.ChargeParams{
		Amount:      "0.50",
		Description: "Gin-paid endpoint",
	}), func(c *ginfw.Context) {
		credential := ginadapter.Credential(c)
		receipt := ginadapter.Receipt(c)
		c.JSON(http.StatusOK, ginfw.H{
			"data":    "paid content",
			"payer":   credential.Source,
			"receipt": receipt.Reference,
		})
	})

	log.Printf("MPP Gin server listening on http://localhost:3001")
	log.Printf("Recipient: %s", merchant.Address().Hex())
	log.Printf("RPC URL: %s", rpcURL)
	if err := router.Run(":3001"); err != nil {
		log.Fatal(err)
	}
}
