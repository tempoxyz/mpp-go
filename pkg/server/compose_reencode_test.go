package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tempoxyz/mpp-go/pkg/mpp"
)

// growingCredential returns a Payment credential under the 64 KiB
// Authorization limit whose challenge echoes opaque as a JSON object.
// ParseCredential accepts that form, but re-encoding it with
// mpp.FormatCredential base64url-encodes opaque, growing it past the limit.
func growingCredential(t *testing.T, payment *Mpp) string {
	t.Helper()
	challenge, err := payment.Charge(context.Background(), ChargeParams{Amount: "0.50"})
	require.NoError(t, err)
	require.NotNil(t, challenge.Challenge)
	c := challenge.Challenge
	raw, err := json.Marshal(map[string]any{
		"challenge": map[string]any{
			"id":      c.ID,
			"realm":   c.Realm,
			"method":  c.Method,
			"intent":  c.Intent,
			"request": c.RequestB64,
			"expires": c.Expires,
			"opaque":  map[string]any{"blob": strings.Repeat("x", 46*1024)},
		},
		"payload": map[string]any{"type": "hash", "hash": "0xabc"},
	})
	require.NoError(t, err)
	authorization := mpp.SchemePayment + " " + base64.RawURLEncoding.EncodeToString(raw)
	require.Less(t, len(authorization), 64*1024)

	cred, err := mpp.ParseCredential(authorization)
	require.NoError(t, err)
	_, err = mpp.FormatCredential(cred)
	require.ErrorContains(t, err, "exceeds maximum size", "the re-encoded credential must outgrow the limit")
	return authorization
}

func TestComposeMatchingDoesNotReencodeCredential(t *testing.T) {
	payment := newTestServer(t, middlewareTestMethod{}, "api.example.com", "test-secret-key-minimum-32-byte-secret")
	authorization := growingCredential(t, payment)
	configs := []ComposeConfig{{Mpp: payment, Params: ChargeParams{Amount: "0.50"}}}

	t.Run("ComposeMiddleware", func(t *testing.T) {
		handler := ComposeMiddleware(configs...)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			t.Fatal("handler should not be called for a tampered challenge")
		}))
		req := httptest.NewRequest(http.MethodGet, "/paid", nil)
		req.Header.Set(mpp.HeaderAuthorization, authorization)
		resp := httptest.NewRecorder()
		handler.ServeHTTP(resp, req)
		assert.Equal(t, http.StatusPaymentRequired, resp.Code, resp.Body.String())
		assert.NotEmpty(t, resp.Header().Get(mpp.HeaderWWWAuthenticate))
	})

	t.Run("ComposeCharge", func(t *testing.T) {
		result, err := ComposeCharge(context.Background(), authorization, configs...)
		require.Error(t, err)
		var pe *mpp.PaymentError
		require.True(t, errors.As(err, &pe), "want a client PaymentError, got %v", err)
		assert.Equal(t, http.StatusPaymentRequired, pe.Status)
		require.NotNil(t, result)
		assert.NotNil(t, result.Challenge)
	})
}
