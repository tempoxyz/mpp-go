package main

import (
	"context"
	"encoding/json"

	"github.com/tempoxyz/mpp-go/examples/internal/devnet"
	mppclient "github.com/tempoxyz/mpp-go/pkg/client"
	"github.com/tempoxyz/mpp-go/pkg/mpp"
	charge "github.com/tempoxyz/mpp-go/pkg/tempo/client"
)

type clientResult struct {
	Data    string
	Payer   string
	Receipt string
}

func runClient(ctx context.Context, url, rpcURL string, chainID int64) (*clientResult, error) {
	method, err := charge.New(charge.Config{
		ChainID:    chainID,
		PrivateKey: devnet.PayerPrivateKey,
		RPCURL:     rpcURL,
	})
	if err != nil {
		return nil, err
	}
	client := mppclient.New([]mppclient.Method{method})

	response, err := client.Get(ctx, url+"/paid")
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()

	receipt, err := mpp.ParsePaymentReceipt(response.Header.Get("Payment-Receipt"))
	if err != nil {
		return nil, err
	}

	var body struct {
		Data  string `json:"data"`
		Payer string `json:"payer"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		return nil, err
	}

	return &clientResult{Data: body.Data, Payer: body.Payer, Receipt: receipt.Reference}, nil
}
