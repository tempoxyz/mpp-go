package chargeserver

import (
	"context"
	"encoding/json"
	"github.com/tempoxyz/mpp-go/pkg/client"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tempoxyz/mpp-go/pkg/internal/mpptest"

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
	challenge := mpptest.NewChallenge(
		"test-secret-key-minimum-32-byte-secret",
		"api.example.com",
		tempo.MethodName,
		tempo.IntentCharge,
		map[string]any{
			"amount":    "1",
			"currency":  "0x20c0000000000000000000000000000000000001",
			"recipient": "0x70997970c51812dc3a010c7d01b50e0d17dc79c8",
		},
		mpp.WithExpires(mpp.ExpiresIn(5*time.Minute)),
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
	assert.Empty(t, broadcastCall.IdempotencyKey)
}

func TestRelayConsumesCredentialOnceWithoutCachedReceipt(t *testing.T) {
	t.Parallel()
	for _, payload := range []map[string]any{
		{"type": "transaction", "signature": "0x1234"},
		{"type": "hash", "hash": "0x1a2b3c4d5e6f7890abcdef1234567890abcdef1234567890abcdef1234567890"},
		{"type": "proof", "signature": "0x1234"},
	} {
		t.Run(payload["type"].(string), func(t *testing.T) {
			t.Parallel()
			var mu sync.Mutex
			consumed := false
			server, calls := relayTestServer(t, func(path string) any {
				if path == "/v1/mpp/validate" {
					return map[string]any{"success": true}
				}
				mu.Lock()
				defer mu.Unlock()
				if consumed {
					// A relay replays a cached receipt only for a repeated idempotency key.
					return map[string]any{"success": false, "error": map[string]any{"code": "already_used", "message": "Used"}}
				}
				consumed = true
				return map[string]any{
					"success": true,
					"receipt": map[string]any{"method": "tempo", "reference": "0xabc", "timestamp": "2026-07-22T00:00:00Z"},
				}
			})
			intent := newRelayIntentForTest(t, server.URL)
			credential := relayTestCredential(payload)

			_, err := intent.Verify(context.Background(), credential, nil)
			require.NoError(t, err)
			_, err = intent.Verify(context.Background(), credential, nil)
			require.ErrorIs(t, err, mpp.ErrVerification)
			for range 4 {
				assert.Empty(t, (<-calls).IdempotencyKey)
			}
		})
	}
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

	assert.Empty(t, broadcastCall.IdempotencyKey)
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
	method, err := New(Config{Store: tempo.NewMemoryStore(),
		ChainID:    42431,
		Currencies: []string{testCurrency},
		Recipient:  testRecipient,
		Relay:      &RelayConfig{APIKey: relayTestAPIKey, APIBaseURL: server.URL},
	})
	require.NoError(t, err)
	payment := newTestServer(t, method, "api.example.com", "test-secret-key-minimum-32-byte-secret")
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
			if tt.broadcast != nil {
				assert.Equal(t, mpp.ErrorTypePaymentOutcomeUnknown, paymentErr.Type)
				assert.Equal(t, http.StatusInternalServerError, paymentErr.Status)
				assert.Equal(t, map[string]any{"code": "settlement_unknown"}, paymentErr.Details)
			} else {
				assert.Equal(t, mpp.ErrorTypeVerificationFailed, paymentErr.Type)
				assert.Empty(t, paymentErr.Details)
			}
		})
	}
}

func TestRelayBroadcastExchangeFailureIsSettlementUnknown(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		respond func(http.ResponseWriter)
	}{
		{name: "malformed body", respond: func(w http.ResponseWriter) {
			_, _ = w.Write([]byte("not a relay response"))
		}},
		{name: "server error", respond: func(w http.ResponseWriter) {
			http.Error(w, "private relay detail", http.StatusBadGateway)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/v1/mpp/validate" {
					_ = json.NewEncoder(w).Encode(map[string]any{"success": true})
					return
				}
				tt.respond(w)
			}))
			t.Cleanup(server.Close)

			_, err := newRelayIntentForTest(t, server.URL).Verify(context.Background(), relayTestCredential(map[string]any{"type": "proof"}), nil)
			var paymentErr *mpp.PaymentError
			require.ErrorAs(t, err, &paymentErr)
			assert.Equal(t, mpp.ErrorTypePaymentOutcomeUnknown, paymentErr.Type)
			assert.Equal(t, map[string]any{"code": "settlement_unknown"}, paymentErr.Details)
			assert.Equal(t, http.StatusInternalServerError, paymentErr.Status)
			assert.NotContains(t, paymentErr.Error(), "private relay detail")
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

func TestNewRejectsIntentWithRelay(t *testing.T) {
	t.Parallel()
	localIntent, err := NewIntent(IntentConfig{Store: tempo.NewMemoryStore()})
	require.NoError(t, err)
	config := Config{
		Intent:    localIntent,
		Recipient: testRecipient,
		ChainID:   42431,
		Relay:     &RelayConfig{APIKey: relayTestAPIKey, APIBaseURL: "https://relay.example"},
	}
	method, err := New(config)
	require.ErrorContains(t, err, "Config.Intent cannot be combined with Relay")
	require.Nil(t, method)
	method, err = New(config)
	require.ErrorContains(t, err, "Config.Intent cannot be combined with Relay")
	require.Nil(t, method)
}

func TestRelayMethodAllowsRelayResolvedChain(t *testing.T) {
	t.Parallel()
	method, err := New(Config{Store: tempo.NewMemoryStore(),
		ChainID:    999999,
		Currencies: []string{testCurrency},
		Recipient:  testRecipient,
		Relay:      &RelayConfig{APIKey: relayTestAPIKey},
	})
	require.NoError(t, err)

	_, err = method.BuildChargeRequest(mppserver.ChargeParams{Amount: "1"})
	require.NoError(t, err)
}

func newTestServer(t *testing.T, method mppserver.Method, realm, secretKey string) *mppserver.Mpp {
	t.Helper()
	payment, err := mppserver.New(method, realm, secretKey)
	require.NoError(t, err)
	return payment
}

func TestNewRelayClientDefaultsToTimeout(t *testing.T) {
	relay, err := newRelayClient(&RelayConfig{APIKey: relayTestAPIKey})
	require.NoError(t, err)
	assert.Equal(t, 30*time.Second, relay.httpClient.Timeout)

	custom := &http.Client{Timeout: time.Second}
	relay, err = newRelayClient(&RelayConfig{APIKey: relayTestAPIKey, HTTPClient: custom})
	require.NoError(t, err)
	assert.Equal(t, time.Second, relay.httpClient.Timeout)
}

// The mock relay settles once but loses its response. Even with multiple
// currencies available, the automatic transport must not create another payment.
func TestRelayUnknownSettlementStopsAutomaticPayment(t *testing.T) {
	for _, body := range []string{
		"lost response",
		`{"success":true}`,
		`{"success":true,"receipt":{"method":"tempo","reference":"0xabc","timestamp":"invalid"}}`,
	} {
		t.Run(body, func(t *testing.T) {
			var settlements atomic.Int32
			relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/v1/mpp/validate" {
					_, _ = w.Write([]byte(`{"success":true}`))
					return
				}
				settlements.Add(1)
				_, _ = w.Write([]byte(body))
			}))
			defer relay.Close()
			method, err := New(Config{Store: tempo.NewMemoryStore(), ChainID: 42431,
				Currencies: []string{ousdAddress, usdceAddress}, Recipient: testRecipient,
				Relay: &RelayConfig{APIKey: relayTestAPIKey, APIBaseURL: relay.URL}})
			require.NoError(t, err)
			payment := newTestServer(t, method, "api.example.com", "test-secret-key-minimum-32-byte-secret")
			var served atomic.Int32
			endpoint := httptest.NewServer(mppserver.ChargeMiddleware(payment, mppserver.ChargeParams{Amount: "0.50"})(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				served.Add(1)
				w.WriteHeader(http.StatusOK)
			})))
			defer endpoint.Close()
			payer := &relayCountingPayer{}
			httpClient, err := client.NewHTTPClient([]client.Method{payer})
			require.NoError(t, err)
			response, err := httpClient.Get(endpoint.URL)
			require.NoError(t, err)
			defer response.Body.Close()
			assert.Equal(t, http.StatusInternalServerError, response.StatusCode)
			assert.Empty(t, response.Header.Values(mpp.HeaderWWWAuthenticate))
			var problem mpp.PaymentError
			require.NoError(t, json.NewDecoder(response.Body).Decode(&problem))
			assert.Equal(t, mpp.ErrorTypePaymentOutcomeUnknown, problem.Type)
			assert.Equal(t, "settlement_unknown", problem.Details["code"])
			assert.EqualValues(t, 1, payer.calls.Load())
			assert.EqualValues(t, 1, settlements.Load())
			assert.Zero(t, served.Load())
		})
	}
}

type relayCountingPayer struct{ calls atomic.Int32 }

func (*relayCountingPayer) Name() string   { return "tempo" }
func (*relayCountingPayer) Intent() string { return "charge" }
func (p *relayCountingPayer) CreateCredential(_ context.Context, challenge *mpp.Challenge) (*mpp.Credential, error) {
	p.calls.Add(1)
	return challenge.NewCredential(map[string]any{"type": "transaction", "signature": "0x1234"}), nil
}
