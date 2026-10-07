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
	// Currency restricts issued challenges to exactly this token contract
	// address. It cannot be combined with Currencies.
	//
	// Deprecated: Use Currencies with a single element.
	Currency string
	// Currencies lists the accepted token contract addresses in presentation
	// order; see MethodConfig.Currencies.
	Currencies []string
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
	// FeeToken fixes the fee token a local fee payer pays gas in; see IntentConfig.FeeToken.
	FeeToken string
	// Store is required to verify payments. Use a persistent store shared by all
	// replicas, with atomic PutIfAbsent and no eviction of replay keys.
	// An explicit tempo.NewMemoryStore() is only suitable for single-process development.
	Store tempo.Store
	// Relay delegates credential validation and finalization to Tempo API or a compatible MPP relay.
	Relay *RelayConfig
}

// MethodFromConfig constructs a Tempo charge method from one config struct.
// It returns an error when the currency configuration is invalid.
func MethodFromConfig(config Config) (*Method, error) {
	if _, err := resolveCurrencies(config.Currency, config.Currencies, config.ChainID); err != nil {
		return nil, err
	}
	methodConfig := MethodConfig{
		Intent:         config.Intent,
		Currency:       config.Currency,
		Currencies:     config.Currencies,
		Recipient:      config.Recipient,
		Decimals:       config.Decimals,
		ChainID:        config.ChainID,
		FeePayer:       config.FeePayer,
		FeePayerURL:    config.FeePayerURL,
		SupportedModes: append([]tempo.ChargeMode(nil), config.SupportedModes...),
	}
	if methodConfig.Intent == nil {
		buildIntent := NewIntent
		if config.Relay != nil {
			buildIntent = newIntent
		}
		intent, err := buildIntent(IntentConfig{
			RPC:                   config.RPC,
			RPCURL:                config.RPCURL,
			FeePayerSigner:        config.FeePayerSigner,
			FeePayerPrivateKey:    config.FeePayerPrivateKey,
			FeePayerPrivateKeyEnv: config.FeePayerPrivateKeyEnv,
			FeePayerPolicies:      config.FeePayerPolicies,
			FeeToken:              config.FeeToken,
			Store:                 config.Store,
		})
		if err != nil {
			return nil, err
		}
		methodConfig.Intent = intent
	}
	if config.Relay != nil {
		relay, err := newRelayClient(config.Relay)
		if err != nil {
			return nil, err
		}
		method := NewMethod(methodConfig)
		intent, err := relay.intent(method.intent.Name())
		if err != nil {
			return nil, err
		}
		method.intent = intent
		method.supportsUnknownChain = true
		return method, nil
	}
	return NewMethod(methodConfig), nil
}
