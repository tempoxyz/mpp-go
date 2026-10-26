package ginadapter

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	ginfw "github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tempoxyz/mpp-go/pkg/mpp"
	"github.com/tempoxyz/mpp-go/pkg/server"
)

// unformattableReceiptMethod settles every Credential with a receipt
// mpp.FormatReceiptWithError rejects (no method).
type unformattableReceiptMethod struct{ settled *int }

func (unformattableReceiptMethod) Name() string { return "tempo" }

func (m unformattableReceiptMethod) Intents() map[string]server.Intent {
	intent, err := server.NewIntent("charge", server.IntentHooks{Verify: func(context.Context, *mpp.Credential, map[string]any) (*mpp.Receipt, error) {
		*m.settled++
		return &mpp.Receipt{Status: "success", Reference: "0xreceipt", Timestamp: time.Now()}, nil
	}})
	if err != nil {
		panic(err)
	}
	return map[string]server.Intent{"charge": intent}
}

func settledAuthorization(t *testing.T, challengeHeader string) string {
	t.Helper()
	challenge, err := mpp.ParseChallenge(challengeHeader)
	require.NoError(t, err)
	authorization, err := mpp.FormatCredential(challenge.NewCredential(map[string]any{"type": "hash", "hash": "0xabc"}))
	require.NoError(t, err)
	return authorization
}

func TestChargeMiddlewareServesSettledPaymentWhenReceiptCannotBeFormatted(t *testing.T) {
	ginfw.SetMode(ginfw.TestMode)
	var settled, served int
	payment := newTestServer(t, unformattableReceiptMethod{&settled}, "api.example.com", "test-secret-key-minimum-32-byte-secret")
	router := ginfw.New()
	router.GET("/paid", ChargeMiddleware(payment, server.ChargeParams{Amount: "0.50"}), func(c *ginfw.Context) {
		served++
		c.String(http.StatusOK, "paid content")
	})

	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, httptest.NewRequest(http.MethodGet, "/paid", nil))
	require.Equal(t, http.StatusPaymentRequired, resp.Code)

	req := httptest.NewRequest(http.MethodGet, "/paid", nil)
	req.Header.Set(mpp.HeaderAuthorization, settledAuthorization(t, resp.Header().Get(mpp.HeaderWWWAuthenticate)))
	resp = httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	assert.Equal(t, 1, settled)
	assert.Equal(t, 1, served, "a settled payment must reach the handler")
	assert.Equal(t, http.StatusOK, resp.Code)
	assert.Equal(t, "paid content", resp.Body.String())
	assert.Empty(t, resp.Header().Get(mpp.HeaderPaymentReceipt))
}
