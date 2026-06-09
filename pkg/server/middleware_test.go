package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tempoxyz/mpp-go/pkg/mpp"
)

type middlewareTestMethod struct{}

func (middlewareTestMethod) Name() string { return "tempo" }

func (middlewareTestMethod) Intents() map[string]Intent {
	return map[string]Intent{"charge": verifyTestIntent{}}
}

func TestChargeMiddleware_EndToEnd(t *testing.T) {
	payment := New(middlewareTestMethod{}, "api.example.com", "secret-key")
	handler := ChargeMiddleware(payment, ChargeParams{Amount: "0.50"})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, CredentialFromContext(r.Context()).Source+":"+ReceiptFromContext(r.Context()).Reference)
	}))
	server := httptest.NewServer(handler)
	defer server.Close()

	challengeResponse, err := http.Get(server.URL)
	if !assert.NoErrorf(t, err,
		"http.Get() error = %v", err) {
		return
	}

	defer challengeResponse.Body.Close()
	if !assert.Equalf(t, http.StatusPaymentRequired, challengeResponse.StatusCode,
		"challenge status = %d, want %d", challengeResponse.StatusCode, http.StatusPaymentRequired) {
		return
	}

	challenge, err := mpp.ParseChallenge(challengeResponse.Header.Get("WWW-Authenticate"))
	if !assert.NoErrorf(t, err,
		"ParseChallenge() error = %v", err) {
		return
	}

	credential := &mpp.Credential{
		Challenge: challenge.ToEcho(),
		Source:    "did:key:z6Mkrdemo",
		Payload:   map[string]any{"type": "hash", "hash": "0xabc123"},
	}

	retry, err := http.NewRequest(http.MethodGet, server.URL, nil)
	if !assert.NoErrorf(t, err,
		"http.NewRequest() error = %v", err) {
		return
	}

	retry.Header.Set("Authorization", credential.ToAuthorization())

	paidResponse, err := http.DefaultClient.Do(retry)
	if !assert.NoErrorf(t, err,
		"Do() error = %v", err) {
		return
	}

	defer paidResponse.Body.Close()
	if !assert.Equalf(t, http.StatusOK, paidResponse.StatusCode,
		"paid status = %d, want %d", paidResponse.StatusCode, http.StatusOK) {
		return
	}

	receipt, err := mpp.ParsePaymentReceipt(paidResponse.Header.Get("Payment-Receipt"))
	if !assert.NoErrorf(t, err,
		"ParsePaymentReceipt() error = %v", err) {
		return
	}
	if !assert.Equalf(t, "0xreceipt", receipt.Reference,
		"receipt reference = %q, want %q", receipt.Reference, "0xreceipt") {
		return
	}

	body, err := io.ReadAll(paidResponse.Body)
	if !assert.NoErrorf(t, err,
		"io.ReadAll() error = %v", err) {
		return
	}

	if got := string(body); got != "did:key:z6Mkrdemo:0xreceipt" {
		assert.Failf(t, "", "response body = %q, want %q", got, "did:key:z6Mkrdemo:0xreceipt")
		return
	}
}

func TestChargeMiddlewareRejectsTamperedRequestBodyDigest(t *testing.T) {
	payment := New(middlewareTestMethod{}, "api.example.com", "secret-key")
	handler := ChargeMiddleware(payment, ChargeParams{Amount: "0.50"})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		_, _ = io.WriteString(w, "paid")
	}))
	server := httptest.NewServer(handler)
	defer server.Close()

	const originalBody = `{"query":"paid"}`
	challengeRequest, err := http.NewRequest(http.MethodPost, server.URL, strings.NewReader(originalBody))
	require.NoError(t, err)

	challengeResponse, err := http.DefaultClient.Do(challengeRequest)
	require.NoError(t, err)
	defer challengeResponse.Body.Close()
	require.Equal(t, http.StatusPaymentRequired, challengeResponse.StatusCode)

	challenge, err := mpp.ParseChallenge(challengeResponse.Header.Get("WWW-Authenticate"))
	require.NoError(t, err)
	assert.Equal(t, mpp.BodyDigest.Compute([]byte(originalBody)), challenge.Digest)

	credential := &mpp.Credential{
		Challenge: challenge.ToEcho(),
		Source:    "did:key:z6Mkrdemo",
		Payload:   map[string]any{"type": "hash", "hash": "0xabc123"},
	}
	retry, err := http.NewRequest(http.MethodPost, server.URL, strings.NewReader(`{"query":"tampered"}`))
	require.NoError(t, err)
	retry.Header.Set("Authorization", credential.ToAuthorization())

	paidResponse, err := http.DefaultClient.Do(retry)
	require.NoError(t, err)
	defer paidResponse.Body.Close()
	assert.Equal(t, http.StatusBadRequest, paidResponse.StatusCode)
}

func TestChargeMiddlewareRejectsCRLFChallengeDescription(t *testing.T) {
	payment := New(middlewareTestMethod{}, "api.example.com", "secret-key")
	handler := ChargeMiddleware(payment, ChargeParams{
		Amount:      "0.50",
		Description: "Line one\r\nLine two",
	})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Fail(t, "handler should not be called")
		return
	}))

	req := httptest.NewRequest(http.MethodGet, "/paid", nil)
	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, req)

	require.Equal(t, http.StatusBadRequest, resp.Code)
	assert.Empty(t, resp.Header().Get("WWW-Authenticate"))

	var problem struct {
		Type string `json:"type"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&problem))
	assert.Equal(t, string(mpp.ErrorTypeInvalidChallenge), problem.Type)
}
