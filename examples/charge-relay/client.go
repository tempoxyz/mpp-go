package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/tempoxyz/mpp-go/pkg/mpp"
	"github.com/tempoxyz/mpp-go/pkg/server"
	charge "github.com/tempoxyz/mpp-go/pkg/tempo/client"
)

type relayClientResult struct {
	Receipt string
	URL     string
}

func runRelayClient(
	ctx context.Context,
	apiURL string,
	rpcURL string,
	chainID int64,
	privateKey string,
	payment *server.Mpp,
) (*relayClientResult, error) {
	method, err := charge.New(charge.Config{
		ChainID:    chainID,
		PrivateKey: privateKey,
		RPCURL:     rpcURL,
	})
	if err != nil {
		return nil, fmt.Errorf("configure payer: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL+"/api/photo", nil)
	if err != nil {
		return nil, err
	}
	challengeResponse, err := http.DefaultClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("request photo challenge: %w", err)
	}
	challengeResponse.Body.Close()
	if challengeResponse.StatusCode != http.StatusPaymentRequired {
		return nil, fmt.Errorf("challenge route returned HTTP %d", challengeResponse.StatusCode)
	}
	challenge, err := mpp.ParseChallenge(challengeResponse.Header.Get("WWW-Authenticate"))
	if err != nil {
		return nil, fmt.Errorf("parse payment challenge: %w", err)
	}
	credential, err := method.CreateCredential(ctx, challenge)
	if err != nil {
		return nil, fmt.Errorf("create payment credential: %w", err)
	}
	if _, err := payment.ValidateCredential(ctx, credential); err != nil {
		return nil, fmt.Errorf("validate payment credential: %w", err)
	}

	request, err = http.NewRequestWithContext(ctx, http.MethodGet, apiURL+"/api/photo", nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", credential.ToAuthorization())
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("pay for photo: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("paid route returned HTTP %d", response.StatusCode)
	}

	receipt, err := mpp.ParseReceipt(response.Header.Get("Payment-Receipt"))
	if err != nil {
		return nil, fmt.Errorf("parse payment receipt: %w", err)
	}
	var body struct {
		URL string `json:"url"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("decode paid response: %w", err)
	}
	return &relayClientResult{Receipt: receipt.Reference, URL: body.URL}, nil
}
