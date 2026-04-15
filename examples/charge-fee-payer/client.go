package main

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/tempoxyz/mpp-go/examples/internal/devnet"
	"github.com/tempoxyz/mpp-go/pkg/client"
	"github.com/tempoxyz/mpp-go/pkg/mpp"
	charge "github.com/tempoxyz/mpp-go/pkg/tempo/client"
)

type clientResult struct {
	Receipt string
	Tx      string
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
	cl := client.New([]client.Method{method})

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url+"/paid", nil)
	if err != nil {
		return nil, err
	}
	response, err := cl.Do(req)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()

	receipt, err := mpp.ParsePaymentReceipt(response.Header.Get("Payment-Receipt"))
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
