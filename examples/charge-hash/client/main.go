package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"

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

	method, err := charge.New(charge.Config{ChainID: chainID, CredentialType: tempo.CredentialTypeHash, PrivateKey: privateKey, RPCURL: rpcURL})
	if err != nil {
		log.Fatal(err)
	}
	c := client.New([]client.Method{method})

	baseURL := os.Getenv("BASE_URL")
	if baseURL == "" {
		baseURL = "http://localhost:3000"
	}
	response, err := c.Get(ctx, baseURL+"/paid")
	if err != nil {
		log.Fatal(err)
	}
	defer response.Body.Close()

	receipt, err := mpp.ParsePaymentReceipt(response.Header.Get("Payment-Receipt"))
	if err != nil {
		log.Fatal(err)
	}
	var body struct {
		Tx string `json:"tx"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		log.Fatal(err)
	}

	fmt.Printf("broadcast hash credential and received receipt %s (body tx %s)\n", receipt.Reference, body.Tx)
}
