package echoadapter

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	echofw "github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
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

	e := echofw.New()
	var challengeCommitted bool
	var challengeStatus int
	e.Use(func(next echofw.HandlerFunc) echofw.HandlerFunc {
		return func(c echofw.Context) error {
			err := next(c)
			if c.Request().Header.Get("Authorization") == "" {
				challengeCommitted = c.Response().Committed
				challengeStatus = c.Response().Status
			}
			return err
		}
	})
	payment := server.New(middlewareTestMethod{}, "api.example.com", "secret-key")
	e.GET("/paid", func(c echofw.Context) error {
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
	paidRequest.Header.Set("Authorization", credential.ToAuthorization())
	paidResponse := httptest.NewRecorder()
	e.ServeHTTP(paidResponse, paidRequest)
	if !assert.Equalf(t, http.StatusOK, paidResponse.Code,
		"paid status = %d, want %d", paidResponse.Code, http.StatusOK) {
		return
	}

	receipt, err := mpp.ParsePaymentReceipt(paidResponse.Header().Get("Payment-Receipt"))
	if !assert.NoErrorf(t, err,
		"ParsePaymentReceipt() error = %v", err) {
		return
	}
	if !assert.Equalf(t, "0xreceipt", receipt.Reference,
		"receipt reference = %q, want %q", receipt.Reference, "0xreceipt") {
		return
	}
	if got := paidResponse.Header().Get("Cache-Control"); got != "private" {
		t.Fatalf("Cache-Control = %q, want private", got)
	}

	if got := paidResponse.Body.String(); got != "did:key:z6Mkrdemo:0xreceipt" {
		assert.Failf(t, "", "response body = %q, want %q", got, "did:key:z6Mkrdemo:0xreceipt")
		return
	}
}
