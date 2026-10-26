package fiberadapter

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	fiberfw "github.com/gofiber/fiber/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tempoxyz/mpp-go/pkg/mpp"
	"github.com/tempoxyz/mpp-go/pkg/server"
)

func TestComposeMiddleware(t *testing.T) {
	t.Parallel()

	payment := newTestServer(t, offersTestMethod{}, "api.example.com", "test-secret-key-minimum-32-byte-secret")
	app := fiberfw.New()
	app.Get("/paid/:id", ComposeMiddleware(
		server.Offer{Mpp: payment, Params: server.ChargeParams{Amount: "1"}},
		server.Offer{Mpp: payment, Params: server.ChargeParams{Amount: "2"}},
	), func(c *fiberfw.Ctx) error {
		return c.SendString(Receipt(c).Reference)
	})

	response, err := app.Test(httptest.NewRequest(http.MethodGet, "/paid/7?q=1", nil))
	require.NoError(t, err)
	response.Body.Close()
	require.Equal(t, http.StatusPaymentRequired, response.StatusCode)
	_, challenges, err := offeredCurrencies(response.Header)
	require.NoError(t, err)
	require.Len(t, challenges, 4)
	scope := challenges[3].Request["_mppx_scope"].(map[string]any)
	assert.Equal(t, "/paid/:id", scope["route"])
	assert.Equal(t, "/paid/7", scope["resource"])
	assert.Equal(t, "q=1", scope["query"])

	request := httptest.NewRequest(http.MethodGet, "/paid/7?q=1", nil)
	authorization, err := mpp.FormatCredential(challenges[3].NewCredential(map[string]any{"type": "test"}))
	require.NoError(t, err)
	request.Header.Set(mpp.HeaderAuthorization, authorization)
	paid, err := app.Test(request)
	require.NoError(t, err)
	body, _ := io.ReadAll(paid.Body)
	paid.Body.Close()
	require.Equal(t, http.StatusOK, paid.StatusCode)
	assert.Equal(t, "0xreceipt-"+testOffers[1], string(body))
	assert.NotEmpty(t, paid.Header.Get(mpp.HeaderPaymentReceipt))

	assert.Panics(t, func() { ComposeMiddleware() })
}
