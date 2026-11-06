package echoadapter

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tempoxyz/mpp-go/pkg/internal/mpptest"

	echofw "github.com/labstack/echo/v5"
	"github.com/labstack/echo/v5/middleware"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tempoxyz/mpp-go/pkg/mpp"
	"github.com/tempoxyz/mpp-go/pkg/server"
)

type middlewareTestMethod struct{}

func (middlewareTestMethod) Name() string { return "tempo" }

func (middlewareTestMethod) Intents() map[string]server.Intent {
	return map[string]server.Intent{"charge": verifyTestIntent{}}
}

type verifyTestIntent struct{}

func (verifyTestIntent) Name() string { return "charge" }

func (verifyTestIntent) Verify(_ context.Context, _ *mpp.Credential, _ map[string]any) (*mpp.Receipt, error) {
	return mpp.Success("tempo", "0xreceipt"), nil
}

func TestChargeMiddleware_EndToEnd(t *testing.T) {
	t.Parallel()

	e := echofw.New()
	var challengeCommitted bool
	var challengeStatus int
	e.Use(func(next echofw.HandlerFunc) echofw.HandlerFunc {
		return func(c *echofw.Context) error {
			err := next(c)
			if c.Request().Header.Get("Authorization") == "" {
				response, unwrapErr := echofw.UnwrapResponse(c.Response())
				require.NoError(t, unwrapErr)
				challengeCommitted = response.Committed
				challengeStatus = response.Status
			}
			return err
		}
	})
	payment := newTestServer(t, middlewareTestMethod{}, "api.example.com", "test-secret-key-minimum-32-byte-secret")
	e.GET("/paid", func(c *echofw.Context) error {
		if c.Request().Header.Get("X-Downstream-Error") != "" {
			return c.String(http.StatusInternalServerError, "failed")
		}
		if c.Request().Header.Get("X-Downstream-Redirect") != "" {
			return c.Redirect(http.StatusFound, "/elsewhere")
		}
		credential := Credential(c)
		receipt := Receipt(c)
		if !assert.Falsef(t, credential == nil || receipt == nil,
			"expected credential and receipt on echo context") {
			return *new(error)
		}

		ctxCredential := server.CredentialFromContext(c.Request().Context())
		ctxReceipt := server.ReceiptFromContext(c.Request().Context())
		if !assert.Falsef(t, ctxCredential == nil || ctxReceipt == nil,
			"expected credential and receipt on request context") {
			return *new(error)
		}

		return c.String(http.StatusOK, credential.Source+":"+receipt.Reference)
	}, ChargeMiddleware(payment, server.ChargeParams{Amount: "0.50"}))

	challengeRequest := httptest.NewRequest(http.MethodGet, "/paid", nil)
	challengeResponse := httptest.NewRecorder()
	e.ServeHTTP(challengeResponse, challengeRequest)
	if !assert.Equalf(t, http.StatusPaymentRequired, challengeResponse.Code,
		"challenge status = %d, want %d", challengeResponse.Code, http.StatusPaymentRequired) {
		return
	}
	if !assert.True(t, challengeCommitted,
		"challenge response was not marked committed in echo") {
		return
	}
	if !assert.Equalf(t, http.StatusPaymentRequired, challengeStatus,
		"echo challenge status = %d, want %d", challengeStatus, http.StatusPaymentRequired) {
		return
	}

	challenge, err := mpp.ParseChallenge(challengeResponse.Header().Get("WWW-Authenticate"))
	if !assert.NoErrorf(t, err,
		"ParseChallenge() error = %v", err) {
		return
	}

	credential := &mpp.Credential{
		Challenge: challenge.ToEcho(),
		Source:    "did:key:z6Mkrdemo",
		Payload:   map[string]any{"type": "hash", "hash": "0xabc123"},
	}

	paidRequest := httptest.NewRequest(http.MethodGet, "/paid", nil)
	paidRequest.Header.Set("Authorization", mpptest.FormatCredential(credential))
	paidResponse := httptest.NewRecorder()
	e.ServeHTTP(paidResponse, paidRequest)
	if !assert.Equalf(t, http.StatusOK, paidResponse.Code,
		"paid status = %d, want %d", paidResponse.Code, http.StatusOK) {
		return
	}

	receipt, err := mpp.ParseReceipt(paidResponse.Header().Get("Payment-Receipt"))
	if !assert.NoErrorf(t, err,
		"ParseReceipt() error = %v", err) {
		return
	}
	if !assert.Equalf(t, "0xreceipt", receipt.Reference,
		"receipt reference = %q, want %q", receipt.Reference, "0xreceipt") {
		return
	}

	if got := paidResponse.Body.String(); got != "did:key:z6Mkrdemo:0xreceipt" {
		assert.Failf(t, "", "response body = %q, want %q", got, "did:key:z6Mkrdemo:0xreceipt")
		return
	}

	failedRequest := httptest.NewRequest(http.MethodGet, "/paid", nil)
	failedRequest.Header.Set("Authorization", mpptest.FormatCredential(credential))
	failedRequest.Header.Set("X-Downstream-Error", "true")
	failedResponse := httptest.NewRecorder()
	e.ServeHTTP(failedResponse, failedRequest)
	require.Equal(t, http.StatusInternalServerError, failedResponse.Code)
	assert.Empty(t, failedResponse.Header().Get(mpp.HeaderPaymentReceipt))

	redirectRequest := httptest.NewRequest(http.MethodGet, "/paid", nil)
	redirectRequest.Header.Set("Authorization", mpptest.FormatCredential(credential))
	redirectRequest.Header.Set("X-Downstream-Redirect", "true")
	redirectResponse := httptest.NewRecorder()
	e.ServeHTTP(redirectResponse, redirectRequest)
	require.Equal(t, http.StatusFound, redirectResponse.Code)
	assert.Equal(t, "/elsewhere", redirectResponse.Header().Get("Location"))
	assert.Empty(t, redirectResponse.Header().Get(mpp.HeaderPaymentReceipt))
}

func TestChargeMiddlewareAutoScopesRoute(t *testing.T) {
	t.Parallel()

	e := echofw.New()
	payment := newTestServer(t, middlewareTestMethod{}, "api.example.com", "test-secret-key-minimum-32-byte-secret")
	e.GET("/paid/:id", func(c *echofw.Context) error {
		return c.String(http.StatusOK, "paid")
	}, ChargeMiddleware(payment, server.ChargeParams{Amount: "0.50"}))

	challengeRequest := httptest.NewRequest(http.MethodGet, "/paid/one?view=full", nil)
	challengeResponse := httptest.NewRecorder()
	e.ServeHTTP(challengeResponse, challengeRequest)
	require.Equal(t, http.StatusPaymentRequired, challengeResponse.Code)

	challenge, err := mpp.ParseChallenge(challengeResponse.Header().Get("WWW-Authenticate"))
	require.NoError(t, err)
	// The route template is bound, as in mppx; the resource and query are not.
	assert.Equal(t, "GET /paid/:id", challenge.Opaque["_mppx_scope"])
	assert.NotContains(t, challenge.Request, "_mppx_scope")

	credential := &mpp.Credential{
		Challenge: challenge.ToEcho(),
		Source:    "did:key:z6Mkrdemo",
		Payload:   map[string]any{"type": "hash", "hash": "0xabc123"},
	}
	paidRequest := httptest.NewRequest(http.MethodGet, "/paid/two?view=full", nil)
	paidRequest.Header.Set("Authorization", mpptest.FormatCredential(credential))
	paidResponse := httptest.NewRecorder()
	e.ServeHTTP(paidResponse, paidRequest)

	assert.Equal(t, http.StatusOK, paidResponse.Code)
	assert.NotEmpty(t, paidResponse.Header().Get("Payment-Receipt"))
}

func TestChargeMiddlewareRejectsTamperedRequestBodyDigest(t *testing.T) {
	t.Parallel()

	e := echofw.New()
	payment := newTestServer(t, middlewareTestMethod{}, "api.example.com", "test-secret-key-minimum-32-byte-secret")
	e.POST("/paid", func(c *echofw.Context) error {
		_, _ = io.ReadAll(c.Request().Body)
		return c.String(http.StatusOK, "paid")
	}, ChargeMiddleware(payment, server.ChargeParams{Amount: "0.50"}))

	const originalBody = `{"query":"paid"}`
	challengeRequest := httptest.NewRequest(http.MethodPost, "/paid", strings.NewReader(originalBody))
	challengeResponse := httptest.NewRecorder()
	e.ServeHTTP(challengeResponse, challengeRequest)
	require.Equal(t, http.StatusPaymentRequired, challengeResponse.Code)

	challenge, err := mpp.ParseChallenge(challengeResponse.Header().Get("WWW-Authenticate"))
	require.NoError(t, err)
	assert.Equal(t, mpp.ComputeBodyDigest([]byte(originalBody)), challenge.Digest)

	credential := &mpp.Credential{
		Challenge: challenge.ToEcho(),
		Source:    "did:key:z6Mkrdemo",
		Payload:   map[string]any{"type": "hash", "hash": "0xabc123"},
	}
	paidRequest := httptest.NewRequest(http.MethodPost, "/paid", strings.NewReader(`{"query":"tampered"}`))
	paidRequest.Header.Set("Authorization", mpptest.FormatCredential(credential))
	paidResponse := httptest.NewRecorder()
	e.ServeHTTP(paidResponse, paidRequest)

	assert.Equal(t, http.StatusPaymentRequired, paidResponse.Code)
}

func TestChargeMiddlewarePreservesVerifiedRequestBody(t *testing.T) {
	t.Parallel()

	e := echofw.New()
	payment := newTestServer(t, middlewareTestMethod{}, "api.example.com", "test-secret-key-minimum-32-byte-secret")
	e.POST("/paid", func(c *echofw.Context) error {
		body, _ := io.ReadAll(c.Request().Body)
		return c.String(http.StatusOK, string(body))
	}, ChargeMiddleware(payment, server.ChargeParams{Amount: "0.50"}))

	const originalBody = `{"query":"paid"}`
	challengeRequest := httptest.NewRequest(http.MethodPost, "/paid", strings.NewReader(originalBody))
	challengeResponse := httptest.NewRecorder()
	e.ServeHTTP(challengeResponse, challengeRequest)
	require.Equal(t, http.StatusPaymentRequired, challengeResponse.Code)

	challenge, err := mpp.ParseChallenge(challengeResponse.Header().Get("WWW-Authenticate"))
	require.NoError(t, err)
	credential := &mpp.Credential{
		Challenge: challenge.ToEcho(),
		Source:    "did:key:z6Mkrdemo",
		Payload:   map[string]any{"type": "hash", "hash": "0xabc123"},
	}
	paidRequest := httptest.NewRequest(http.MethodPost, "/paid", strings.NewReader(originalBody))
	paidRequest.Header.Set("Authorization", mpptest.FormatCredential(credential))
	paidResponse := httptest.NewRecorder()
	e.ServeHTTP(paidResponse, paidRequest)

	require.Equal(t, http.StatusOK, paidResponse.Code)
	assert.Equal(t, originalBody, paidResponse.Body.String())
}

func newTestServer(t *testing.T, method server.Method, realm, secretKey string, opts ...server.Option) *server.Mpp {
	t.Helper()
	payment, err := server.New(method, realm, secretKey, opts...)
	require.NoError(t, err)
	return payment
}

func TestOversizedBodyRejectedBeforePayment(t *testing.T) {
	router := echofw.New()
	router.POST("/", func(c *echofw.Context) error { t.Fatal("handler called"); return nil }, ChargeMiddleware(nil, server.ChargeParams{}))
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(strings.Repeat("x", server.MaxRequestBodyBytes+1)))
	req.ContentLength = -1
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)
}

func TestChargeMiddlewareRespectsCallerMppxScope(t *testing.T) {
	t.Parallel()

	e := echofw.New()
	payment := newTestServer(t, middlewareTestMethod{}, "api.example.com", "test-secret-key-minimum-32-byte-secret")
	e.GET("/paid/:id", func(c *echofw.Context) error { return nil }, ChargeMiddleware(payment, server.ChargeParams{
		Amount:    "0.50",
		MppxScope: "custom",
	}))

	resp := httptest.NewRecorder()
	e.ServeHTTP(resp, httptest.NewRequest(http.MethodGet, "/paid/one?view=full", nil))
	require.Equal(t, http.StatusPaymentRequired, resp.Code)
	challenge, err := mpp.ParseChallenge(resp.Header().Get("WWW-Authenticate"))
	require.NoError(t, err)
	assert.Equal(t, "custom", challenge.Opaque["_mppx_scope"])
}

func TestChargeMiddlewareV5ResponseLifecycle(t *testing.T) {
	for _, test := range []struct {
		name    string
		handler echofw.HandlerFunc
		status  int
		receipt bool
	}{
		{"no content", func(c *echofw.Context) error { return c.NoContent(http.StatusNoContent) }, http.StatusNoContent, true},
		{"no content failure", func(c *echofw.Context) error { return c.NoContent(http.StatusServiceUnavailable) }, http.StatusServiceUnavailable, false},
		{"JSON success", func(c *echofw.Context) error { return c.JSON(http.StatusCreated, map[string]string{"result": "paid"}) }, http.StatusCreated, true},
		{"returned error", func(c *echofw.Context) error {
			return echofw.NewHTTPError(http.StatusServiceUnavailable, "unavailable")
		}, http.StatusServiceUnavailable, false},
		{"stream flush", func(c *echofw.Context) error {
			if _, err := io.WriteString(c.Response(), "paid stream"); err != nil {
				return err
			}
			return http.NewResponseController(c.Response()).Flush()
		}, http.StatusOK, true},
		{"before hook changes status", func(c *echofw.Context) error {
			response, err := echofw.UnwrapResponse(c.Response())
			if err != nil {
				return err
			}
			response.Before(func() { response.Status = http.StatusServiceUnavailable })
			return c.String(http.StatusOK, "unavailable")
		}, http.StatusServiceUnavailable, false},
	} {
		for _, gzip := range []bool{false, true} {
			name := test.name
			if gzip {
				name += "/gzip"
			}
			t.Run(name, func(t *testing.T) {
				e := echofw.New()
				if gzip {
					e.Use(middleware.GzipWithConfig(middleware.GzipConfig{MinLength: 128}))
				}
				payment := newTestServer(t, middlewareTestMethod{}, "api.example.com", "test-secret-key-minimum-32-byte-secret")
				var response *echofw.Response
				e.GET("/paid", func(c *echofw.Context) error {
					var err error
					response, err = echofw.UnwrapResponse(c.Response())
					require.NoError(t, err)
					return test.handler(c)
				}, ChargeMiddleware(payment, server.ChargeParams{Amount: "0.50"}))
				challenge := httptest.NewRecorder()
				e.ServeHTTP(challenge, httptest.NewRequest(http.MethodGet, "/paid", nil))
				require.Equal(t, http.StatusPaymentRequired, challenge.Code)
				request := httptest.NewRequest(http.MethodGet, "/paid", nil)
				request.Header.Set("Accept-Encoding", "gzip")
				request.Header.Set(mpp.HeaderAuthorization, settledAuthorization(t, challenge.Header().Get(mpp.HeaderWWWAuthenticate)))
				result := httptest.NewRecorder()
				e.ServeHTTP(result, request)
				require.Equal(t, test.status, result.Code)
				require.NotNil(t, response)
				assert.True(t, response.Committed)
				assert.Equal(t, test.status, response.Status)
				assert.Equal(t, test.receipt, result.Header().Get(mpp.HeaderPaymentReceipt) != "")
				if test.name == "stream flush" {
					assert.True(t, result.Flushed)
				}
			})
		}
	}
}

func TestUnsupportedResponseWriterRejectedBeforePayment(t *testing.T) {
	for _, test := range []struct {
		name       string
		middleware func(*server.Mpp) echofw.MiddlewareFunc
	}{
		{"charge", func(payment *server.Mpp) echofw.MiddlewareFunc {
			return ChargeMiddleware(payment, server.ChargeParams{Amount: "0.50"})
		}},
		{"compose", func(payment *server.Mpp) echofw.MiddlewareFunc {
			return ComposeMiddleware(server.Offer{Mpp: payment, Params: server.ChargeParams{Amount: "0.50"}})
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			settled := 0
			payment := newTestServer(t, unformattableReceiptMethod{&settled}, "api.example.com", "test-secret-key-minimum-32-byte-secret")
			middleware := test.middleware(payment)
			e := echofw.New()
			e.GET("/paid", func(c *echofw.Context) error { return c.NoContent(http.StatusOK) }, middleware)
			challenge := httptest.NewRecorder()
			e.ServeHTTP(challenge, httptest.NewRequest(http.MethodGet, "/paid", nil))
			require.Equal(t, http.StatusPaymentRequired, challenge.Code)
			request := httptest.NewRequest(http.MethodGet, "/paid", nil)
			request.Header.Set(mpp.HeaderAuthorization, settledAuthorization(t, challenge.Header().Get(mpp.HeaderWWWAuthenticate)))
			ctx := e.NewContext(request, httptest.NewRecorder())
			ctx.SetResponse(httptest.NewRecorder()) // No Unwrap method or Echo response tracking.
			called := false
			err := middleware(func(*echofw.Context) error { called = true; return nil })(ctx)
			require.Error(t, err)
			assert.Zero(t, settled)
			assert.False(t, called)
		})
	}
}
