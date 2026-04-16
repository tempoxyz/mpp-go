package main

import (
	"context"
	"encoding/json"

	"github.com/tempoxyz/mpp-go/examples/internal/devnet"
	"github.com/tempoxyz/mpp-go/pkg/client"
	"github.com/tempoxyz/mpp-go/pkg/mpp"
	"github.com/tempoxyz/mpp-go/pkg/tempo"
	charge "github.com/tempoxyz/mpp-go/pkg/tempo/client"
)

type clientResult struct {
	Receipt string
	Tx      string
}

func runClient(ctx context.Context, url, rpcURL string, chainID int64) (*clientResult, error) {
	method, err := charge.New(charge.Config{
		ChainID:        chainID,
		CredentialType: tempo.CredentialTypeHash,
		PrivateKey:     devnet.PayerPrivateKey,
		RPCURL:         rpcURL,
	})
	if err != nil {
		return nil, err
	}
	c := client.New([]client.Method{method})

	response, err := c.Get(ctx, url+"/paid")
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()

	receipt, err := mpp.ParseReceipt(response.Header.Get("Payment-Receipt"))
	if err != nil {
		return nil, err
	}

	var body struct {
		Tx string `json:"tx"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		return nil, err
	}

	return &clientResult{Receipt: receipt.Reference, Tx: body.Tx}, nil
}
