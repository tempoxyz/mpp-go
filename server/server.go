// Package server provides the server-side MPP payment handler.
//
// It implements the HTTP 402 challenge/credential flow: when a request
// arrives without payment credentials, it returns a 402 with a
// WWW-Authenticate challenge. When valid credentials are provided,
// it verifies them and returns a receipt.
package server

import (
	"context"
	"fmt"

	"github.com/tempoxyz/mpp-go/mpp"
)

// Intent is the interface for payment intents (server-side verification).
type Intent interface {
	Name() string
	Verify(ctx context.Context, credential *mpp.Credential, request map[string]any) (*mpp.Receipt, error)
}

// Method is the interface for server-side payment methods.
type Method interface {
	Name() string
	Intents() map[string]Intent
}

// Mpp is the server-side payment handler.
type Mpp struct {
	method    Method
	realm     string
	secretKey string
}

// New creates an Mpp instance.
func New(method Method, realm, secretKey string) *Mpp {
	return &Mpp{
		method:    method,
		realm:     realm,
		secretKey: secretKey,
	}
}

// ChargeParams contains the parameters for a charge operation.
type ChargeParams struct {
	Authorization string
	Amount        string
	Currency      string
	Recipient     string
	Expires       string
	Description   string
	Memo          string
	FeePayer      bool
	ChainID       int
	Extra         map[string]string
}

// ChargeResult is either a Challenge or a verified (Credential, Receipt) pair.
type ChargeResult struct {
	Challenge  *mpp.Challenge
	Credential *mpp.Credential
	Receipt    *mpp.Receipt
}

// IsChallenge returns true if the result is a 402 challenge.
func (r *ChargeResult) IsChallenge() bool {
	return r.Challenge != nil
}

// Charge handles a charge intent with human-readable amounts.
func (m *Mpp) Charge(ctx context.Context, params ChargeParams) (*ChargeResult, error) {
	intent, ok := m.method.Intents()["charge"]
	if !ok {
		return nil, fmt.Errorf("method %q does not support charge intent", m.method.Name())
	}

	request := map[string]any{
		"amount":   params.Amount,
		"currency": params.Currency,
	}
	if params.Recipient != "" {
		request["recipient"] = params.Recipient
	}
	if params.FeePayer {
		request["feePayer"] = true
	}
	if params.ChainID != 0 {
		request["chainId"] = params.ChainID
	}
	if params.Memo != "" {
		request["memo"] = params.Memo
	}
	for k, v := range params.Extra {
		request[k] = v
	}

	result, err := VerifyOrChallenge(ctx, VerifyParams{
		Authorization: params.Authorization,
		Intent:        intent,
		Request:       request,
		Realm:         m.realm,
		SecretKey:     m.secretKey,
		Method:        m.method.Name(),
		Description:   params.Description,
		Meta:          params.Extra,
		Expires:       params.Expires,
	})
	if err != nil {
		return nil, err
	}

	return &ChargeResult{
		Challenge:  result.Challenge,
		Credential: result.Credential,
		Receipt:    result.Receipt,
	}, nil
}
