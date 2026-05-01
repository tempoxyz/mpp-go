package chargeserver

import (
	"github.com/tempoxyz/mpp-go/pkg/tempo"
	temposigner "github.com/tempoxyz/tempo-go/pkg/signer"
)

// Config combines the method defaults and intent verification settings used by
// the high-level Tempo server constructor.
type Config struct {
	// Intent verifies Tempo charge credentials for this method.
	Intent *Intent
	// Currency is the default token contract address for issued challenges.
	Currency string
	// Recipient is the default payee address for issued challenges.
	Recipient string
	// Decimals controls how human-readable amounts are normalized.
	Decimals int
	// ChainID binds issued challenges to a specific Tempo chain when set.
	ChainID int64
	// FeePayer enables sponsored transaction flows by default.
	FeePayer bool
	// FeePayerURL points at a remote co-signer when the server does not sign locally.
	FeePayerURL string
	// Memo overrides the default attribution memo generation.
	Memo string
	// SupportedModes limits the credential submission modes advertised to clients.
	SupportedModes []tempo.ChargeMode
	// RPC overrides the Tempo JSON-RPC client used for verification.
	RPC tempo.RPCClient
	// RPCURL is used to build an RPC client when RPC is nil.
	RPCURL string
	// FeePayerSigner co-signs sponsored transactions locally when provided.
	FeePayerSigner *temposigner.Signer
	// FeePayerPrivateKey constructs FeePayerSigner when FeePayerSigner is nil.
	FeePayerPrivateKey string
	// FeePayerPrivateKeyEnv loads the fee-payer key from an environment variable when FeePayerPrivateKey is empty.
	FeePayerPrivateKeyEnv string
	// FeePayerPolicies allowlists the fee tokens this verifier will sponsor.
	FeePayerPolicies map[string]FeePayerPolicy
	// Store persists replay-protection keys for hash and proof credentials.
	Store tempo.Store
}

// MethodFromConfig constructs a Tempo charge method from one config struct.
func MethodFromConfig(config Config) (*Method, error) {
	methodConfig := MethodConfig{
		Intent:         config.Intent,
		Currency:       config.Currency,
		Recipient:      config.Recipient,
		Decimals:       config.Decimals,
		ChainID:        config.ChainID,
		FeePayer:       config.FeePayer,
		FeePayerURL:    config.FeePayerURL,
		Memo:           config.Memo,
		SupportedModes: append([]tempo.ChargeMode(nil), config.SupportedModes...),
	}
	if methodConfig.Intent == nil {
		intent, err := NewIntent(IntentConfig{
			RPC:                   config.RPC,
			RPCURL:                config.RPCURL,
			FeePayerSigner:        config.FeePayerSigner,
			FeePayerPrivateKey:    config.FeePayerPrivateKey,
			FeePayerPrivateKeyEnv: config.FeePayerPrivateKeyEnv,
			FeePayerPolicies:      config.FeePayerPolicies,
			Store:                 config.Store,
		})
		if err != nil {
			return nil, err
		}
		methodConfig.Intent = intent
	}
	return NewMethod(methodConfig), nil
}
