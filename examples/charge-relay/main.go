package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/tempoxyz/mpp-go/examples/internal/devnet"
	"github.com/tempoxyz/mpp-go/pkg/tempo"
	tempotx "github.com/tempoxyz/tempo-go/pkg/transaction"
)

const relayRealm = "mpp-go-relay.example.com"

func main() {
	if err := run(context.Background()); err != nil {
		log.Fatal(err)
	}
}

func run(ctx context.Context) error {
	apiKey := os.Getenv("TEMPO_API_KEY")
	if apiKey == "" {
		return fmt.Errorf("set TEMPO_API_KEY to a Tempo API key with the mpp:write scope")
	}
	apiURL := os.Getenv("TEMPO_API_URL")
	if apiURL == "" {
		apiURL = "https://api.tempo.xyz"
	}
	rpcURL := os.Getenv("TEMPO_RPC_URL")
	if rpcURL == "" {
		rpcURL = tempotx.RpcUrlModerato
	}
	secretKey := os.Getenv("MPP_SECRET_KEY")
	if secretKey == "" {
		secretKey = "mpp-go-demo-tempo-api-relay-secret-key"
	}

	rpc := tempo.NewRPCClient(rpcURL)
	chainID, err := rpc.GetChainID(ctx)
	if err != nil {
		return fmt.Errorf("get Moderato chain ID: %w", err)
	}
	if int64(chainID) != tempotx.ChainIdModerato {
		return fmt.Errorf("relay example requires Moderato chain ID %d, got %d", tempotx.ChainIdModerato, chainID)
	}

	api, err := startRelayServer(apiKey, apiURL, secretKey, int64(chainID))
	if err != nil {
		return err
	}
	defer api.Close()

	payerKey, err := crypto.GenerateKey()
	if err != nil {
		return fmt.Errorf("generate payer: %w", err)
	}
	payerPrivateKey := hexutil.Encode(crypto.FromECDSA(payerKey))
	payer := crypto.PubkeyToAddress(payerKey.PublicKey)
	if err := devnet.FundAddress(ctx, rpc, payer); err != nil {
		return fmt.Errorf("fund Moderato payer: %w", err)
	}
	result, err := runRelayClient(ctx, api.URL, rpcURL, int64(chainID), payerPrivateKey, api.payment)
	if err != nil {
		return err
	}

	fmt.Printf("validated and paid %s through the Tempo relay from %s; receipt %s\n", result.URL, payer.Hex(), result.Receipt)
	return nil
}
