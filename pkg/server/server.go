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

// Validation describes a credential accepted without consuming or broadcasting it.
type Validation struct {
	// Challenge is the server-issued challenge echoed by the credential.
	Challenge mpp.ChallengeEcho
	// Credential is the accepted credential.
	Credential *mpp.Credential
	// Details contains method-specific validation metadata.
	Details map[string]any
	// Intent is the validated payment intent.
	Intent string
	// Method is the validated payment method.
	Method string
	// Request is the challenge request bound to the credential.
	Request map[string]any
	// Source is the optional payer identity.
	Source string
}

// VerifyFunc is the legacy combined credential verification hook.
type VerifyFunc func(
	ctx context.Context,
	credential *mpp.Credential,
	request map[string]any,
) (*mpp.Receipt, error)

// ValidateFunc performs an advisory credential check without mutating payment state.
type ValidateFunc func(
	ctx context.Context,
	credential *mpp.Credential,
	request map[string]any,
) (*Validation, error)

// BroadcastFunc finalizes a validated credential and may mutate payment state.
type BroadcastFunc func(
	ctx context.Context,
	credential *mpp.Credential,
	request map[string]any,
) (*mpp.Receipt, error)

// IntentHooks configures either a legacy Verify hook or split Validate and Broadcast hooks.
type IntentHooks struct {
	// Verify configures the legacy combined verification path.
	Verify VerifyFunc
	// Validate configures the advisory phase of a split verification path.
	Validate ValidateFunc
	// Broadcast configures the settlement phase of a split verification path.
	Broadcast BroadcastFunc
}

type verifyHookIntent struct {
	name   string
	verify VerifyFunc
}

type splitHookIntent struct {
	name      string
	validate  ValidateFunc
	broadcast BroadcastFunc
}

// NewIntent builds an Intent from either Verify or Validate and Broadcast hooks.
func NewIntent(name string, hooks IntentHooks) (Intent, error) {
	if name == "" {
		return nil, fmt.Errorf("server: intent name is required")
	}
	if hooks.Verify != nil && hooks.Validate == nil && hooks.Broadcast == nil {
		return &verifyHookIntent{name: name, verify: hooks.Verify}, nil
	}
	if hooks.Verify == nil && hooks.Validate != nil && hooks.Broadcast != nil {
		return &splitHookIntent{name: name, validate: hooks.Validate, broadcast: hooks.Broadcast}, nil
	}
	return nil, fmt.Errorf("server: intent hooks require either Verify or both Validate and Broadcast")
}

func (i *verifyHookIntent) Name() string { return i.name }

func (i *verifyHookIntent) Verify(
	ctx context.Context,
	credential *mpp.Credential,
	request map[string]any,
) (*mpp.Receipt, error) {
	return i.verify(ctx, credential, request)
}

func (i *splitHookIntent) Name() string { return i.name }

func (i *splitHookIntent) Verify(
	ctx context.Context,
	credential *mpp.Credential,
	request map[string]any,
) (*mpp.Receipt, error) {
	if _, err := i.validate(ctx, credential, request); err != nil {
		return nil, err
	}
	return i.broadcast(ctx, credential, request)
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

// minimumSecretKeyBytes is the shortest secret key accepted for HMAC-bound
// challenge IDs. It matches the canonical mppx reference (assertSecretKey).
const minimumSecretKeyBytes = 32

// Mpp is the server-side payment handler.
type Mpp struct {
	method    Method
	realm     string
	secretKey string
}

// New creates an Mpp instance. It returns an error when secretKey is shorter
// than minimumSecretKeyBytes, since a weak secret yields forgeable,
// HMAC-bound challenge IDs.
func New(method Method, realm, secretKey string) (*Mpp, error) {
	if len(secretKey) < minimumSecretKeyBytes {
		return nil, fmt.Errorf("server: secret key must be at least %d bytes", minimumSecretKeyBytes)
	}
	return &Mpp{
		method:    method,
		realm:     realm,
		secretKey: secretKey,
	}, nil
}

// Realm returns the WWW-Authenticate realm used by this payment handler.
func (m *Mpp) Realm() string {
	return m.realm
}

// ValidateCredential validates an issued credential without consuming or broadcasting it.
func (m *Mpp) ValidateCredential(ctx context.Context, credential *mpp.Credential) (*Validation, error) {
	intent, request, err := m.prepareCredential(credential)
	if err != nil {
		return nil, err
	}
	return validateCredential(ctx, intent, credential, request, m.method.Name())
}

// BroadcastCredential validates and settles an issued credential.
func (m *Mpp) BroadcastCredential(ctx context.Context, credential *mpp.Credential) (*mpp.Receipt, error) {
	intent, request, err := m.prepareCredential(credential)
	if err != nil {
		return nil, err
	}
	return intent.Verify(ctx, credential, request)
}

// VerifyCredential is a backwards-compatible alias for BroadcastCredential.
func (m *Mpp) VerifyCredential(ctx context.Context, credential *mpp.Credential) (*mpp.Receipt, error) {
	return m.BroadcastCredential(ctx, credential)
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
	// Body is the actual request body to bind via the challenge digest.
	Body any
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
	// MppxScope binds framework route/resource/query context into the request.
	MppxScope map[string]string
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

// buildChargeRequest produces the canonical request map for a charge operation.
func (m *Mpp) buildChargeRequest(params ChargeParams) (map[string]any, error) {
	if builder, ok := m.method.(ChargeRequestBuilder); ok {
		request, err := builder.BuildChargeRequest(params)
		if err != nil {
			return nil, err
		}
		applyMppxScope(request, params.MppxScope)
		return request, nil
	}
	request := map[string]any{
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
	applyMppxScope(request, params.MppxScope)
	return request, nil
}

func applyMppxScope(request map[string]any, scope map[string]string) {
	if len(scope) == 0 {
		return
	}
	request["_mppx_scope"] = scope
}

// Charge handles a charge intent with human-readable amounts.
func (m *Mpp) Charge(ctx context.Context, params ChargeParams) (*ChargeResult, error) {
	intent, ok := m.method.Intents()["charge"]
	if !ok {
		return nil, fmt.Errorf("method %q does not support charge intent", m.method.Name())
	}

	request, err := m.buildChargeRequest(params)
	if err != nil {
		return nil, err
	}

	result, err := VerifyOrChallenge(ctx, VerifyParams{
		Authorization: params.Authorization,
		Intent:        intent,
		Request:       request,
		Body:          params.Body,
		Realm:         m.realm,
		SecretKey:     m.secretKey,
		Method:        m.method.Name(),
		Description:   params.Description,
		Meta:          params.Meta,
		Expires:       params.Expires,
	})
	if err != nil {
		if result != nil {
			return &ChargeResult{
				Challenge:  result.Challenge,
				Credential: result.Credential,
				Receipt:    result.Receipt,
			}, err
		}
		return nil, err
	}

	return &ChargeResult{
		Challenge:  result.Challenge,
		Credential: result.Credential,
		Receipt:    result.Receipt,
	}, nil
}
