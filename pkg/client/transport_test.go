package client

import (
	"context"
	"fmt"
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

func TestTransport_RoundTrip_402UnparseableExpiresIsSkipped(t *testing.T) {
	// A challenge whose expires cannot be parsed must not be paid: the issuing
	// server would itself reject the resulting credential.
	challenge := mpp.NewChallenge("secret", "realm", "tempo", "payment", nil,
		mpp.WithExpires("not-a-timestamp"))

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("WWW-Authenticate", challenge.ToAuthenticate("realm"))
		w.WriteHeader(http.StatusPaymentRequired)
		w.Write([]byte("bad expires"))
	}))
	defer srv.Close()

	method := &mockMethod{name: "tempo", cred: newTestCredential("tempo")}
	tr := NewTransport([]Method{method}, nil)
	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	resp, err := tr.RoundTrip(req)
	if !assert.NoErrorf(t, err, "unexpected error: %v", err) {
		return
	}
	defer resp.Body.Close()
	// No parseable/valid challenge → original 402 returned, no payment made.
	assert.Equal(t, http.StatusPaymentRequired, resp.StatusCode)
	assert.Equalf(t, 0, method.calls,
		"CreateCredential() calls = %d, want 0 for unparseable expires", method.calls)
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

func TestTransport_RoundTrip_StandaloneRefusesRedirectedPayment(t *testing.T) {
	// A standalone Transport (no Client.Do wrapper) plugged into a redirect
	// following http.Client must not auto-pay an origin reached via redirect.
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
	tr := NewTransport([]Method{method}, nil)
	// The natural, unguarded way to use the exported Transport.
	hc := &http.Client{Transport: tr}
	req, _ := http.NewRequest(http.MethodGet, origin.URL, nil)
	_, err := hc.Do(req)

	if !assert.Falsef(t, err == nil || !strings.Contains(err.Error(), "refusing payment after redirect"),
		"Do() error = %v, want redirect-payment refusal", err) {
		return
	}
	if !assert.Equalf(t, 0, method.calls,
		"CreateCredential() calls = %d, want 0", method.calls) {
		return
	}
}

func TestTransport_RoundTrip_StandaloneDirectRequestStillPays(t *testing.T) {
	// A standalone Transport must keep working for a direct (non-redirected)
	// request: the request URL is the origin the caller chose.
	var challenge *mpp.Challenge
	callCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		if r.Header.Get("Authorization") == "" {
			w.Header().Set("WWW-Authenticate", challenge.ToAuthenticate(challenge.Realm))
			w.WriteHeader(http.StatusPaymentRequired)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	challenge = challengeForURL(t, srv.URL, "tempo", nil)

	method := &mockMethod{name: "tempo", cred: newTestCredential("tempo")}
	tr := NewTransport([]Method{method}, nil)
	hc := &http.Client{Transport: tr}
	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	resp, err := hc.Do(req)
	if !assert.NoErrorf(t, err, "unexpected error: %v", err) {
		return
	}
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, 1, method.calls)
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

// intentMockMethod implements Method and reports the intents it supports.
type intentMockMethod struct {
	mockMethod
	intents []string
	seen    []string
}

func (m *intentMockMethod) Intents() []string { return m.intents }

func (m *intentMockMethod) CreateCredential(ctx context.Context, ch *mpp.Challenge) (*mpp.Credential, error) {
	m.seen = append(m.seen, ch.Intent)
	return m.mockMethod.CreateCredential(ctx, ch)
}

// TestTransport_RoundTrip_SkipsUnsupportedIntent covers the intent negotiation
// example from the core spec: a server offering the same method under two
// intents. A client that does not recognize an intent must treat that
// challenge as unsupported rather than paying it with the wrong intent's
// credential.
func TestTransport_RoundTrip_SkipsUnsupportedIntent(t *testing.T) {
	var authorizeCh, chargeCh *mpp.Challenge

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			// Unsupported intent is advertised first, so a client that
			// matches on method alone picks it.
			w.Header().Add("WWW-Authenticate", authorizeCh.ToAuthenticate(authorizeCh.Realm))
			w.Header().Add("WWW-Authenticate", chargeCh.ToAuthenticate(chargeCh.Realm))
			w.WriteHeader(http.StatusPaymentRequired)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	parsedURL, err := urlpkg.Parse(srv.URL)
	if !assert.NoErrorf(t, err, "url.Parse(%q) error = %v", srv.URL, err) {
		return
	}
	authorizeCh = mpp.NewChallenge("secret", parsedURL.Host, "example", "authorize", nil)
	chargeCh = mpp.NewChallenge("secret", parsedURL.Host, "example", "charge", nil)

	method := &intentMockMethod{
		mockMethod: mockMethod{name: "example", cred: newTestCredential("example")},
		intents:    []string{"charge"},
	}
	tr := NewTransport([]Method{method}, nil)
	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	resp, err := tr.RoundTrip(req)
	if !assert.NoErrorf(t, err, "unexpected error: %v", err) {
		return
	}
	defer resp.Body.Close()

	assert.Equalf(t, []string{"charge"}, method.seen,
		"client paid the wrong intent: built credentials for %v, want only [charge]", method.seen)
}

// strictIntentMethod mirrors the built-in Tempo client method, which rejects
// challenges carrying an intent it cannot settle.
type strictIntentMethod struct {
	intentMockMethod
}

func (m *strictIntentMethod) CreateCredential(ctx context.Context, ch *mpp.Challenge) (*mpp.Credential, error) {
	if ch.Intent != "charge" {
		return nil, fmt.Errorf("unsupported challenge intent %q", ch.Intent)
	}
	return m.intentMockMethod.CreateCredential(ctx, ch)
}

// TestTransport_RoundTrip_UnsupportedIntentFirstStillPays is the user-visible
// consequence of matching on the method token alone: the transport committed
// to the unsupported challenge, CreateCredential rejected it, and RoundTrip
// failed the whole request even though the server also offered a challenge the
// method could settle.
func TestTransport_RoundTrip_UnsupportedIntentFirstStillPays(t *testing.T) {
	var authorizeCh, chargeCh *mpp.Challenge
	paid := false

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			w.Header().Add("WWW-Authenticate", authorizeCh.ToAuthenticate(authorizeCh.Realm))
			w.Header().Add("WWW-Authenticate", chargeCh.ToAuthenticate(chargeCh.Realm))
			w.WriteHeader(http.StatusPaymentRequired)
			return
		}
		paid = true
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	parsedURL, err := urlpkg.Parse(srv.URL)
	if !assert.NoErrorf(t, err, "url.Parse(%q) error = %v", srv.URL, err) {
		return
	}
	authorizeCh = mpp.NewChallenge("secret", parsedURL.Host, "example", "authorize", nil)
	chargeCh = mpp.NewChallenge("secret", parsedURL.Host, "example", "charge", nil)

	method := &strictIntentMethod{intentMockMethod{
		mockMethod: mockMethod{name: "example", cred: newTestCredential("example")},
		intents:    []string{"charge"},
	}}
	tr := NewTransport([]Method{method}, nil)
	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	resp, err := tr.RoundTrip(req)
	if !assert.NoErrorf(t, err,
		"request failed even though a payable challenge was offered: %v", err) {
		return
	}
	defer resp.Body.Close()

	assert.Equalf(t, http.StatusOK, resp.StatusCode, "expected 200, got %d", resp.StatusCode)
	assert.Truef(t, paid, "server never received the payment credential")
}

// TestTransport_RoundTrip_MethodWithoutIntentsAcceptsAny pins the compatibility
// contract: a Method that does not implement IntentMethod keeps matching on the
// method token alone.
func TestTransport_RoundTrip_MethodWithoutIntentsAcceptsAny(t *testing.T) {
	var challenge *mpp.Challenge

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			w.Header().Set("WWW-Authenticate", challenge.ToAuthenticate(challenge.Realm))
			w.WriteHeader(http.StatusPaymentRequired)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	parsedURL, err := urlpkg.Parse(srv.URL)
	if !assert.NoErrorf(t, err, "url.Parse(%q) error = %v", srv.URL, err) {
		return
	}
	challenge = mpp.NewChallenge("secret", parsedURL.Host, "example", "some-new-intent", nil)

	method := &mockMethod{name: "example", cred: newTestCredential("example")}
	tr := NewTransport([]Method{method}, nil)
	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	resp, err := tr.RoundTrip(req)
	if !assert.NoErrorf(t, err, "unexpected error: %v", err) {
		return
	}
	defer resp.Body.Close()

	assert.Equalf(t, 1, method.calls,
		"method without declared intents should still be used, calls = %d", method.calls)
}
