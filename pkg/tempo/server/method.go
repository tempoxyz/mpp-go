// Package chargeserver verifies Tempo charge Credentials and builds Tempo
// charge Challenges for MPP HTTP servers.
package chargeserver

import (
	"fmt"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/tempoxyz/mpp-go/pkg/mpp"
	mppserver "github.com/tempoxyz/mpp-go/pkg/server"
	"github.com/tempoxyz/mpp-go/pkg/tempo"
	tempotx "github.com/tempoxyz/tempo-go/pkg/transaction"
)

// methodConfig holds the request defaults New gives a Method; see Config for
// each field.
type methodConfig struct {
	Intent         *Intent
	Currency       string
	Currencies     []string
	Recipient      string
	Decimals       int
	ChainID        int64
	FeePayer       bool
	FeePayerURL    string
	SupportedModes []tempo.ChargeMode
}

// Method adapts Tempo charge configuration to the generic server interfaces.
type Method struct {
	intent               mppserver.Intent
	currencies           []string
	recipient            string
	decimals             int
	chainID              int64
	feePayer             bool
	feePayerURL          string
	supportedModes       []tempo.ChargeMode
	supportsUnknownChain bool
}

var _ mppserver.Method = (*Method)(nil)
var _ mppserver.ChargeRequestBuilder = (*Method)(nil)
var _ mppserver.ChargeOffersBuilder = (*Method)(nil)

func newMethod(config methodConfig) *Method {
	decimals := config.Decimals
	if decimals == 0 {
		decimals = tempo.DefaultDecimals
	}
	intent := config.Intent
	if intent == nil {
		panic("tempo server: Intent must be configured with a replay Store")
	}
	chainID := config.ChainID
	if chainID == 0 {
		chainID = tempo.InferChainIDFromRPCURL(intent.rpcURL)
		if intent.rpc == nil && intent.rpcURL == "" {
			chainID = tempotx.ChainIdMainnet
		}
	}
	currencies, err := resolveCurrencies(config.Currency, config.Currencies, chainID)
	if err != nil {
		panic(err.Error())
	}
	return &Method{
		intent:               intent,
		currencies:           currencies,
		recipient:            config.Recipient,
		decimals:             decimals,
		chainID:              chainID,
		feePayer:             config.FeePayer,
		feePayerURL:          config.FeePayerURL,
		supportedModes:       append([]tempo.ChargeMode(nil), config.SupportedModes...),
		supportsUnknownChain: intent.rpc != nil || intent.rpcURL != "",
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

// BuildChargeRequests returns one normalized Tempo charge request per accepted
// currency, in presentation order. A per-request ChargeParams.Currency
// overrides the configured currencies and yields a single request.
func (m *Method) BuildChargeRequests(params mppserver.ChargeParams) ([]map[string]any, error) {
	if params.Currency != "" || len(m.currencies) == 0 {
		request, err := m.BuildChargeRequest(params)
		if err != nil {
			return nil, err
		}
		return []map[string]any{request}, nil
	}
	requests := make([]map[string]any, 0, len(m.currencies))
	for _, currency := range m.currencies {
		offer := params
		offer.Currency = currency
		request, err := m.BuildChargeRequest(offer)
		if err != nil {
			return nil, err
		}
		requests = append(requests, request)
	}
	return requests, nil
}

// BuildChargeRequest normalizes server charge parameters into Tempo request
// data for a single currency: ChargeParams.Currency when set, otherwise the
// first accepted currency.
func (m *Method) BuildChargeRequest(params mppserver.ChargeParams) (map[string]any, error) {
	currency := params.Currency
	if currency == "" && len(m.currencies) > 0 {
		currency = m.currencies[0]
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
	if chainID != 0 && !tempo.IsKnownChainID(chainID) && !m.supportsUnknownChain {
		return nil, fmt.Errorf("tempo server: unknown chain id %d; configure Intent.RPC or Intent.RPCURL explicitly", chainID)
	}
	feePayerURL := params.FeePayerURL
	if feePayerURL == "" {
		feePayerURL = m.feePayerURL
	}
	feePayer := params.FeePayer || m.feePayer
	request, err := tempo.NormalizeChargeRequest(tempo.ChargeRequestParams{
		Amount:         params.Amount,
		Currency:       currency,
		Recipient:      recipient,
		Decimals:       m.decimals,
		Description:    params.Description,
		ExternalID:     params.ExternalID,
		ChainID:        chainID,
		FeePayer:       feePayer,
		FeePayerURL:    feePayerURL,
		Splits:         append([]tempo.SplitParams(nil), params.Splits...),
		SupportedModes: resolvedModes(params.SupportedModes, m.supportedModes),
	})
	if err != nil {
		return nil, err
	}
	// The floor applies only when issuing a challenge that needs a sponsored
	// transaction: zero-amount charges are paid with a proof, and a request
	// carrying a credential is verified against its echoed expiry instead.
	if feePayer && request.Amount != "0" && !carriesPaymentCredential(params) {
		if err := validateSponsoredChallengeExpiry(params.Expires, time.Now()); err != nil {
			return nil, err
		}
	}
	return request.Map(), nil
}

func carriesPaymentCredential(params mppserver.ChargeParams) bool {
	return mpp.FindPaymentAuthorization(params.Authorization) != "" ||
		mpp.FindPaymentAuthorization(params.PaymentAuthorization) != ""
}

// validateSponsoredChallengeExpiry rejects a sponsored charge whose challenge
// would expire less than tempo.FeePayerMinRemainingValidity after issuance.
// Clients refuse such challenges and the server would reject their sponsored
// transactions for too little remaining validity, so issuing one is a
// configuration error. An empty expires uses the 5 minute default.
func validateSponsoredChallengeExpiry(expires string, now time.Time) error {
	if expires == "" {
		return nil
	}
	expiry, err := parseExpires(expires)
	if err != nil {
		return fmt.Errorf("tempo server: invalid Expires %q: %w", expires, err)
	}
	if expiry.Sub(now) < tempo.FeePayerMinRemainingValidity {
		return fmt.Errorf("tempo server: sponsored charge challenges must expire at least %s after issuance, got Expires %q", tempo.FeePayerMinRemainingValidity, expires)
	}
	return nil
}

func resolvedModes(requestModes, defaultModes []tempo.ChargeMode) []tempo.ChargeMode {
	if len(requestModes) > 0 {
		return append([]tempo.ChargeMode(nil), requestModes...)
	}
	return append([]tempo.ChargeMode(nil), defaultModes...)
}

// resolveCurrencies returns the ordered accepted currencies for a method. The
// legacy single Currency is kept verbatim; an explicit Currencies list is
// validated and deduplicated case-insensitively; otherwise chain defaults apply.
func resolveCurrencies(currency string, currencies []string, chainID int64) ([]string, error) {
	if currency != "" && currencies != nil {
		return nil, fmt.Errorf("tempo server: specify either Currency or Currencies, not both")
	}
	if currency != "" {
		return []string{currency}, nil
	}
	if currencies == nil {
		return tempo.DefaultCurrenciesForChain(chainID), nil
	}
	seen := make(map[string]struct{}, len(currencies))
	resolved := make([]string, 0, len(currencies))
	for _, candidate := range currencies {
		if !isHexAddress(candidate) {
			return nil, fmt.Errorf("tempo server: invalid currency address %q", candidate)
		}
		key := strings.ToLower(candidate)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		resolved = append(resolved, candidate)
	}
	if len(resolved) == 0 {
		return nil, fmt.Errorf("tempo server: Currencies must contain at least one currency")
	}
	return resolved, nil
}

func isHexAddress(value string) bool {
	return (strings.HasPrefix(value, "0x") || strings.HasPrefix(value, "0X")) && common.IsHexAddress(value)
}
