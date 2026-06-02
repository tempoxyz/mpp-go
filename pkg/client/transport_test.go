package client

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	urlpkg "net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/tempoxyz/mpp-go/pkg/mpp"
)

// mockMethod implements Method for testing.
type mockMethod struct {
	name  string
	cred  *mpp.Credential
	err   error
	calls int
}

func (m *mockMethod) Name() string { return m.name }
func (m *mockMethod) CreateCredential(_ context.Context, ch *mpp.Challenge) (*mpp.Credential, error) {
	m.calls++
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

func challengeForURL(t *testing.T, rawURL, method string, request map[string]any, opts ...mpp.ChallengeOption) *mpp.Challenge {
	t.Helper()
	parsedURL, err := urlpkg.Parse(rawURL)
	if !assert.NoErrorf(t, err,
		"url.Parse(%q) error = %v", rawURL, err) {
		return *new(*mpp.Challenge)
	}

	return mpp.NewChallenge("secret", parsedURL.Host, method, "payment", request, opts...)
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
	if !assert.NoErrorf(t, err,
		"unexpected error: %v", err) {
		return
	}

	defer resp.Body.Close()
	if !assert.Equalf(t, http.StatusOK, resp.StatusCode,
		"expected 200, got %d", resp.StatusCode) {
		return
	}

}

func TestTransport_RoundTrip_402WithPayment(t *testing.T) {
	callCount := 0
	var challenge *mpp.Challenge

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		if r.Header.Get("Authorization") == "" {
			w.Header().Set("WWW-Authenticate", challenge.ToAuthenticate(challenge.Realm))
			w.WriteHeader(http.StatusPaymentRequired)
			w.Write([]byte("pay me"))
			return
		}
		// Verify we got a Payment authorization header.
		auth := r.Header.Get("Authorization")
		assert.Truef(t, strings.HasPrefix(auth, "Payment "),
			"expected Payment auth scheme, got %q", auth)

		w.WriteHeader(http.StatusOK)
		w.Write([]byte("paid"))
	}))
	defer srv.Close()
	challenge = challengeForURL(t, srv.URL, "tempo", nil)

	cred := newTestCredential("tempo")
	method := &mockMethod{name: "tempo", cred: cred}
	tr := NewTransport([]Method{method}, nil)
	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	resp, err := tr.RoundTrip(req)
	if !assert.NoErrorf(t, err,
		"unexpected error: %v", err) {
		return
	}

	defer resp.Body.Close()
	if !assert.Equalf(t, http.StatusOK, resp.StatusCode,
		"expected 200, got %d", resp.StatusCode) {
		return
	}

	body, _ := io.ReadAll(resp.Body)
	if !assert.Equalf(t, "paid", string(body),
		"expected body 'paid', got %q", string(body)) {
		return
	}
	if !assert.EqualValuesf(t, 2, callCount,
		"expected 2 calls to server, got %d", callCount) {
		return
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
	if !assert.NoErrorf(t, err,
		"unexpected error: %v", err) {
		return
	}

	defer resp.Body.Close()
	if !assert.Equalf(t, http.StatusPaymentRequired, resp.StatusCode,
		"expected 402, got %d", resp.StatusCode) {
		return
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
	if !assert.NoErrorf(t, err,
		"unexpected error: %v", err) {
		return
	}

	defer resp.Body.Close()
	if !
	// Expired challenge → no matching method → return original 402.
	assert.Equalf(t, http.StatusPaymentRequired, resp.StatusCode,
		"expected 402 for expired challenge, got %d", resp.StatusCode) {
		return
	}

}

func TestTransport_RoundTrip_PostWithBody(t *testing.T) {
	callCount := 0
	var challenge *mpp.Challenge

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		body, _ := io.ReadAll(r.Body)
		if r.Header.Get("Authorization") == "" {
			assert.Equalf(t, "request-body", string(body),
				"first request body = %q, want %q", string(body), "request-body")

			w.Header().Set("WWW-Authenticate", challenge.ToAuthenticate(challenge.Realm))
			w.WriteHeader(http.StatusPaymentRequired)
			return
		}
		assert.
			// Retry should have the same body.
			Equalf(t, "request-body", string(body),
				"retry body = %q, want %q", string(body), "request-body")

		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	challenge = challengeForURL(t, srv.URL, "tempo", nil)

	cred := newTestCredential("tempo")
	method := &mockMethod{name: "tempo", cred: cred}
	tr := NewTransport([]Method{method}, nil)
	bodyStr := "request-body"
	req, _ := http.NewRequest(http.MethodPost, srv.URL, strings.NewReader(bodyStr))
	req.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader(bodyStr)), nil
	}
	resp, err := tr.RoundTrip(req)
	if !assert.NoErrorf(t, err,
		"unexpected error: %v", err) {
		return
	}

	defer resp.Body.Close()
	if !assert.Equalf(t, http.StatusOK, resp.StatusCode,
		"expected 200, got %d", resp.StatusCode) {
		return
	}
	if !assert.EqualValuesf(t, 2, callCount,
		"expected 2 calls, got %d", callCount) {
		return
	}

}

func TestTransport_RoundTrip_MultipleWWWAuthenticate(t *testing.T) {
	var stripeChallenge *mpp.Challenge
	var tempoChallenge *mpp.Challenge

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			w.Header().Add("WWW-Authenticate", stripeChallenge.ToAuthenticate(stripeChallenge.Realm))
			w.Header().Add("WWW-Authenticate", tempoChallenge.ToAuthenticate(tempoChallenge.Realm))
			w.WriteHeader(http.StatusPaymentRequired)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	stripeChallenge = challengeForURL(t, srv.URL, "stripe", map[string]any{"amount": "100"})
	tempoChallenge = challengeForURL(t, srv.URL, "tempo", map[string]any{"amount": "100"})

	cred := newTestCredential("tempo")
	method := &mockMethod{name: "tempo", cred: cred}
	tr := NewTransport([]Method{method}, nil)
	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	resp, err := tr.RoundTrip(req)
	if !assert.NoErrorf(t, err,
		"unexpected error: %v", err) {
		return
	}

	defer resp.Body.Close()
	if !assert.Equalf(t, http.StatusOK, resp.StatusCode,
		"expected 200, got %d", resp.StatusCode) {
		return
	}

}

func TestTransport_RoundTrip_MergedWWWAuthenticate(t *testing.T) {
	var challenge *mpp.Challenge

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			w.Header().Set("WWW-Authenticate", `Bearer realm="example", `+challenge.ToAuthenticate(challenge.Realm))
			w.WriteHeader(http.StatusPaymentRequired)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	challenge = challengeForURL(t, srv.URL, "tempo", map[string]any{"amount": "100"})

	cred := newTestCredential("tempo")
	method := &mockMethod{name: "tempo", cred: cred}
	tr := NewTransport([]Method{method}, nil)
	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	resp, err := tr.RoundTrip(req)
	if !assert.NoErrorf(t, err,
		"unexpected error: %v", err) {
		return
	}

	defer resp.Body.Close()
	if !assert.Equalf(t, http.StatusOK, resp.StatusCode,
		"expected 200, got %d", resp.StatusCode) {
		return
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
	if !assert.NoErrorf(t, err,
		"unexpected error: %v", err) {
		return
	}

	defer resp.Body.Close()
	if !
	// Non-Payment scheme → no matching method → return original 402.
	assert.Equalf(t, http.StatusPaymentRequired, resp.StatusCode,
		"expected 402, got %d", resp.StatusCode) {
		return
	}

}

func TestTransport_RoundTrip_RejectsOriginMismatchFromContext(t *testing.T) {
	var challenge *mpp.Challenge
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("WWW-Authenticate", challenge.ToAuthenticate(challenge.Realm))
		w.WriteHeader(http.StatusPaymentRequired)
	}))
	defer srv.Close()
	challenge = challengeForURL(t, srv.URL, "tempo", nil)

	method := &mockMethod{name: "tempo", cred: newTestCredential("tempo")}
	tr := NewTransport([]Method{method}, nil)
	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	req = req.WithContext(withPaymentOrigin(req.Context(), "https://api.example.com"))
	_, err := tr.RoundTrip(req)
	if !assert.Falsef(t, err == nil || !strings.Contains(err.Error(), "refusing payment for redirected origin"),
		"RoundTrip() error = %v, want origin mismatch", err) {
		return
	}
	if !assert.Equalf(t, 0, method.calls,
		"CreateCredential() calls = %d, want 0", method.calls) {
		return
	}

}

func TestClient_Get(t *testing.T) {
	var challenge *mpp.Challenge

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			w.Header().Set("WWW-Authenticate", challenge.ToAuthenticate(challenge.Realm))
			w.WriteHeader(http.StatusPaymentRequired)
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("hello"))
	}))
	defer srv.Close()
	challenge = challengeForURL(t, srv.URL, "tempo", nil)

	cred := newTestCredential("tempo")
	method := &mockMethod{name: "tempo", cred: cred}
	c := New([]Method{method})
	resp, err := c.Get(context.Background(), srv.URL)
	if !assert.NoErrorf(t, err,
		"unexpected error: %v", err) {
		return
	}

	defer resp.Body.Close()
	if !assert.Equalf(t, http.StatusOK, resp.StatusCode,
		"expected 200, got %d", resp.StatusCode) {
		return
	}

}

func TestClient_Post(t *testing.T) {
	var challenge *mpp.Challenge

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equalf(t, http.MethodPost, r.Method,
			"expected POST, got %s", r.Method)
		assert.Equalf(t, "application/json", r.Header.Get("Content-Type"),
			"content-type = %q, want application/json", r.Header.Get("Content-Type"))

		if r.Header.Get("Authorization") == "" {
			w.Header().Set("WWW-Authenticate", challenge.ToAuthenticate(challenge.Realm))
			w.WriteHeader(http.StatusPaymentRequired)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	challenge = challengeForURL(t, srv.URL, "tempo", nil)

	cred := newTestCredential("tempo")
	method := &mockMethod{name: "tempo", cred: cred}
	c := New([]Method{method})
	body := strings.NewReader(`{"key":"value"}`)
	resp, err := c.Post(context.Background(), srv.URL, "application/json", body)
	if !assert.NoErrorf(t, err,
		"unexpected error: %v", err) {
		return
	}

	defer resp.Body.Close()
	if !assert.Equalf(t, http.StatusOK, resp.StatusCode,
		"expected 200, got %d", resp.StatusCode) {
		return
	}

}

func TestClient_WithHTTPClient(t *testing.T) {
	custom := &http.Client{}
	c := New(nil, WithHTTPClient(custom))
	if !assert.Equal(t, custom, c.httpClient,
		"expected custom http client to be set") {
		return
	}

}

func TestClient_Do_RejectsCrossOriginRedirect(t *testing.T) {
	var challenge *mpp.Challenge
	attacker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("WWW-Authenticate", challenge.ToAuthenticate(challenge.Realm))
		w.WriteHeader(http.StatusPaymentRequired)
	}))
	defer attacker.Close()
	challenge = challengeForURL(t, attacker.URL, "tempo", nil)

	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, attacker.URL, http.StatusFound)
	}))
	defer origin.Close()

	method := &mockMethod{name: "tempo", cred: newTestCredential("tempo")}
	c := New([]Method{method})
	_, err := c.Get(context.Background(), origin.URL)
	if !assert.Falsef(t, err == nil || !strings.Contains(err.Error(), "refusing cross-origin redirect"),
		"Get() error = %v, want cross-origin redirect rejection", err) {
		return
	}
	if !assert.Equalf(t, 0, method.calls,
		"CreateCredential() calls = %d, want 0", method.calls) {
		return
	}

}
