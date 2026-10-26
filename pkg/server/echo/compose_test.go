package echoadapter

import (
	"net/http"
	"net/http/httptest"
	"testing"

	echofw "github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tempoxyz/mpp-go/pkg/mpp"
	"github.com/tempoxyz/mpp-go/pkg/server"
)

func TestComposeMiddleware(t *testing.T) {
	t.Parallel()

	payment := newTestServer(t, offersTestMethod{}, "api.example.com", "test-secret-key-minimum-32-byte-secret")
	e := echofw.New()
	e.GET("/paid/:id", func(c echofw.Context) error {
		return c.String(http.StatusOK, Receipt(c).Reference)
	}, ComposeMiddleware(
		server.Offer{Mpp: payment, Params: server.ChargeParams{Amount: "1"}},
		server.Offer{Mpp: payment, Params: server.ChargeParams{Amount: "2"}},
	))

	recorder := httptest.NewRecorder()
	e.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/paid/7", nil))
	require.Equal(t, http.StatusPaymentRequired, recorder.Code)
	_, challenges, err := offeredCurrencies(recorder.Header())
	require.NoError(t, err)
	require.Len(t, challenges, 4)
	scope := challenges[3].Request["_mppx_scope"].(map[string]any)
	assert.Equal(t, "/paid/:id", scope["route"])
	assert.Equal(t, "/paid/7", scope["resource"])

	request := httptest.NewRequest(http.MethodGet, "/paid/7", nil)
	authorization, err := mpp.FormatCredential(challenges[3].NewCredential(map[string]any{"type": "test"}))
	require.NoError(t, err)
	request.Header.Set(mpp.HeaderAuthorization, authorization)
	paid := httptest.NewRecorder()
	e.ServeHTTP(paid, request)
	require.Equal(t, http.StatusOK, paid.Code)
	assert.Equal(t, "0xreceipt-"+testOffers[1], paid.Body.String())
	assert.NotEmpty(t, paid.Header().Get(mpp.HeaderPaymentReceipt))

	assert.Panics(t, func() { ComposeMiddleware() })
}
