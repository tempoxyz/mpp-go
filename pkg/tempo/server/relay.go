package chargeserver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/tempoxyz/mpp-go/pkg/mpp"
	mppserver "github.com/tempoxyz/mpp-go/pkg/server"
	"github.com/tempoxyz/mpp-go/pkg/tempo"
)

const defaultRelayAPIBaseURL = "https://api.tempo.xyz"

const maxRelayResponseSize = 1 << 20

// defaultRelayTimeout bounds relay calls when RelayConfig.HTTPClient is nil,
// so a stalled relay cannot hold a request open indefinitely.
const defaultRelayTimeout = 30 * time.Second

// RelayErrorCode is a stable machine-readable relay failure code.
type RelayErrorCode string

const (
	// RelayErrorAlreadyUsed means the credential or transaction was already used.
	RelayErrorAlreadyUsed RelayErrorCode = "already_used"
	// RelayErrorBroadcastFailed means transaction broadcast or confirmation failed.
	RelayErrorBroadcastFailed RelayErrorCode = "broadcast_failed"
	// RelayErrorExpired means the challenge or payment authorization expired.
	RelayErrorExpired RelayErrorCode = "expired"
	// RelayErrorInvalidPayment means the payment payload or requirements are invalid.
	RelayErrorInvalidPayment RelayErrorCode = "invalid_payment"
	// RelayErrorInsufficientFunds means the sender lacks funds.
	RelayErrorInsufficientFunds RelayErrorCode = "insufficient_funds"
	// RelayErrorPolicyDenied means the payment violates relay or account policy.
	RelayErrorPolicyDenied RelayErrorCode = "policy_denied"
	// RelayErrorScreenRejected means an originator or recipient failed screening.
	RelayErrorScreenRejected RelayErrorCode = "screen_rejected"
	// RelayErrorSimulationFailed means transaction simulation failed.
	RelayErrorSimulationFailed RelayErrorCode = "simulation_failed"
	// RelayErrorTemporarilyUnavailable means retrying may succeed.
	RelayErrorTemporarilyUnavailable RelayErrorCode = "temporarily_unavailable"
	// RelayErrorUnsupported means the method, intent, network, or asset is unsupported.
	RelayErrorUnsupported RelayErrorCode = "unsupported"
	// RelayErrorUnknown means the relay encountered an unexpected failure.
	RelayErrorUnknown RelayErrorCode = "unknown"
)

// RelayConfig configures Tempo API or a compatible MPP relay.
type RelayConfig struct {
	// APIKey is a Tempo API key with the mpp:write scope.
	APIKey string
	// APIBaseURL is the relay base URL, including an optional path prefix.
	// It defaults to https://api.tempo.xyz.
	APIBaseURL string
	// HTTPClient overrides the client used for relay calls. When nil, relay
	// calls use a client with a 30 second timeout.
	HTTPClient *http.Client
}

type relayClient struct {
	apiKey       string
	httpClient   *http.Client
	validateURL  string
	broadcastURL string
}

func (r *relayClient) intent(name string) (mppserver.Intent, error) {
	return mppserver.NewIntent(name, mppserver.IntentHooks{
		Validate: func(
			ctx context.Context,
			credential *mpp.Credential,
			_ map[string]any,
		) (*mppserver.Validation, error) {
			if credential == nil {
				return nil, mpp.ErrMalformedCredential("credential is required")
			}
			request, err := relayCredentialRequest(credential)
			if err != nil {
				return nil, mpp.ErrMalformedCredential("invalid echoed request")
			}
			return r.validate(ctx, credential, request, tempo.MethodName, name)
		},
		Broadcast: func(
			ctx context.Context,
			credential *mpp.Credential,
			_ map[string]any,
		) (*mpp.Receipt, error) {
			return r.broadcast(ctx, credential)
		},
	})
}

type relayError struct {
	Code    RelayErrorCode `json:"code"`
	Message string         `json:"message,omitempty"`
}

type relayReceipt struct {
	ExternalID string `json:"externalId,omitempty"`
	Method     string `json:"method"`
	Reference  string `json:"reference"`
	Timestamp  string `json:"timestamp"`
}

type relayResponse struct {
	Success bool          `json:"success"`
	Error   *relayError   `json:"error,omitempty"`
	Receipt *relayReceipt `json:"receipt,omitempty"`
}

func newRelayClient(config *RelayConfig) (*relayClient, error) {
	if config == nil {
		return nil, nil
	}
	if strings.TrimSpace(config.APIKey) == "" {
		return nil, fmt.Errorf("tempo server: relay API key is required")
	}
	baseURL := config.APIBaseURL
	if baseURL == "" {
		baseURL = defaultRelayAPIBaseURL
	}
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, fmt.Errorf("tempo server: invalid relay API base URL %q", baseURL)
	}
	if !strings.HasSuffix(parsed.Path, "/") {
		parsed.Path += "/"
	}
	client := http.Client{Timeout: defaultRelayTimeout}
	if config.HTTPClient != nil {
		client = *config.HTTPClient
	}
	// Relay endpoints should not redirect. Besides making endpoint selection
	// explicit, this prevents the custom API-key header from reaching another
	// origin through net/http's default redirect handling.
	client.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return &relayClient{
		apiKey:       config.APIKey,
		httpClient:   &client,
		validateURL:  parsed.ResolveReference(&url.URL{Path: "v1/mpp/validate"}).String(),
		broadcastURL: parsed.ResolveReference(&url.URL{Path: "v1/mpp/broadcast"}).String(),
	}, nil
}

func (r *relayClient) validate(
	ctx context.Context,
	credential *mpp.Credential,
	request map[string]any,
	method string,
	intent string,
) (*mppserver.Validation, error) {
	input, err := marshalRelayInput(credential)
	if err != nil {
		return nil, relayFailure(credential, nil)
	}
	validation, err := r.post(ctx, r.validateURL, input)
	if err != nil || !validation.Success {
		return nil, relayFailure(credential, validation.Error)
	}
	return &mppserver.Validation{
		Challenge:  credential.Challenge,
		Credential: credential,
		Details:    map[string]any{},
		Intent:     intent,
		Method:     method,
		Request:    request,
		Source:     credential.Source,
	}, nil
}

func (r *relayClient) broadcast(ctx context.Context, credential *mpp.Credential) (*mpp.Receipt, error) {
	input, err := marshalRelayInput(credential)
	if err != nil {
		return nil, relayFailure(credential, nil)
	}
	// Send no idempotency key: a cached settlement receipt must not authorize
	// another protected operation, so the relay consumes each credential once.
	broadcast, err := r.post(ctx, r.broadcastURL, input)
	if err != nil {
		// The relay may have settled the payment before the exchange failed.
		// Without an idempotency key a retry cannot recover the receipt, so tell
		// the app the outcome is unknown and must be reconciled separately.
		return nil, relaySettlementUnknown(credential.Challenge.ID)
	}
	if !broadcast.Success {
		if broadcast.Error != nil {
			switch broadcast.Error.Code {
			case RelayErrorAlreadyUsed, RelayErrorExpired, RelayErrorInvalidPayment,
				RelayErrorInsufficientFunds, RelayErrorPolicyDenied, RelayErrorScreenRejected,
				RelayErrorSimulationFailed, RelayErrorTemporarilyUnavailable, RelayErrorUnsupported:
				return nil, relayFailure(credential, broadcast.Error)
			}
		}
		// Confirmation failures and unrecognized errors do not prove non-settlement.
		return nil, relaySettlementUnknown(credential.Challenge.ID)
	}
	if broadcast.Receipt == nil {
		return nil, relaySettlementUnknown(credential.Challenge.ID)
	}
	receipt, err := parseRelayReceipt(broadcast.Receipt)
	if err != nil {
		return nil, relaySettlementUnknown(credential.Challenge.ID)
	}
	return receipt, nil
}

func marshalRelayInput(credential *mpp.Credential) ([]byte, error) {
	raw, err := json.Marshal(credential)
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var input any
	if err := decoder.Decode(&input); err != nil {
		return nil, err
	}
	var canonical bytes.Buffer
	encoder := json.NewEncoder(&canonical)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(input); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(canonical.Bytes(), []byte("\n")), nil
}

func relayCredentialRequest(credential *mpp.Credential) (map[string]any, error) {
	if credential.Challenge.Request == "" {
		return nil, nil
	}
	return mpp.B64Decode(credential.Challenge.Request)
}

func (r *relayClient) post(
	ctx context.Context,
	endpoint string,
	input []byte,
) (relayResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(input))
	if err != nil {
		return relayResponse{}, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Tempo-API-Key", r.apiKey)

	response, err := r.httpClient.Do(req)
	if err != nil {
		return relayResponse{}, err
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return relayResponse{}, fmt.Errorf("relay returned HTTP %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxRelayResponseSize+1))
	if err != nil || len(body) > maxRelayResponseSize {
		return relayResponse{}, fmt.Errorf("invalid relay response")
	}
	var result relayResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return relayResponse{}, err
	}
	return result, nil
}

func parseRelayReceipt(receipt *relayReceipt) (*mpp.Receipt, error) {
	if receipt.Method != tempo.MethodName || receipt.Reference == "" {
		return nil, mpp.ErrVerificationFailed("payment verification failed")
	}
	timestamp, err := time.Parse(time.RFC3339, receipt.Timestamp)
	if err != nil {
		return nil, mpp.ErrVerificationFailed("payment verification failed")
	}
	return &mpp.Receipt{
		Status:     "success",
		Timestamp:  timestamp.UTC(),
		Reference:  receipt.Reference,
		Method:     receipt.Method,
		ExternalID: receipt.ExternalID,
	}, nil
}

func relayFailure(credential *mpp.Credential, relayErr *relayError) error {
	if relayErr != nil && relayErr.Code == RelayErrorExpired {
		return mpp.ErrPaymentExpired(credential.Challenge.Expires)
	}
	err := mpp.ErrVerificationFailed("payment verification failed")
	if relayErr != nil {
		err.Details = safeRelayErrorDetails(relayErr.Code)
	}
	return err
}

// relaySettlementUnknownCode marks a broadcast whose outcome is unknown because
// the relay exchange failed after the request may have been sent.
const relaySettlementUnknownCode = "settlement_unknown"

func relaySettlementUnknown(challengeID string) error {
	err := mpp.ErrPaymentOutcomeUnknown(challengeID)
	err.Details = map[string]any{"code": relaySettlementUnknownCode}
	return err
}

func safeRelayErrorDetails(code RelayErrorCode) map[string]any {
	switch code {
	case RelayErrorAlreadyUsed,
		RelayErrorBroadcastFailed,
		RelayErrorInsufficientFunds,
		RelayErrorInvalidPayment,
		RelayErrorSimulationFailed,
		RelayErrorUnsupported:
		return map[string]any{"code": string(code)}
	case RelayErrorTemporarilyUnavailable:
		return map[string]any{"code": string(code), "retry": "same_credential"}
	default:
		return nil
	}
}
