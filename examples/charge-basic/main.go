package main

import (
	"context"
	"fmt"
	"log"

	"github.com/tempoxyz/mpp-go/examples/internal/devnet"
	"github.com/tempoxyz/mpp-go/pkg/tempo"
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
	payer, err := temposigner.NewSigner(devnet.PayerPrivateKey)
	if err != nil {
		log.Fatal(err)
	}
	if err := devnet.FundAddress(ctx, rpc, payer.Address()); err != nil {
		log.Fatal(err)
	}

	api, err := startServer(rpcURL, chainID)
	if err != nil {
		log.Fatal(err)
	}
	defer api.Close()

	result, err := runClient(ctx, api.url, rpcURL, chainID)
	if err != nil {
		log.Fatal(err)
	}

	fmt.Printf("paid request for %q from %s with receipt %s\n", result.Data, result.Payer, result.Receipt)
}
