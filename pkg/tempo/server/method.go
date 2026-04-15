// Package temposerver verifies Tempo charge Credentials and builds Tempo
// charge Challenges for MPP HTTP servers.
package temposerver

import (
	"fmt"

	mppserver "github.com/tempoxyz/mpp-go/pkg/server"
	"github.com/tempoxyz/mpp-go/pkg/tempo"
)

// MethodConfig configures a Tempo payment method for server-side charging.
type MethodConfig struct {
	Intent         *ChargeIntent
	Currency       string
	Recipient      string
	Decimals       int
	ChainID        int64
	FeePayer       bool
	FeePayerURL    string
	Memo           string
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
		SupportedModes: append([]tempo.ChargeMode(nil), m.supportedModes...),
	})
	if err != nil {
		return nil, err
	}
	return request.Map(), nil
}
