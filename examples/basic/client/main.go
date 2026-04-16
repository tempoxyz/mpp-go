package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/tempoxyz/mpp-go/examples/internal/devnet"
	"github.com/tempoxyz/mpp-go/pkg/client"
	"github.com/tempoxyz/mpp-go/pkg/mpp"
	"github.com/tempoxyz/mpp-go/pkg/tempo"
	charge "github.com/tempoxyz/mpp-go/pkg/tempo/client"
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

	privateKey := os.Getenv("PRIVATE_KEY")
	if privateKey == "" {
		privateKey = devnet.PayerPrivateKey
	}

	signer, err := temposigner.NewSigner(privateKey)
	if err != nil {
		log.Fatal(err)
	}
	if err := devnet.FundAddress(ctx, rpc, signer.Address()); err != nil {
		log.Fatal(err)
	}

	method, err := charge.New(charge.Config{
		PrivateKey: privateKey,
		ChainID:    chainID,
		RPCURL:     rpcURL,
	})
	if err != nil {
		log.Fatal(err)
	}

	c := client.New([]client.Method{method})
	baseURL := os.Getenv("BASE_URL")
	if baseURL == "" {
		baseURL = "http://localhost:3000"
	}
	target := baseURL + "/api/fortune"
	if len(os.Args) > 1 {
		target = os.Args[1]
	}

	resp, err := c.Get(ctx, target)
	if err != nil {
		log.Fatal(err)
	}
	defer resp.Body.Close()

	receipt, err := mpp.ParseReceipt(resp.Header.Get("Payment-Receipt"))
	if err != nil {
		log.Fatal(err)
	}

	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		log.Fatal(err)
	}

	prettyBody, err := json.MarshalIndent(body, "", "  ")
	if err != nil {
		log.Fatal(err)
	}

	fmt.Printf("Status: %s\n", resp.Status)
	if receipt.Reference != "" {
		fmt.Printf("Receipt: %s\n", receipt.Reference)
	}
	if payer, ok := body["payer"].(string); ok && payer != "" {
		fmt.Printf("Payer: %s\n", payer)
	}
	if fortune, ok := body["fortune"].(string); ok && fortune != "" {
		fmt.Printf("Fortune: %s\n", fortune)
	} else {
		fmt.Printf("Response: %s\n", strings.TrimSpace(string(prettyBody)))
	}
}
