package chargeserver

import (
	"fmt"

	"github.com/tempoxyz/mpp-go/pkg/tempo"
	temposigner "github.com/tempoxyz/tempo-go/pkg/signer"
)

// Config combines the method defaults and intent verification settings used by
// New, the Tempo server constructor.
type Config struct {
	// Intent verifies Tempo charge credentials for this method. When nil, New
	// builds one from the verification fields below. It cannot be combined
	// with Relay.
	Intent *Intent
	// Currencies lists the accepted token contract addresses in presentation
	// order. The server issues one charge Challenge per currency, and a
	// Credential for any of them verifies. An explicit list replaces the chain
	// defaults from tempo.DefaultCurrenciesForChain; it must contain at least
	// one valid 0x-prefixed address, and case-insensitive duplicates are
	// dropped.
	Currencies []string
	// Recipient is the default payee address for issued challenges.
	Recipient string
	// Decimals controls how human-readable amounts are normalized.
	Decimals int
	// ChainID binds issued challenges to a specific Tempo chain when set, such
	// as tempo.ChainIDMainnet or tempo.ChainIDModerato.
	ChainID int64
	// FeePayer requests sponsorship on every route. A method with a fee payer
	// (FeePayerSigner, FeePayerPrivateKey, FeePayerURL or HostedFeePayer)
	// sponsors every route anyway, unless a route sets
	// ChargeParams.DisableFeePayer, as in mppx; New rejects FeePayer without
	// a fee payer, except with a Relay, which sponsors charges itself.
	FeePayer bool
	// FeePayerURL sponsors charges through a hosted fee-payer service at this
	// JSON-RPC URL (shorthand for HostedFeePayer{URL: FeePayerURL}). The
	// server asks it to co-sign with eth_fillTransaction, then simulates and
	// broadcasts the transaction itself; challenges no longer carry it.
	FeePayerURL string
	// HostedFeePayer sponsors charges through a hosted fee-payer service with
	// request headers; see HostedFeePayer.
	HostedFeePayer *HostedFeePayer
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
	// AllowedFeeTokens lists the fee tokens this verifier will sponsor; see
	// IntentConfig.AllowedFeeTokens.
	AllowedFeeTokens []string
	// FeePayerPolicy sets the sponsorship limits for every allowed fee token;
	// see IntentConfig.FeePayerPolicy.
	FeePayerPolicy *FeePayerPolicy
	// FeeToken fixes the fee token a local fee payer pays gas in; see IntentConfig.FeeToken.
	FeeToken string
	// Store is required to verify payments. Use a persistent store shared by all
	// replicas, with atomic PutIfAbsent and no eviction of replay keys.
	// An explicit tempo.NewMemoryStore() is only suitable for single-process development.
	Store tempo.Store
	// ExpireReplayKeys lets replay keys expire after their challenge; see
	// IntentConfig.ExpireReplayKeys for when that is safe.
	ExpireReplayKeys bool
	// Relay delegates credential validation and finalization to Tempo API or
	// a compatible MPP relay. It cannot be combined with Intent.
	Relay *RelayConfig
}

// New constructs a Tempo charge method from one config struct. It returns an
// error when the configuration is invalid: a missing replay Store (without
// Relay), an invalid currency or fee-payer configuration, or Intent combined
// with Relay.
//
// Without Currencies, the method accepts
// tempo.DefaultCurrenciesForChain for the configured chain, or for the chain
// inferred from RPCURL when ChainID is zero. With no custom RPC, it defaults
// to mainnet.
func New(config Config) (*Method, error) {
	if config.Intent != nil && config.Relay != nil {
		return nil, fmt.Errorf("tempo server: Config.Intent cannot be combined with Relay; the relay verifies credentials")
	}
	if _, err := resolveCurrencies(config.Currencies, config.ChainID); err != nil {
		return nil, err
	}
	methodConfig := methodConfig{
		Intent:         config.Intent,
		Currencies:     config.Currencies,
		Recipient:      config.Recipient,
		Decimals:       config.Decimals,
		ChainID:        config.ChainID,
		FeePayer:       config.FeePayer,
		SupportedModes: append([]tempo.ChargeMode(nil), config.SupportedModes...),
	}
	if config.Intent != nil && (config.FeePayerURL != "" || config.HostedFeePayer != nil) {
		return nil, fmt.Errorf("tempo server: configure FeePayerURL or HostedFeePayer on the IntentConfig of Config.Intent")
	}
	if methodConfig.Intent == nil {
		buildIntent := NewIntent
		if config.Relay != nil {
			buildIntent = newIntent
		}
		intent, err := buildIntent(IntentConfig{
			RPC:                config.RPC,
			RPCURL:             config.RPCURL,
			FeePayerSigner:     config.FeePayerSigner,
			FeePayerPrivateKey: config.FeePayerPrivateKey,
			AllowedFeeTokens:   config.AllowedFeeTokens,
			FeePayerPolicy:     config.FeePayerPolicy,
			FeeToken:           config.FeeToken,
			FeePayerURL:        config.FeePayerURL,
			HostedFeePayer:     config.HostedFeePayer,
			Store:              config.Store,
			ExpireReplayKeys:   config.ExpireReplayKeys,
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
		method := newMethod(methodConfig)
		intent, err := relay.intent(method.intent.Name())
		if err != nil {
			return nil, err
		}
		method.intent = intent
		method.supportsUnknownChain = true
		method.relay = true
		return method, nil
	}
	if config.FeePayer && !methodConfig.Intent.sponsors() {
		return nil, fmt.Errorf("tempo server: FeePayer requires a fee payer; configure FeePayerSigner, FeePayerPrivateKey, FeePayerURL or HostedFeePayer")
	}
	if !hasReplayStore(methodConfig.Intent.store) {
		return nil, fmt.Errorf("tempo server: a replay Store is required")
	}
	return newMethod(methodConfig), nil
}
