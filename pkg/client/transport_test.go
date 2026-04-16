package client

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tempoxyz/mpp-go/pkg/mpp"
)

// mockMethod implements Method for testing.
type mockMethod struct {
	name string
	cred *mpp.Credential
	err  error
}

func (m *mockMethod) Name() string { return m.name }
func (m *mockMethod) CreateCredential(_ context.Context, ch *mpp.Challenge) (*mpp.Credential, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.cred, nil
}

func newTestCredential(method string) *mpp.Credential {
	return &mpp.Credential{
		Challenge: mpp.ChallengeEcho{
			ID:     "test-id",
			Method: method,
			Intent: "payment",
		},
		Source: "test",
	}
}

func TestTransport_RoundTrip_No402(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	}))
	defer srv.Close()

	tr := NewTransport(nil, nil)
	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	resp, err := tr.RoundTrip(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
}

func TestTransport_RoundTrip_402WithPayment(t *testing.T) {
	challenge := mpp.NewChallenge("secret", "test-realm", "tempo", "payment", nil)
	callCount := 0

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		if r.Header.Get("Authorization") == "" {
			w.Header().Set("WWW-Authenticate", challenge.ToAuthenticate("test-realm"))
			w.WriteHeader(http.StatusPaymentRequired)
			w.Write([]byte("pay me"))
			return
		}
		// Verify we got a Payment authorization header.
		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "Payment ") {
			t.Errorf("expected Payment auth scheme, got %q", auth)
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("paid"))
	}))
	defer srv.Close()

	cred := newTestCredential("tempo")
	method := &mockMethod{name: "tempo", cred: cred}
	tr := NewTransport([]Method{method}, nil)
	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	resp, err := tr.RoundTrip(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "paid" {
		t.Fatalf("expected body 'paid', got %q", string(body))
	}
	if callCount != 2 {
		t.Fatalf("expected 2 calls to server, got %d", callCount)
	}
}

func TestTransport_RoundTrip_402NoMatchingMethod(t *testing.T) {
	challenge := mpp.NewChallenge("secret", "realm", "stripe", "payment", nil)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("WWW-Authenticate", challenge.ToAuthenticate("realm"))
		w.WriteHeader(http.StatusPaymentRequired)
		w.Write([]byte("pay me"))
	}))
	defer srv.Close()

	method := &mockMethod{name: "tempo", cred: newTestCredential("tempo")}
	tr := NewTransport([]Method{method}, nil)
	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	resp, err := tr.RoundTrip(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusPaymentRequired {
		t.Fatalf("expected 402, got %d", resp.StatusCode)
	}
}

func TestTransport_RoundTrip_402ExpiredChallenge(t *testing.T) {
	// Use an expiry in the past.
	challenge := mpp.NewChallenge("secret", "realm", "tempo", "payment", nil,
		mpp.WithExpires("2020-01-01T00:00:00.000Z"))

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("WWW-Authenticate", challenge.ToAuthenticate("realm"))
		w.WriteHeader(http.StatusPaymentRequired)
		w.Write([]byte("expired"))
	}))
	defer srv.Close()

	method := &mockMethod{name: "tempo", cred: newTestCredential("tempo")}
	tr := NewTransport([]Method{method}, nil)
	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	resp, err := tr.RoundTrip(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()
	// Expired challenge → no matching method → return original 402.
	if resp.StatusCode != http.StatusPaymentRequired {
		t.Fatalf("expected 402 for expired challenge, got %d", resp.StatusCode)
	}
}

func TestTransport_RoundTrip_PostWithBody(t *testing.T) {
	challenge := mpp.NewChallenge("secret", "realm", "tempo", "payment", nil)
	callCount := 0

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		body, _ := io.ReadAll(r.Body)
		if r.Header.Get("Authorization") == "" {
			if string(body) != "request-body" {
				t.Errorf("first request body = %q, want %q", string(body), "request-body")
			}
			w.Header().Set("WWW-Authenticate", challenge.ToAuthenticate("realm"))
			w.WriteHeader(http.StatusPaymentRequired)
			return
		}
		// Retry should have the same body.
		if string(body) != "request-body" {
			t.Errorf("retry body = %q, want %q", string(body), "request-body")
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cred := newTestCredential("tempo")
	method := &mockMethod{name: "tempo", cred: cred}
	tr := NewTransport([]Method{method}, nil)
	bodyStr := "request-body"
	req, _ := http.NewRequest(http.MethodPost, srv.URL, strings.NewReader(bodyStr))
	req.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader(bodyStr)), nil
	}
	resp, err := tr.RoundTrip(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if callCount != 2 {
		t.Fatalf("expected 2 calls, got %d", callCount)
	}
}

func TestTransport_RoundTrip_MultipleWWWAuthenticate(t *testing.T) {
	stripeChallenge := mpp.NewChallenge("secret", "realm", "stripe", "payment", map[string]any{"amount": "100"})
	tempoChallenge := mpp.NewChallenge("secret", "realm", "tempo", "payment", map[string]any{"amount": "100"})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			w.Header().Add("WWW-Authenticate", stripeChallenge.ToAuthenticate("realm"))
			w.Header().Add("WWW-Authenticate", tempoChallenge.ToAuthenticate("realm"))
			w.WriteHeader(http.StatusPaymentRequired)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cred := newTestCredential("tempo")
	method := &mockMethod{name: "tempo", cred: cred}
	tr := NewTransport([]Method{method}, nil)
	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	resp, err := tr.RoundTrip(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
}

func TestTransport_RoundTrip_MergedWWWAuthenticate(t *testing.T) {
	challenge := mpp.NewChallenge("secret", "realm", "tempo", "payment", map[string]any{"amount": "100"})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			w.Header().Set("WWW-Authenticate", `Bearer realm="example", `+challenge.ToAuthenticate("realm"))
			w.WriteHeader(http.StatusPaymentRequired)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cred := newTestCredential("tempo")
	method := &mockMethod{name: "tempo", cred: cred}
	tr := NewTransport([]Method{method}, nil)
	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	resp, err := tr.RoundTrip(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
}

func TestTransport_RoundTrip_NonPaymentAuthScheme(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("WWW-Authenticate", `Bearer realm="example"`)
		w.WriteHeader(http.StatusPaymentRequired)
		w.Write([]byte("not payment"))
	}))
	defer srv.Close()

	method := &mockMethod{name: "tempo", cred: newTestCredential("tempo")}
	tr := NewTransport([]Method{method}, nil)
	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	resp, err := tr.RoundTrip(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()
	// Non-Payment scheme → no matching method → return original 402.
	if resp.StatusCode != http.StatusPaymentRequired {
		t.Fatalf("expected 402, got %d", resp.StatusCode)
	}
}

func TestClient_Get(t *testing.T) {
	challenge := mpp.NewChallenge("secret", "realm", "tempo", "payment", nil)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			w.Header().Set("WWW-Authenticate", challenge.ToAuthenticate("realm"))
			w.WriteHeader(http.StatusPaymentRequired)
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("hello"))
	}))
	defer srv.Close()

	cred := newTestCredential("tempo")
	method := &mockMethod{name: "tempo", cred: cred}
	c := New([]Method{method})
	resp, err := c.Get(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
}

func TestClient_Post(t *testing.T) {
	challenge := mpp.NewChallenge("secret", "realm", "tempo", "payment", nil)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("content-type = %q, want application/json", r.Header.Get("Content-Type"))
		}
		if r.Header.Get("Authorization") == "" {
			w.Header().Set("WWW-Authenticate", challenge.ToAuthenticate("realm"))
			w.WriteHeader(http.StatusPaymentRequired)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cred := newTestCredential("tempo")
	method := &mockMethod{name: "tempo", cred: cred}
	c := New([]Method{method})
	body := strings.NewReader(`{"key":"value"}`)
	resp, err := c.Post(context.Background(), srv.URL, "application/json", body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
}

func TestClient_WithHTTPClient(t *testing.T) {
	custom := &http.Client{}
	c := New(nil, WithHTTPClient(custom))
	if c.httpClient != custom {
		t.Fatal("expected custom http client to be set")
	}
}
