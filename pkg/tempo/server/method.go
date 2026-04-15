package server

import (
	"fmt"

	genericserver "github.com/tempoxyz/mpp-go/pkg/server"
	"github.com/tempoxyz/mpp-go/pkg/tempo"
)

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

var _ genericserver.Method = (*Method)(nil)
var _ genericserver.ChargeRequestBuilder = (*Method)(nil)

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

func (m *Method) Name() string {
	return tempo.MethodName
}

func (m *Method) Intents() map[string]genericserver.Intent {
	return map[string]genericserver.Intent{tempo.IntentCharge: m.intent}
}

func (m *Method) BuildChargeRequest(params genericserver.ChargeParams) (map[string]any, error) {
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
