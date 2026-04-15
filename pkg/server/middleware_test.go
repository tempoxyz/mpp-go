package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/tempoxyz/mpp-go/pkg/mpp"
)

type middlewareTestMethod struct{}

func (middlewareTestMethod) Name() string { return "tempo" }

func (middlewareTestMethod) Intents() map[string]Intent {
	return map[string]Intent{"charge": verifyTestIntent{}}
}

func TestChargeMiddleware_EndToEnd(t *testing.T) {
	payment := New(middlewareTestMethod{}, "api.example.com", "secret-key")
	handler := ChargeMiddleware(payment, ChargeParams{Amount: "0.50"})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, CredentialFromContext(r.Context()).Source+":"+ReceiptFromContext(r.Context()).Reference)
	}))
	server := httptest.NewServer(handler)
	defer server.Close()

	challengeResponse, err := http.Get(server.URL)
	if err != nil {
		t.Fatalf("http.Get() error = %v", err)
	}
	defer challengeResponse.Body.Close()

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

	retry, err := http.NewRequest(http.MethodGet, server.URL, nil)
	if err != nil {
		t.Fatalf("http.NewRequest() error = %v", err)
	}
	retry.Header.Set("Authorization", credential.ToAuthorization())

	paidResponse, err := http.DefaultClient.Do(retry)
	if err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	defer paidResponse.Body.Close()

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
		t.Fatalf("io.ReadAll() error = %v", err)
	}
	if got := string(body); got != "did:key:z6Mkrdemo:0xreceipt" {
		t.Fatalf("response body = %q, want %q", got, "did:key:z6Mkrdemo:0xreceipt")
	}
}
