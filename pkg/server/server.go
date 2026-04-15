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

	"github.com/tempoxyz/mpp-go/pkg/mpp"
	"github.com/tempoxyz/mpp-go/pkg/tempo"
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

// ChargeRequestBuilder lets a method provide a canonical request shape for the
// generic charge helper. Tempo uses this to normalize human amounts and nest
// method-specific fields under methodDetails.
type ChargeRequestBuilder interface {
	BuildChargeRequest(params ChargeParams) (map[string]any, error)
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
	// Authorization is the incoming Authorization header value.
	Authorization string
	// Amount is the human-readable charge amount.
	Amount string
	// Currency overrides the method's default currency.
	Currency string
	// Recipient overrides the method's default recipient.
	Recipient string
	// ExternalID is copied into the request and echoed back in the Receipt.
	ExternalID string
	// Expires overrides the default Challenge expiry.
	Expires string
	// Description is exposed in the server-generated Challenge.
	Description string
	// Memo sets a Tempo transfer memo when the method supports it.
	Memo string
	// Splits adds Tempo split-payment transfers under methodDetails.splits.
	Splits []tempo.SplitParams
	// FeePayer requests the sponsored Tempo flow when the method supports it.
	FeePayer bool
	// FeePayerURL points at a remote fee-payer signer.
	FeePayerURL string
	// SupportedModes advertises the Tempo submission modes allowed for this challenge.
	SupportedModes []tempo.ChargeMode
	// ChainID overrides the method's default Tempo chain ID.
	ChainID int
	// Meta stores opaque Challenge metadata.
	Meta map[string]string
	// Deprecated: use Meta.
	// Extra is a deprecated alias for Meta.
	Extra map[string]string
}

// ChargeResult is either a Challenge or a verified (Credential, Receipt) pair.
type ChargeResult struct {
	// Challenge is returned when the client still needs to satisfy the payment.
	Challenge *mpp.Challenge
	// Credential is the verified client credential on success.
	Credential *mpp.Credential
	// Receipt acknowledges a successfully verified payment.
	Receipt *mpp.Receipt
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

	request := map[string]any{}
	if builder, ok := m.method.(ChargeRequestBuilder); ok {
		var err error
		request, err = builder.BuildChargeRequest(params)
		if err != nil {
			return nil, err
		}
	} else {
		request = map[string]any{
			"amount":   params.Amount,
			"currency": params.Currency,
		}
		if params.Recipient != "" {
			request["recipient"] = params.Recipient
		}
		if params.ExternalID != "" {
			request["externalId"] = params.ExternalID
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
	}

	meta := params.Meta
	if meta == nil {
		meta = params.Extra
	}

	result, err := VerifyOrChallenge(ctx, VerifyParams{
		Authorization: params.Authorization,
		Intent:        intent,
		Request:       request,
		Realm:         m.realm,
		SecretKey:     m.secretKey,
		Method:        m.method.Name(),
		Description:   params.Description,
		Meta:          meta,
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
