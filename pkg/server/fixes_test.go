package server

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tempoxyz/mpp-go/pkg/mpp"
)

type hijackRecorder struct {
	*httptest.ResponseRecorder
	writeHeaderCalls int
}

func (w *hijackRecorder) WriteHeader(status int) {
	w.writeHeaderCalls++
	w.ResponseRecorder.WriteHeader(status)
}

func (w *hijackRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	client, server := net.Pipe()
	_ = client.Close()
	return server, bufio.NewReadWriter(bufio.NewReader(server), bufio.NewWriter(server)), nil
}

func TestDeferPaymentReceiptSkipsWriteHeaderAfterHijack(t *testing.T) {
	recorder := &hijackRecorder{ResponseRecorder: httptest.NewRecorder()}
	wrapped, complete := DeferPaymentReceipt(recorder, mpp.Success("tempo", "0xreceipt"))

	hijacker, ok := wrapped.(http.Hijacker)
	require.True(t, ok)
	conn, _, err := hijacker.Hijack()
	require.NoError(t, err)
	defer conn.Close()

	complete()
	assert.Zero(t, recorder.writeHeaderCalls, "complete must not write a header on a hijacked connection")
	assert.Empty(t, recorder.Header().Get(mpp.HeaderPaymentReceipt))
}

func TestDeferPaymentReceiptCommitsAfterFailedHijack(t *testing.T) {
	recorder := httptest.NewRecorder() // does not implement http.Hijacker
	wrapped, complete := DeferPaymentReceipt(recorder, mpp.Success("tempo", "0xreceipt"))

	_, _, err := wrapped.(http.Hijacker).Hijack()
	require.Error(t, err)

	complete()
	assert.Equal(t, http.StatusOK, recorder.Code)
	assert.NotEmpty(t, recorder.Header().Get(mpp.HeaderPaymentReceipt))
}

func challengeScope(t *testing.T, header http.Header) any {
	t.Helper()
	challenge, err := mpp.ParseChallenge(header.Get(mpp.HeaderWWWAuthenticate))
	require.NoError(t, err)
	return challenge.Request["_mppx_scope"]
}

func TestChargeMiddlewareRespectsCallerMppxScope(t *testing.T) {
	payment := newTestServer(t, middlewareTestMethod{}, "api.example.com", "test-secret-key-minimum-32-byte-secret")
	next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})

	custom := ChargeMiddleware(payment, ChargeParams{
		Amount:    "0.50",
		MppxScope: map[string]string{"route": "custom"},
	})(next)
	resp := httptest.NewRecorder()
	custom.ServeHTTP(resp, httptest.NewRequest(http.MethodGet, "/paid?x=1", nil))
	require.Equal(t, http.StatusPaymentRequired, resp.Code)
	assert.Equal(t, map[string]any{"route": "custom"}, challengeScope(t, resp.Header()))

	fromRequest := ChargeMiddleware(payment, ChargeParams{Amount: "0.50"})(next)
	resp = httptest.NewRecorder()
	fromRequest.ServeHTTP(resp, httptest.NewRequest(http.MethodGet, "/paid?x=1", nil))
	require.Equal(t, http.StatusPaymentRequired, resp.Code)
	assert.Equal(t, map[string]any{"resource": "/paid", "query": "x=1"}, challengeScope(t, resp.Header()))
}

func TestComposeMiddlewareRespectsCallerMppxScope(t *testing.T) {
	payment := newTestServer(t, middlewareTestMethod{}, "api.example.com", "test-secret-key-minimum-32-byte-secret")
	handler := ComposeMiddleware(ComposeConfig{
		Mpp:    payment,
		Params: ChargeParams{Amount: "0.50", MppxScope: map[string]string{"route": "custom"}},
	})(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))

	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, httptest.NewRequest(http.MethodGet, "/paid", nil))
	require.Equal(t, http.StatusPaymentRequired, resp.Code)
	assert.Equal(t, map[string]any{"route": "custom"}, challengeScope(t, resp.Header()))
}

func TestChargeMiddlewareReportsServerMisconfigurationAsInternalError(t *testing.T) {
	payment := newTestServer(t, middlewareTestMethod{}, "api.example.com", "test-secret-key-minimum-32-byte-secret")
	handler := ChargeMiddleware(payment, ChargeParams{
		Amount: "0.50",
		Meta:   map[string]string{"_mpp_nonce": "fixed"},
	})(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("handler should not be called")
	}))

	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, httptest.NewRequest(http.MethodGet, "/paid", nil))
	require.Equal(t, http.StatusInternalServerError, resp.Code)
	var problem struct {
		Type   string `json:"type"`
		Detail string `json:"detail"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&problem))
	assert.Equal(t, string(mpp.ErrorTypeInternalPayment), problem.Type)
	assert.NotContains(t, problem.Detail, "_mpp_nonce")
}

func TestNewRejectsNilMethod(t *testing.T) {
	payment, err := New(nil, "api.example.com", "test-secret-key-minimum-32-byte-secret")
	assert.Nil(t, payment)
	assert.ErrorContains(t, err, "payment method is required")
}

func TestIsMalformedCredentialUnwraps(t *testing.T) {
	assert.True(t, isMalformedCredential(fmt.Errorf("wrapped: %w", mpp.ErrMalformedCredential("bad"))))
	assert.False(t, isMalformedCredential(mpp.ErrBadRequest("bad")))
}

func TestComposeChargeKeepsChallengeCollectionError(t *testing.T) {
	payment := newTestServer(t, middlewareTestMethod{}, "api.example.com", "test-secret-key-minimum-32-byte-secret")
	result, err := Compose(context.Background(), "Payment not-base64!", ComposeConfig{
		Mpp:    payment,
		Params: ChargeParams{Amount: "0.50", Meta: map[string]string{"_mpp_nonce": "fixed"}},
	})
	assert.Nil(t, result)
	require.ErrorContains(t, err, "_mpp_nonce is reserved")
	// No challenge could be issued: this is a server failure, not a client
	// PaymentError, so MCP and HTTP report it as internal.
	var pe *mpp.PaymentError
	assert.False(t, errors.As(err, &pe))
}

func TestComposeMiddlewareMalformedCredentialWithoutChallengesIsInternalError(t *testing.T) {
	payment := newTestServer(t, middlewareTestMethod{}, "api.example.com", "test-secret-key-minimum-32-byte-secret")
	handler := ComposeMiddleware(ComposeConfig{
		Mpp:    payment,
		Params: ChargeParams{Amount: "0.50", Meta: map[string]string{"_mpp_nonce": "fixed"}},
	})(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("handler should not be called")
	}))

	req := httptest.NewRequest(http.MethodGet, "/paid", nil)
	req.Header.Set(mpp.HeaderAuthorization, "Payment not-base64!")
	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, req)
	require.Equal(t, http.StatusInternalServerError, resp.Code)
	var problem map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&problem))
	assert.Equal(t, string(mpp.ErrorTypeInternalPayment), problem["type"])
}
