//go:build integration

package tests

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	ginfw "github.com/gin-gonic/gin"
	"github.com/go-chi/chi/v5"
	fiberfw "github.com/gofiber/fiber/v2"
	echofw "github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"
	mppclient "github.com/tempoxyz/mpp-go/pkg/client"
	"github.com/tempoxyz/mpp-go/pkg/mpp"
	mppserver "github.com/tempoxyz/mpp-go/pkg/server"
	echoadapter "github.com/tempoxyz/mpp-go/pkg/server/echo"
	fiberadapter "github.com/tempoxyz/mpp-go/pkg/server/fiber"
	ginadapter "github.com/tempoxyz/mpp-go/pkg/server/gin"
	"github.com/tempoxyz/mpp-go/pkg/tempo"
	chargeclient "github.com/tempoxyz/mpp-go/pkg/tempo/client"
	chargeserver "github.com/tempoxyz/mpp-go/pkg/tempo/server"
)

var adapterChargeParams = mppserver.ChargeParams{Amount: "0.50", Description: "Adapter integration"}

// TestIntegrationAdapters pays one request through each framework adapter
// against the live node.
func TestIntegrationAdapters(t *testing.T) {
	adapters := map[string]func(t *testing.T, payment *mppserver.Mpp) string{
		"net/http": func(t *testing.T, payment *mppserver.Mpp) string {
			srv := httptest.NewServer(mppserver.ChargeMiddleware(payment, adapterChargeParams)(http.HandlerFunc(
				func(w http.ResponseWriter, r *http.Request) {
					writePaidBody(w, mppserver.CredentialFromContext(r.Context()), mppserver.ReceiptFromContext(r.Context()))
				})))
			t.Cleanup(srv.Close)
			return srv.URL
		},
		"chi": func(t *testing.T, payment *mppserver.Mpp) string {
			router := chi.NewRouter()
			router.With(mppserver.ChargeMiddleware(payment, adapterChargeParams)).Get("/paid",
				func(w http.ResponseWriter, r *http.Request) {
					writePaidBody(w, mppserver.CredentialFromContext(r.Context()), mppserver.ReceiptFromContext(r.Context()))
				})
			srv := httptest.NewServer(router)
			t.Cleanup(srv.Close)
			return srv.URL
		},
		"gin": func(t *testing.T, payment *mppserver.Mpp) string {
			ginfw.SetMode(ginfw.TestMode)
			router := ginfw.New()
			router.GET("/paid", ginadapter.ChargeMiddleware(payment, adapterChargeParams), func(c *ginfw.Context) {
				writePaidBody(c.Writer, ginadapter.Credential(c), ginadapter.Receipt(c))
			})
			srv := httptest.NewServer(router)
			t.Cleanup(srv.Close)
			return srv.URL
		},
		"echo": func(t *testing.T, payment *mppserver.Mpp) string {
			e := echofw.New()
			e.GET("/paid", func(c echofw.Context) error {
				writePaidBody(c.Response(), echoadapter.Credential(c), echoadapter.Receipt(c))
				return nil
			}, echoadapter.ChargeMiddleware(payment, adapterChargeParams))
			srv := httptest.NewServer(e)
			t.Cleanup(srv.Close)
			return srv.URL
		},
		"fiber": func(t *testing.T, payment *mppserver.Mpp) string {
			app := fiberfw.New(fiberfw.Config{DisableStartupMessage: true})
			app.Get("/paid", fiberadapter.ChargeMiddleware(payment, adapterChargeParams), func(c *fiberfw.Ctx) error {
				credential, receipt := fiberadapter.Credential(c), fiberadapter.Receipt(c)
				return c.JSON(map[string]any{"payer": credential.Source, "receipt": receipt.Reference})
			})
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			require.NoError(t, err)
			done := make(chan error, 1)
			go func() { done <- app.Listener(listener) }()
			t.Cleanup(func() {
				_ = app.Shutdown()
				<-done
			})
			return "http://" + listener.Addr().String()
		},
	}

	for _, name := range []string{"net/http", "chi", "gin", "echo", "fiber"} {
		t.Run(name, func(t *testing.T) {
			payment := newLiveCharge(t, tempo.NewMemoryStore())
			url := adapters[name](t, payment) + "/paid"
			assertPaidRoute(t, url)
		})
	}
}

// newLiveCharge returns an Mpp that charges devnet tokens on the live node.
func newLiveCharge(t *testing.T, store tempo.Store) *mppserver.Mpp {
	t.Helper()
	rpcURL := integrationRPCURL(t)
	chainID := waitForRPC(t, context.Background(), tempo.NewRPCClient(rpcURL))
	method, err := chargeserver.MethodFromConfig(chargeserver.Config{
		Store:     store,
		RPCURL:    rpcURL,
		ChainID:   int64(chainID),
		Currency:  integrationCurrency,
		Recipient: integrationRecipient,
	})
	require.NoError(t, err)
	return newTestServer(t, method, integrationRealm, integrationSecretKey)
}

// newSharedPayerMethod returns a client method that pays with sharedPayer.
func newSharedPayerMethod(t *testing.T, credentialType tempo.CredentialType) *chargeclient.Method {
	t.Helper()
	rpcURL := integrationRPCURL(t)
	chainID := waitForRPC(t, context.Background(), tempo.NewRPCClient(rpcURL))
	method, err := chargeclient.New(chargeclient.Config{
		PaymentPolicy:  func(context.Context, tempo.ChargeRequest) error { return nil },
		Signer:         sharedPayer,
		RPCURL:         rpcURL,
		ChainID:        int64(chainID),
		CredentialType: credentialType,
	})
	require.NoError(t, err)
	return method
}

// assertPaidRoute checks that url answers 402 without a Credential and 200
// with a settled Receipt after the shared payer pays.
func assertPaidRoute(t *testing.T, url string) {
	t.Helper()
	ctx := context.Background()

	unpaid, err := http.Get(url)
	require.NoError(t, err)
	unpaid.Body.Close()
	require.Equal(t, http.StatusPaymentRequired, unpaid.StatusCode)
	_, err = mpp.ParseChallenge(unpaid.Header.Get("WWW-Authenticate"))
	require.NoError(t, err)

	client := mppclient.New([]mppclient.Method{newSharedPayerMethod(t, "")})
	response, err := client.Get(ctx, url)
	require.NoError(t, err)
	defer response.Body.Close()
	require.Equal(t, http.StatusOK, response.StatusCode)

	receipt, err := mpp.ParsePaymentReceipt(response.Header.Get("Payment-Receipt"))
	require.NoError(t, err)
	require.Equal(t, "success", receipt.Status)
	require.Equal(t, tempo.MethodName, receipt.Method)

	var body struct {
		Payer   string `json:"payer"`
		Receipt string `json:"receipt"`
	}
	require.NoError(t, json.NewDecoder(response.Body).Decode(&body))
	require.NotEmpty(t, body.Payer)
	require.Equal(t, receipt.Reference, body.Receipt)
	waitForReceipt(t, ctx, tempo.NewRPCClient(integrationRPCURL(t)), receipt.Reference)
}

func writePaidBody(w http.ResponseWriter, credential *mpp.Credential, receipt *mpp.Receipt) {
	if credential == nil || receipt == nil {
		http.Error(w, errors.New("missing payment context").Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"payer": credential.Source, "receipt": receipt.Reference})
}
