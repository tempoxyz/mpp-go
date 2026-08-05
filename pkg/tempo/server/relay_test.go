package chargeserver

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tempoxyz/mpp-go/pkg/mpp"
	mppserver "github.com/tempoxyz/mpp-go/pkg/server"
	"github.com/tempoxyz/mpp-go/pkg/tempo"
)

const relayTestAPIKey = "tempo_api_key"

type relayCall struct {
	Body           map[string]any
	IdempotencyKey string
	Path           string
	APIKey         string
}

func relayTestCredential(payload map[string]any) *mpp.Credential {
	challenge := mpp.NewChallenge(
		"test-secret-key",
		"api.example.com",
		tempo.MethodName,
		tempo.IntentCharge,
		map[string]any{
			"amount":    "1",
			"currency":  "0x20c0000000000000000000000000000000000001",
			"recipient": "0x70997970c51812dc3a010c7d01b50e0d17dc79c8",
		},
		mpp.WithExpires(mpp.Expires.Minutes(5)),
	)
	return challenge.NewCredential(payload, mpp.WithCredentialSource(
		"did:pkh:eip155:42431:0x1234567890123456789012345678901234567890",
	))
}

func relayTestServer(t *testing.T, respond func(string) any) (*httptest.Server, <-chan relayCall) {
	t.Helper()
	calls := make(chan relayCall, 10)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		calls <- relayCall{
			Body:           body,
			IdempotencyKey: r.Header.Get("Idempotency-Key"),
			Path:           r.URL.Path,
			APIKey:         r.Header.Get("Tempo-API-Key"),
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(respond(r.URL.Path))
	}))
	t.Cleanup(server.Close)
	return server, calls
}

func newRelayForTest(t *testing.T, apiBaseURL string) *relayClient {
	t.Helper()
	relay, err := newRelayClient(&RelayConfig{APIKey: relayTestAPIKey, APIBaseURL: apiBaseURL})
	require.NoError(t, err)
	return relay
}

func newRelayIntentForTest(t *testing.T, apiBaseURL string) mppserver.Intent {
	t.Helper()
	intent, err := newRelayForTest(t, apiBaseURL).intent(tempo.IntentCharge)
	require.NoError(t, err)
	return intent
}

func TestRelayVerifyValidatesAndBroadcastsCredential(t *testing.T) {
	t.Parallel()
	server, calls := relayTestServer(t, func(path string) any {
		if path == "/mpp/v1/mpp/validate" {
			return map[string]any{"success": true}
		}
		return map[string]any{
			"success": true,
			"receipt": map[string]any{
				"externalId": "order_123",
				"method":     "tempo",
				"reference":  "0xabc",
				"timestamp":  "2026-07-22T00:00:00.000Z",
			},
		}
	})
	credential := relayTestCredential(map[string]any{
		"type":      "transaction",
		"signature": "0x1234",
	})

	receipt, err := newRelayIntentForTest(t, server.URL+"/mpp").Verify(context.Background(), credential, nil)
	require.NoError(t, err)
	assert.Equal(t, &mpp.Receipt{
		Status:     "success",
		Timestamp:  time.Date(2026, 7, 22, 0, 0, 0, 0, time.UTC),
		Reference:  "0xabc",
		Method:     "tempo",
		ExternalID: "order_123",
	}, receipt)

	validateCall := <-calls
	broadcastCall := <-calls
	assert.Equal(t, "/mpp/v1/mpp/validate", validateCall.Path)
	assert.Equal(t, "", validateCall.IdempotencyKey)
	assert.Equal(t, relayTestAPIKey, validateCall.APIKey)
	assert.Equal(t, credential.Source, validateCall.Body["source"])
	assert.Equal(t, validateCall.Body, broadcastCall.Body)
	assert.Equal(t, "/mpp/v1/mpp/broadcast", broadcastCall.Path)
	raw, err := hexutil.Decode("0x1234")
	require.NoError(t, err)
	assert.Equal(t, "mppx_"+crypto.Keccak256Hash(raw).Hex(), broadcastCall.IdempotencyKey)
}

func TestRelayFinalizesPushCredential(t *testing.T) {
	t.Parallel()
	server, calls := relayTestServer(t, func(path string) any {
		if path == "/v1/mpp/validate" {
			return map[string]any{"success": true}
		}
		return map[string]any{
			"success": true,
			"receipt": map[string]any{
				"method":    "tempo",
				"reference": "0xabc",
				"timestamp": "2026-07-22T00:00:00Z",
			},
		}
	})
	credential := relayTestCredential(map[string]any{
		"type": "hash",
		"hash": "0x1a2b3c4d5e6f7890abcdef1234567890abcdef1234567890abcdef1234567890",
	})

	_, err := newRelayIntentForTest(t, server.URL).Verify(context.Background(), credential, nil)
	require.NoError(t, err)
	validateCall := <-calls
	broadcastCall := <-calls
	assert.Equal(t, "/v1/mpp/validate", validateCall.Path)
	assert.Equal(t, "/v1/mpp/broadcast", broadcastCall.Path)

	input, err := marshalRelayInput(credential)
	require.NoError(t, err)
	digest := sha256.Sum256(input)
	assert.Equal(t, "mppx_0x"+hex.EncodeToString(digest[:]), broadcastCall.IdempotencyKey)
}

func TestRelayIntentExposesCredentialHooks(t *testing.T) {
	t.Parallel()
	server, calls := relayTestServer(t, func(path string) any {
		if path == "/v1/mpp/validate" {
			return map[string]any{"success": true}
		}
		return map[string]any{
			"success": true,
			"receipt": map[string]any{
				"method":    "tempo",
				"reference": "0xabc",
				"timestamp": "2026-07-22T00:00:00Z",
			},
		}
	})
	method, err := MethodFromConfig(Config{
		ChainID:   42431,
		Currency:  testCurrency,
		Recipient: testRecipient,
		Relay:     &RelayConfig{APIKey: relayTestAPIKey, APIBaseURL: server.URL},
	})
	require.NoError(t, err)
	payment := mppserver.New(method, "api.example.com", "test-secret-key")
	credential := relayTestCredential(map[string]any{"type": "transaction", "signature": "0x1234"})

	validation, err := payment.ValidateCredential(context.Background(), credential)
	require.NoError(t, err)
	boundRequest, err := relayCredentialRequest(credential)
	require.NoError(t, err)
	assert.Equal(t, boundRequest, validation.Request)
	assert.Equal(t, credential, validation.Credential)
	assert.Equal(t, "/v1/mpp/validate", (<-calls).Path)
	select {
	case call := <-calls:
		t.Fatalf("ValidateCredential() unexpectedly called %s", call.Path)
	default:
	}

	receipt, err := payment.BroadcastCredential(context.Background(), credential)
	require.NoError(t, err)
	assert.Equal(t, "0xabc", receipt.Reference)
	assert.Equal(t, "/v1/mpp/validate", (<-calls).Path)
	assert.Equal(t, "/v1/mpp/broadcast", (<-calls).Path)
}

func TestRelayErrorMapping(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		code    RelayErrorCode
		details map[string]any
		typeURI mpp.ErrorType
	}{
		{name: "already used", code: RelayErrorAlreadyUsed, details: map[string]any{"code": string(RelayErrorAlreadyUsed)}, typeURI: mpp.ErrorTypeVerificationFailed},
		{name: "broadcast failed", code: RelayErrorBroadcastFailed, details: map[string]any{"code": string(RelayErrorBroadcastFailed)}, typeURI: mpp.ErrorTypeVerificationFailed},
		{name: "invalid payment", code: RelayErrorInvalidPayment, details: map[string]any{"code": string(RelayErrorInvalidPayment)}, typeURI: mpp.ErrorTypeVerificationFailed},
		{name: "insufficient funds", code: RelayErrorInsufficientFunds, details: map[string]any{"code": string(RelayErrorInsufficientFunds)}, typeURI: mpp.ErrorTypeVerificationFailed},
		{name: "simulation failed", code: RelayErrorSimulationFailed, details: map[string]any{"code": string(RelayErrorSimulationFailed)}, typeURI: mpp.ErrorTypeVerificationFailed},
		{name: "unsupported", code: RelayErrorUnsupported, details: map[string]any{"code": string(RelayErrorUnsupported)}, typeURI: mpp.ErrorTypeVerificationFailed},
		{name: "temporarily unavailable", code: RelayErrorTemporarilyUnavailable, details: map[string]any{"code": string(RelayErrorTemporarilyUnavailable), "retry": "same_credential"}, typeURI: mpp.ErrorTypeVerificationFailed},
		{name: "expired", code: RelayErrorExpired, typeURI: mpp.ErrorTypePaymentExpired},
		{name: "policy denied", code: RelayErrorPolicyDenied, typeURI: mpp.ErrorTypeVerificationFailed},
		{name: "screen rejected", code: RelayErrorScreenRejected, typeURI: mpp.ErrorTypeVerificationFailed},
		{name: "unknown", code: RelayErrorUnknown, typeURI: mpp.ErrorTypeVerificationFailed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			server, _ := relayTestServer(t, func(string) any {
				return map[string]any{
					"success": false,
					"error": map[string]any{
						"code":    tt.code,
						"message": "private relay detail",
					},
				}
			})
			credential := relayTestCredential(map[string]any{"type": "proof", "signature": "0x1234"})

			_, err := newRelayIntentForTest(t, server.URL).Verify(context.Background(), credential, nil)
			var paymentErr *mpp.PaymentError
			require.ErrorAs(t, err, &paymentErr)
			assert.Equal(t, tt.typeURI, paymentErr.Type)
			assert.Equal(t, tt.details, paymentErr.Details)
			assert.NotContains(t, paymentErr.Error(), "private relay detail")
		})
	}
}

func TestRelayBoundaryFailuresAreGeneric(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		validate  any
		broadcast any
	}{
		{name: "validation malformed", validate: "not a relay response"},
		{name: "validation missing success", validate: map[string]any{}},
		{name: "broadcast malformed", validate: map[string]any{"success": true}, broadcast: "not a relay response"},
		{name: "broadcast missing receipt", validate: map[string]any{"success": true}, broadcast: map[string]any{"success": true}},
		{name: "receipt wrong method", validate: map[string]any{"success": true}, broadcast: map[string]any{"success": true, "receipt": map[string]any{"method": "stripe", "reference": "0xabc", "timestamp": "2026-07-22T00:00:00Z"}}},
		{name: "receipt invalid timestamp", validate: map[string]any{"success": true}, broadcast: map[string]any{"success": true, "receipt": map[string]any{"method": "tempo", "reference": "0xabc", "timestamp": "invalid"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			server, _ := relayTestServer(t, func(path string) any {
				if path == "/v1/mpp/validate" {
					return tt.validate
				}
				return tt.broadcast
			})

			_, err := newRelayIntentForTest(t, server.URL).Verify(context.Background(), relayTestCredential(map[string]any{"type": "proof"}), nil)
			var paymentErr *mpp.PaymentError
			require.ErrorAs(t, err, &paymentErr)
			assert.Equal(t, mpp.ErrorTypeVerificationFailed, paymentErr.Type)
			assert.Empty(t, paymentErr.Details)
		})
	}
}

func TestRelayHTTPFailureIsGeneric(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"error":{"code":"unsupported","message":"private relay detail"}}`, http.StatusForbidden)
	}))
	t.Cleanup(server.Close)

	_, err := newRelayIntentForTest(t, server.URL).Verify(context.Background(), relayTestCredential(map[string]any{"type": "proof"}), nil)
	var paymentErr *mpp.PaymentError
	require.ErrorAs(t, err, &paymentErr)
	assert.Equal(t, mpp.ErrorTypeVerificationFailed, paymentErr.Type)
	assert.Empty(t, paymentErr.Details)
	assert.NotContains(t, paymentErr.Error(), "private relay detail")
}

func TestRelayDoesNotFollowRedirects(t *testing.T) {
	t.Parallel()
	targetCalled := make(chan struct{}, 1)
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		targetCalled <- struct{}{}
	}))
	t.Cleanup(target.Close)
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", target.URL)
		w.WriteHeader(http.StatusTemporaryRedirect)
	}))
	t.Cleanup(redirect.Close)

	_, err := newRelayIntentForTest(t, redirect.URL).Verify(
		context.Background(),
		relayTestCredential(map[string]any{"type": "proof"}),
		nil,
	)
	require.Error(t, err)
	select {
	case <-targetCalled:
		t.Fatal("relay followed redirect and exposed request headers")
	default:
	}
}

func TestNewRelayClientValidationAndDefaults(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		config *RelayConfig
		err    string
	}{
		{name: "disabled"},
		{name: "missing API key", config: &RelayConfig{}, err: "tempo server: relay API key is required"},
		{name: "invalid base URL", config: &RelayConfig{APIKey: relayTestAPIKey, APIBaseURL: "://bad"}, err: "tempo server: invalid relay API base URL"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			relay, err := newRelayClient(tt.config)
			if tt.err == "" {
				require.NoError(t, err)
				assert.Nil(t, relay)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.err)
		})
	}

	relay, err := newRelayClient(&RelayConfig{APIKey: relayTestAPIKey})
	require.NoError(t, err)
	assert.Equal(t, defaultRelayAPIBaseURL+"/v1/mpp/validate", relay.validateURL)
	assert.Equal(t, defaultRelayAPIBaseURL+"/v1/mpp/broadcast", relay.broadcastURL)
}

func TestMethodFromConfigAppliesRelayToCustomIntent(t *testing.T) {
	t.Parallel()
	server, _ := relayTestServer(t, func(string) any {
		return map[string]any{"success": false, "error": map[string]any{"code": RelayErrorUnsupported}}
	})
	localIntent, err := NewIntent(IntentConfig{})
	require.NoError(t, err)
	method, err := MethodFromConfig(Config{
		Intent:    localIntent,
		Recipient: testRecipient,
		ChainID:   42431,
		Relay:     &RelayConfig{APIKey: relayTestAPIKey, APIBaseURL: server.URL},
	})
	require.NoError(t, err)

	configured := method.Intents()[tempo.IntentCharge]
	_, err = configured.Verify(context.Background(), relayTestCredential(map[string]any{"type": "proof"}), nil)
	var paymentErr *mpp.PaymentError
	require.ErrorAs(t, err, &paymentErr)
	assert.Equal(t, map[string]any{"code": string(RelayErrorUnsupported)}, paymentErr.Details)
}

func TestRelayMethodAllowsRelayResolvedChain(t *testing.T) {
	t.Parallel()
	method, err := MethodFromConfig(Config{
		ChainID:   999999,
		Currency:  testCurrency,
		Recipient: testRecipient,
		Relay:     &RelayConfig{APIKey: relayTestAPIKey},
	})
	require.NoError(t, err)

	_, err = method.BuildChargeRequest(mppserver.ChargeParams{Amount: "1"})
	require.NoError(t, err)
}
