// Package chargeserver verifies Tempo charge Credentials and builds Tempo
// charge Challenges for MPP HTTP servers.
package chargeserver

import (
	"fmt"

	mppserver "github.com/tempoxyz/mpp-go/pkg/server"
	"github.com/tempoxyz/mpp-go/pkg/tempo"
)

// MethodConfig configures a Tempo payment method for server-side charging.
type MethodConfig struct {
	// Intent verifies Tempo charge credentials for this method.
	Intent *ChargeIntent
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
}

// Method adapts Tempo charge configuration to the generic server interfaces.
type Method struct {
	intent         *ChargeIntent
	currency       string
	recipient      string
	decimals       int
	chainID        int64
	feePayer       bool
	feePayerURL    string
	memo           string
	supportedModes []tempo.ChargeMode
}

var _ mppserver.Method = (*Method)(nil)
var _ mppserver.ChargeRequestBuilder = (*Method)(nil)

// NewMethod builds a Tempo server method with request defaults.
func NewMethod(config MethodConfig) *Method {
	decimals := config.Decimals
	if decimals == 0 {
		decimals = tempo.DefaultDecimals
	}
	chainID := config.ChainID
	currency := config.Currency
	if currency == "" {
		currency = tempo.DefaultCurrencyForChain(chainID)
	}
	intent := config.Intent
	if intent == nil {
		intent, _ = NewChargeIntent(ChargeIntentConfig{})
	}
	return &Method{
		intent:         intent,
		currency:       currency,
		recipient:      config.Recipient,
		decimals:       decimals,
		chainID:        chainID,
		feePayer:       config.FeePayer,
		feePayerURL:    config.FeePayerURL,
		memo:           config.Memo,
		supportedModes: append([]tempo.ChargeMode(nil), config.SupportedModes...),
	}
}

// Name returns the method token used in Challenges and Credentials.
func (m *Method) Name() string {
	return tempo.MethodName
}

// Intents exposes the Tempo intents handled by this method.
func (m *Method) Intents() map[string]mppserver.Intent {
	return map[string]mppserver.Intent{tempo.IntentCharge: m.intent}
}

// BuildChargeRequest normalizes server charge parameters into Tempo request data.
func (m *Method) BuildChargeRequest(params mppserver.ChargeParams) (map[string]any, error) {
	currency := params.Currency
	if currency == "" {
		currency = m.currency
	}
	if currency == "" {
		return nil, fmt.Errorf("tempo server: currency must be configured on the method or the request")
	}
	recipient := params.Recipient
	if recipient == "" {
		recipient = m.recipient
	}
	if recipient == "" {
		return nil, fmt.Errorf("tempo server: recipient must be configured on the method or the request")
	}
	chainID := int64(params.ChainID)
	if chainID == 0 {
		chainID = m.chainID
	}
	memo := params.Memo
	if memo == "" {
		memo = m.memo
	}
	feePayerURL := params.FeePayerURL
	if feePayerURL == "" {
		feePayerURL = m.feePayerURL
	}
	request, err := tempo.NormalizeChargeRequest(tempo.ChargeRequestParams{
		Amount:         params.Amount,
		Currency:       currency,
		Recipient:      recipient,
		Decimals:       m.decimals,
		Description:    params.Description,
		ExternalID:     params.ExternalID,
		ChainID:        chainID,
		FeePayer:       params.FeePayer || m.feePayer,
		FeePayerURL:    feePayerURL,
		Memo:           memo,
		Splits:         append([]tempo.SplitParams(nil), params.Splits...),
		SupportedModes: resolvedModes(params.SupportedModes, m.supportedModes),
	})
	if err != nil {
		return nil, err
	}
	return request.Map(), nil
}

func resolvedModes(requestModes, defaultModes []tempo.ChargeMode) []tempo.ChargeMode {
	if len(requestModes) > 0 {
		return append([]tempo.ChargeMode(nil), requestModes...)
	}
	return append([]tempo.ChargeMode(nil), defaultModes...)
}
