package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
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

type verificationFailedMethod struct{}

func (verificationFailedMethod) Name() string { return "tempo" }

func (verificationFailedMethod) Intents() map[string]Intent {
	return map[string]Intent{"charge": verificationFailedIntent{}}
}

type verificationFailedIntent struct{}

func (verificationFailedIntent) Name() string { return "charge" }

func (verificationFailedIntent) Verify(_ context.Context, _ *mpp.Credential, _ map[string]any) (*mpp.Receipt, error) {
	return nil, mpp.ErrVerificationFailed("bad proof")
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
	if got := paidResponse.Header.Get("Cache-Control"); got != "private" {
		t.Fatalf("Cache-Control = %q, want private", got)
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

func TestReadRequestBodyEnforcesLimit(t *testing.T) {
	prev := MaxRequestBodyBytes
	MaxRequestBodyBytes = 16
	defer func() { MaxRequestBodyBytes = prev }()

	// At the limit: allowed and fully preserved.
	exact := strings.Repeat("a", 16)
	r := httptest.NewRequest(http.MethodPost, "/paid", strings.NewReader(exact))
	body, err := ReadRequestBody(r)
	require.NoError(t, err)
	assert.Equal(t, exact, string(body))
	restored, err := io.ReadAll(r.Body)
	require.NoError(t, err)
	assert.Equal(t, exact, string(restored), "body must be restored for the handler")

	// Over the limit: rejected without buffering the whole body.
	tooBig := strings.Repeat("a", 17)
	r = httptest.NewRequest(http.MethodPost, "/paid", strings.NewReader(tooBig))
	_, err = ReadRequestBody(r)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exceeds")
}

func TestChargeMiddlewareRejectsOversizedBody(t *testing.T) {
	prev := MaxRequestBodyBytes
	MaxRequestBodyBytes = 32
	defer func() { MaxRequestBodyBytes = prev }()

	payment := New(middlewareTestMethod{}, "api.example.com", "secret-key")
	handler := ChargeMiddleware(payment, ChargeParams{Amount: "0.50"})(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	server := httptest.NewServer(handler)
	defer server.Close()

	resp, err := http.Post(server.URL, "application/json", strings.NewReader(strings.Repeat("x", 1024)))
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode,
		"oversized body must be rejected before verification")
}

func TestChargeMiddlewareAutoScopesRouteResourceAndQuery(t *testing.T) {
	payment := New(middlewareTestMethod{}, "api.example.com", "secret-key")
	handler := ChargeMiddleware(payment, ChargeParams{Amount: "0.50"})(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "paid")
	}))
	mux := http.NewServeMux()
	mux.Handle("GET /paid/{id}", handler)
	server := httptest.NewServer(mux)
	defer server.Close()

	challengeResponse, err := http.Get(server.URL + "/paid/one?view=full")
	require.NoError(t, err)
	defer challengeResponse.Body.Close()
	require.Equal(t, http.StatusPaymentRequired, challengeResponse.StatusCode)

	challenge, err := mpp.ParseChallenge(challengeResponse.Header.Get("WWW-Authenticate"))
	require.NoError(t, err)
	scope, ok := challenge.Request["_mppx_scope"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "/paid/{id}", scope["route"])
	assert.Equal(t, "/paid/one", scope["resource"])
	assert.Equal(t, "view=full", scope["query"])

	credential := &mpp.Credential{
		Challenge: challenge.ToEcho(),
		Source:    "did:key:z6Mkrdemo",
		Payload:   map[string]any{"type": "hash", "hash": "0xabc123"},
	}

	paidRequest, err := http.NewRequest(http.MethodGet, server.URL+"/paid/two?view=full", nil)
	require.NoError(t, err)
	paidRequest.Header.Set("Authorization", credential.ToAuthorization())
	paidResponse, err := http.DefaultClient.Do(paidRequest)
	require.NoError(t, err)
	defer paidResponse.Body.Close()
	assert.NotEqual(t, http.StatusOK, paidResponse.StatusCode)
	assert.Empty(t, paidResponse.Header.Get("Payment-Receipt"))
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
	assert.Equal(t, http.StatusPaymentRequired, paidResponse.StatusCode)
}

func TestChargeMiddlewarePreservesVerifiedRequestBody(t *testing.T) {
	payment := New(middlewareTestMethod{}, "api.example.com", "secret-key")
	handler := ChargeMiddleware(payment, ChargeParams{Amount: "0.50"})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_, _ = io.WriteString(w, string(body))
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
	credential := &mpp.Credential{
		Challenge: challenge.ToEcho(),
		Source:    "did:key:z6Mkrdemo",
		Payload:   map[string]any{"type": "hash", "hash": "0xabc123"},
	}
	retry, err := http.NewRequest(http.MethodPost, server.URL, strings.NewReader(originalBody))
	require.NoError(t, err)
	retry.Header.Set("Authorization", credential.ToAuthorization())

	paidResponse, err := http.DefaultClient.Do(retry)
	require.NoError(t, err)
	defer paidResponse.Body.Close()
	require.Equal(t, http.StatusOK, paidResponse.StatusCode)
	body, err := io.ReadAll(paidResponse.Body)
	require.NoError(t, err)
	assert.Equal(t, originalBody, string(body))
}

func TestChargeMiddlewareReturnsFreshChallengeOnVerificationFailure(t *testing.T) {
	payment := New(verificationFailedMethod{}, "api.example.com", "secret-key")
	handler := ChargeMiddleware(payment, ChargeParams{Amount: "0.50"})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Fail(t, "handler should not be called")
	}))

	challengeReq := httptest.NewRequest(http.MethodGet, "/paid", nil)
	challengeResp := httptest.NewRecorder()
	handler.ServeHTTP(challengeResp, challengeReq)
	require.Equal(t, http.StatusPaymentRequired, challengeResp.Code)

	challenge, err := mpp.ParseChallenge(challengeResp.Header().Get("WWW-Authenticate"))
	require.NoError(t, err)

	credential := &mpp.Credential{
		Challenge: challenge.ToEcho(),
		Payload:   map[string]any{"type": "hash", "hash": "0xabc123"},
	}

	req := httptest.NewRequest(http.MethodGet, "/paid", nil)
	req.Header.Set("Authorization", credential.ToAuthorization())
	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, req)

	require.Equal(t, http.StatusPaymentRequired, resp.Code)
	require.NotEmpty(t, resp.Header().Get("WWW-Authenticate"))
	_, err = mpp.ParseChallenge(resp.Header().Get("WWW-Authenticate"))
	require.NoError(t, err)
	assert.Equal(t, "application/problem+json", resp.Header().Get("Content-Type"))
	assert.Equal(t, "no-store", resp.Header().Get("Cache-Control"))

	var problem struct {
		Type string `json:"type"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&problem))
	assert.Equal(t, string(mpp.ErrorTypeVerificationFailed), problem.Type)
}

func TestChargeMiddlewareRejectsMultiplePaymentCredentials(t *testing.T) {
	payment := New(middlewareTestMethod{}, "api.example.com", "secret-key")
	handlerCalled := false
	handler := ChargeMiddleware(payment, ChargeParams{Amount: "0.50"})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handlerCalled = true
	}))
	server := httptest.NewServer(handler)
	defer server.Close()

	challengeResponse, err := http.Get(server.URL)
	require.NoError(t, err)
	defer challengeResponse.Body.Close()

	challenge, err := mpp.ParseChallenge(challengeResponse.Header.Get("WWW-Authenticate"))
	require.NoError(t, err)

	credential := &mpp.Credential{
		Challenge: challenge.ToEcho(),
		Source:    "did:key:z6Mkrdemo",
		Payload:   map[string]any{"type": "hash", "hash": "0xabc123"},
	}

	retry, err := http.NewRequest(http.MethodGet, server.URL, nil)
	require.NoError(t, err)
	retry.Header.Set("Authorization", credential.ToAuthorization()+", "+credential.ToAuthorization())

	paidResponse, err := http.DefaultClient.Do(retry)
	require.NoError(t, err)
	defer paidResponse.Body.Close()

	assert.False(t, handlerCalled)
	assert.Equal(t, http.StatusBadRequest, paidResponse.StatusCode)

	var problem struct {
		Type string `json:"type"`
	}
	require.NoError(t, json.NewDecoder(paidResponse.Body).Decode(&problem))
	assert.Equal(t, string(mpp.ErrorTypeBadRequest), problem.Type)
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
	assert.Equal(t, string(mpp.ErrorTypeBadRequest), problem.Type)
}

func TestServeVerified_PreservesResponseWriterOptionalInterfaces(t *testing.T) {
	w := newOptionalResponseWriter()
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatalf("ResponseWriter does not expose http.Flusher")
		}
		if _, ok := w.(http.Hijacker); !ok {
			t.Fatalf("ResponseWriter does not expose http.Hijacker")
		}
		flusher.Flush()
	})

	serveVerified(
		handler,
		w,
		httptest.NewRequest(http.MethodGet, "/", nil),
		&mpp.Credential{},
		mpp.Success("0xreceipt"),
	)

	if !w.flushed {
		t.Fatalf("Flush was not forwarded to the underlying ResponseWriter")
	}
	if got := w.header.Get("Cache-Control"); got != "private" {
		t.Fatalf("Cache-Control = %q, want private", got)
	}
}

type optionalResponseWriter struct {
	header  http.Header
	body    bytes.Buffer
	status  int
	flushed bool
}

func newOptionalResponseWriter() *optionalResponseWriter {
	return &optionalResponseWriter{header: make(http.Header)}
}

func (w *optionalResponseWriter) Header() http.Header {
	return w.header
}

func (w *optionalResponseWriter) Write(body []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	return w.body.Write(body)
}

func (w *optionalResponseWriter) WriteHeader(statusCode int) {
	if w.status == 0 {
		w.status = statusCode
	}
}

func (w *optionalResponseWriter) Flush() {
	w.flushed = true
}

func (w *optionalResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return nil, nil, nil
}
