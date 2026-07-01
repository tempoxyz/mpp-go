package fiberadapter

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	fiberfw "github.com/gofiber/fiber/v2"
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
	return mpp.Success("0xreceipt", mpp.WithReceiptMethod("tempo")), nil
}

func TestChargeMiddleware_EndToEnd(t *testing.T) {
	t.Parallel()

	payment := server.New(middlewareTestMethod{}, "api.example.com", "secret-key")
	app := fiberfw.New()

	app.Get("/paid", ChargeMiddleware(payment, server.ChargeParams{Amount: "0.50"}), func(c *fiberfw.Ctx) error {
		credential := Credential(c)
		receipt := Receipt(c)
		if !assert.Falsef(t, credential == nil || receipt == nil,
			"expected credential and receipt on fiber context") {
			return *new(error)
		}

		ctxCredential := server.CredentialFromContext(c.UserContext())
		ctxReceipt := server.ReceiptFromContext(c.UserContext())
		if !assert.Falsef(t, ctxCredential == nil || ctxReceipt == nil,
			"expected credential and receipt on user context") {
			return *new(error)
		}

		return c.SendString(credential.Source + ":" + receipt.Reference)
	})

	challengeRequest := httptest.NewRequest(http.MethodGet, "/paid", nil)
	challengeResponse, err := app.Test(challengeRequest)
	if !assert.NoErrorf(t, err,
		"app.Test() error = %v", err) {
		return
	}
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

	paidRequest := httptest.NewRequest(http.MethodGet, "/paid", nil)
	paidRequest.Header.Set("Authorization", credential.ToAuthorization())
	paidResponse, err := app.Test(paidRequest)
	if !assert.NoErrorf(t, err,
		"app.Test() error = %v", err) {
		return
	}
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
		"ReadAll() error = %v", err) {
		return
	}
	{

		got := string(body)
		if !assert.Equalf(t, "did:key:z6Mkrdemo:0xreceipt", got,
			"response body = %q, want %q", got, "did:key:z6Mkrdemo:0xreceipt") {
			return
		}
	}

}

func TestChargeMiddlewareAutoScopesRouteResourceAndQuery(t *testing.T) {
	t.Parallel()

	payment := server.New(middlewareTestMethod{}, "api.example.com", "secret-key")
	app := fiberfw.New()
	app.Get("/paid/:id", ChargeMiddleware(payment, server.ChargeParams{Amount: "0.50"}), func(c *fiberfw.Ctx) error {
		return c.SendString("paid")
	})

	challengeRequest := httptest.NewRequest(http.MethodGet, "/paid/one?view=full", nil)
	challengeResponse, err := app.Test(challengeRequest)
	require.NoError(t, err)
	defer challengeResponse.Body.Close()
	require.Equal(t, http.StatusPaymentRequired, challengeResponse.StatusCode)

	challenge, err := mpp.ParseChallenge(challengeResponse.Header.Get("WWW-Authenticate"))
	require.NoError(t, err)
	scope, ok := challenge.Request["_mppx_scope"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "/paid/:id", scope["route"])
	assert.Equal(t, "/paid/one", scope["resource"])
	assert.Equal(t, "view=full", scope["query"])

	credential := &mpp.Credential{
		Challenge: challenge.ToEcho(),
		Source:    "did:key:z6Mkrdemo",
		Payload:   map[string]any{"type": "hash", "hash": "0xabc123"},
	}
	paidRequest := httptest.NewRequest(http.MethodGet, "/paid/two?view=full", nil)
	paidRequest.Header.Set("Authorization", credential.ToAuthorization())
	paidResponse, err := app.Test(paidRequest)
	require.NoError(t, err)
	defer paidResponse.Body.Close()

	assert.NotEqual(t, http.StatusOK, paidResponse.StatusCode)
	assert.Empty(t, paidResponse.Header.Get("Payment-Receipt"))
}

func TestChargeMiddlewareRejectsTamperedRequestBodyDigest(t *testing.T) {
	t.Parallel()

	payment := server.New(middlewareTestMethod{}, "api.example.com", "secret-key")
	app := fiberfw.New()

	app.Post("/paid", ChargeMiddleware(payment, server.ChargeParams{Amount: "0.50"}), func(c *fiberfw.Ctx) error {
		return c.SendString("paid")
	})

	const originalBody = `{"query":"paid"}`
	challengeRequest := httptest.NewRequest(http.MethodPost, "/paid", strings.NewReader(originalBody))
	challengeResponse, err := app.Test(challengeRequest)
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
	paidRequest := httptest.NewRequest(http.MethodPost, "/paid", strings.NewReader(`{"query":"tampered"}`))
	paidRequest.Header.Set("Authorization", credential.ToAuthorization())
	paidResponse, err := app.Test(paidRequest)
	require.NoError(t, err)
	defer paidResponse.Body.Close()

	assert.Equal(t, http.StatusBadRequest, paidResponse.StatusCode)
}

func TestChargeMiddlewarePreservesVerifiedRequestBody(t *testing.T) {
	t.Parallel()

	payment := server.New(middlewareTestMethod{}, "api.example.com", "secret-key")
	app := fiberfw.New()

	app.Post("/paid", ChargeMiddleware(payment, server.ChargeParams{Amount: "0.50"}), func(c *fiberfw.Ctx) error {
		return c.SendString(string(c.Body()))
	})

	const originalBody = `{"query":"paid"}`
	challengeRequest := httptest.NewRequest(http.MethodPost, "/paid", strings.NewReader(originalBody))
	challengeResponse, err := app.Test(challengeRequest)
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
	paidRequest := httptest.NewRequest(http.MethodPost, "/paid", strings.NewReader(originalBody))
	paidRequest.Header.Set("Authorization", credential.ToAuthorization())
	paidResponse, err := app.Test(paidRequest)
	require.NoError(t, err)
	defer paidResponse.Body.Close()
	require.Equal(t, http.StatusOK, paidResponse.StatusCode)

	body, err := io.ReadAll(paidResponse.Body)
	require.NoError(t, err)
	assert.Equal(t, originalBody, string(body))
}

func TestChargeMiddlewareRejectsCRLFChallengeDescription(t *testing.T) {
	t.Parallel()

	payment := server.New(middlewareTestMethod{}, "api.example.com", "secret-key")
	app := fiberfw.New()

	app.Get("/paid", ChargeMiddleware(payment, server.ChargeParams{
		Amount:      "0.50",
		Description: "Line one\r\nLine two",
	}), func(c *fiberfw.Ctx) error {
		require.Fail(t, "handler should not be called")
		return *new(error)
	})

	challengeRequest := httptest.NewRequest(http.MethodGet, "/paid", nil)
	challengeResponse, err := app.Test(challengeRequest)
	if !assert.NoErrorf(t, err,
		"app.Test() error = %v", err) {
		return
	}

	defer challengeResponse.Body.Close()

	require.Equal(t, http.StatusBadRequest, challengeResponse.StatusCode)
	assert.Empty(t, challengeResponse.Header.Get("WWW-Authenticate"))

	var problem struct {
		Type string `json:"type"`
	}
	require.NoError(t, json.NewDecoder(challengeResponse.Body).Decode(&problem))
	assert.Equal(t, string(mpp.ErrorTypeBadRequest), problem.Type)
}
