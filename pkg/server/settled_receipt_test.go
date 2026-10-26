package server

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tempoxyz/mpp-go/pkg/mpp"
)

// unformattableReceiptMethod settles every Credential with a receipt
// mpp.FormatReceiptWithError rejects (no method).
type unformattableReceiptMethod struct{ settled *int }

func (unformattableReceiptMethod) Name() string { return "tempo" }

func (m unformattableReceiptMethod) Intents() map[string]Intent {
	intent, err := NewIntent("charge", IntentHooks{Verify: func(context.Context, *mpp.Credential, map[string]any) (*mpp.Receipt, error) {
		*m.settled++
		return &mpp.Receipt{Status: "success", Reference: "0xreceipt", Timestamp: time.Now()}, nil
	}})
	if err != nil {
		panic(err)
	}
	return map[string]Intent{"charge": intent}
}

func TestChargeMiddlewareServesSettledPaymentWhenReceiptCannotBeFormatted(t *testing.T) {
	var settled int
	payment := newTestServer(t, unformattableReceiptMethod{&settled}, "api.example.com", "test-secret-key-minimum-32-byte-secret")
	var served int
	handler := ChargeMiddleware(payment, ChargeParams{Amount: "0.50"})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		served++
		assert.NotNil(t, ReceiptFromContext(r.Context()))
		_, _ = io.WriteString(w, "paid content")
	}))

	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, httptest.NewRequest(http.MethodGet, "/paid", nil))
	require.Equal(t, http.StatusPaymentRequired, resp.Code)
	challenge, err := mpp.ParseChallenge(resp.Header().Get(mpp.HeaderWWWAuthenticate))
	require.NoError(t, err)
	authorization, err := mpp.FormatCredential(challenge.NewCredential(map[string]any{"type": "hash", "hash": "0xabc"}))
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "/paid", nil)
	req.Header.Set(mpp.HeaderAuthorization, authorization)
	resp = httptest.NewRecorder()
	handler.ServeHTTP(resp, req)

	assert.Equal(t, 1, settled)
	assert.Equal(t, 1, served, "a settled payment must reach the handler")
	assert.Equal(t, http.StatusOK, resp.Code)
	assert.Equal(t, "paid content", resp.Body.String())
	assert.Empty(t, resp.Header().Get(mpp.HeaderPaymentReceipt))
	assert.Equal(t, "private", resp.Header().Get("Cache-Control"))
}
