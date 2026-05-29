package server

import (
	"bufio"
	"bytes"
	"io"
	"net"
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
	if got := paidResponse.Header.Values("Vary"); len(got) != 1 || got[0] != "Authorization" {
		t.Fatalf("Vary = %#v, want Authorization", got)
	}

	body, err := io.ReadAll(paidResponse.Body)
	if err != nil {
		t.Fatalf("io.ReadAll() error = %v", err)
	}
	if got := string(body); got != "did:key:z6Mkrdemo:0xreceipt" {
		t.Fatalf("response body = %q, want %q", got, "did:key:z6Mkrdemo:0xreceipt")
	}
}

func TestChargeMiddleware_PreservesExistingVary(t *testing.T) {
	payment := New(middlewareTestMethod{}, "api.example.com", "secret-key")
	handler := ChargeMiddleware(payment, ChargeParams{Amount: "0.50"})(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Vary", "Accept-Encoding")
		w.WriteHeader(http.StatusNoContent)
	}))
	server := httptest.NewServer(handler)
	defer server.Close()

	challengeResponse, err := http.Get(server.URL)
	if err != nil {
		t.Fatalf("http.Get() error = %v", err)
	}
	defer challengeResponse.Body.Close()

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

	got := paidResponse.Header.Values("Vary")
	if len(got) != 2 || got[0] != "Accept-Encoding" || got[1] != "Authorization" {
		t.Fatalf("Vary = %#v, want Accept-Encoding and Authorization", got)
	}
}

func TestServeVerified_PreservesResponseWriterOptionalInterfaces(t *testing.T) {
	w := newOptionalResponseWriter()
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatalf("wrapped ResponseWriter does not expose http.Flusher")
		}
		if _, ok := w.(http.Hijacker); !ok {
			t.Fatalf("wrapped ResponseWriter does not expose http.Hijacker")
		}
		flusher.Flush()
	})

	serveVerified(
		handler,
		w,
		httptest.NewRequest(http.MethodGet, "/", nil),
		&mpp.Credential{},
		mpp.Success("0xreceipt"),
	)

	if !w.flushed {
		t.Fatalf("Flush was not forwarded to the underlying ResponseWriter")
	}
	if got := w.header.Values("Vary"); len(got) != 1 || got[0] != "Authorization" {
		t.Fatalf("Vary = %#v, want Authorization", got)
	}
}

type optionalResponseWriter struct {
	header  http.Header
	body    bytes.Buffer
	status  int
	flushed bool
}

func newOptionalResponseWriter() *optionalResponseWriter {
	return &optionalResponseWriter{header: make(http.Header)}
}

func (w *optionalResponseWriter) Header() http.Header {
	return w.header
}

func (w *optionalResponseWriter) Write(body []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	return w.body.Write(body)
}

func (w *optionalResponseWriter) WriteHeader(statusCode int) {
	if w.status == 0 {
		w.status = statusCode
	}
}

func (w *optionalResponseWriter) Flush() {
	w.flushed = true
}

func (w *optionalResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return nil, nil, nil
}
