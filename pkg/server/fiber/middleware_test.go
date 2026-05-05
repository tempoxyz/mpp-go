package fiberadapter

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	fiberfw "github.com/gofiber/fiber/v2"
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
		if credential == nil || receipt == nil {
			t.Fatalf("expected credential and receipt on fiber context")
		}

		ctxCredential := server.CredentialFromContext(c.UserContext())
		ctxReceipt := server.ReceiptFromContext(c.UserContext())
		if ctxCredential == nil || ctxReceipt == nil {
			t.Fatalf("expected credential and receipt on user context")
		}

		return c.SendString(credential.Source + ":" + receipt.Reference)
	})

	challengeRequest := httptest.NewRequest(http.MethodGet, "/paid", nil)
	challengeResponse, err := app.Test(challengeRequest)
	if err != nil {
		t.Fatalf("app.Test() error = %v", err)
	}

	if challengeResponse.StatusCode != http.StatusPaymentRequired {
		t.Fatalf("challenge status = %d, want %d", challengeResponse.StatusCode, http.StatusPaymentRequired)
	}

	challenge, err := mpp.ParseChallenge(challengeResponse.Header.Get("WWW-Authenticate"))
	if err != nil {
		t.Fatalf("ParseChallenge() error = %v", err)
	}

	credential := &mpp.Credential{
		Challenge: challenge.ToEcho(),
		Source:    "did:key:z6Mkrdemo",
		Payload:   map[string]any{"type": "hash", "hash": "0xabc123"},
	}

	paidRequest := httptest.NewRequest(http.MethodGet, "/paid", nil)
	paidRequest.Header.Set("Authorization", credential.ToAuthorization())
	paidResponse, err := app.Test(paidRequest)
	if err != nil {
		t.Fatalf("app.Test() error = %v", err)
	}

	if paidResponse.StatusCode != http.StatusOK {
		t.Fatalf("paid status = %d, want %d", paidResponse.StatusCode, http.StatusOK)
	}

	receipt, err := mpp.ParsePaymentReceipt(paidResponse.Header.Get("Payment-Receipt"))
	if err != nil {
		t.Fatalf("ParsePaymentReceipt() error = %v", err)
	}
	if receipt.Reference != "0xreceipt" {
		t.Fatalf("receipt reference = %q, want %q", receipt.Reference, "0xreceipt")
	}

	body, err := io.ReadAll(paidResponse.Body)
	if err != nil {
		t.Fatalf("ReadAll() error = %v", err)
	}
	if got := string(body); got != "did:key:z6Mkrdemo:0xreceipt" {
		t.Fatalf("response body = %q, want %q", got, "did:key:z6Mkrdemo:0xreceipt")
	}
}
