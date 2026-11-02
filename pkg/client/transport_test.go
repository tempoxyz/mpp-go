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

	"github.com/tempoxyz/mpp-go/pkg/internal/mpptest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tempoxyz/mpp-go/pkg/mpp"
)

// mockMethod implements Method for testing.
type mockMethod struct {
	name   string
	intent string
	cred   *mpp.Credential
	err    error
	calls  int
	seen   []string
}

func (m *mockMethod) Name() string { return m.name }
func (m *mockMethod) Intent() string {
	if m.intent == "" {
		return "payment"
	}
	return m.intent
}
func (m *mockMethod) CreateCredential(_ context.Context, ch *mpp.Challenge) (*mpp.Credential, error) {
	m.calls++
	m.seen = append(m.seen, ch.Intent)
	if m.err != nil {
		return nil, m.err
	}
	return m.cred, nil
}

func newTestCredential(method string) *mpp.Credential {
	return &mpp.Credential{
		Challenge: mpp.ChallengeEcho{
			ID:      "test-id",
			Method:  method,
			Intent:  "payment",
			Request: "e30",
		},
		Payload: map[string]any{},
		Source:  "test",
	}
}

func challengeForURL(t *testing.T, rawURL, method string, request map[string]any, opts ...mpp.ChallengeOption) *mpp.Challenge {
	t.Helper()
	parsedURL, err := urlpkg.Parse(rawURL)
	if !assert.NoErrorf(t, err,
		"url.Parse(%q) error = %v", rawURL, err) {
		return *new(*mpp.Challenge)
	}

	return mpptest.NewChallenge("secret", parsedURL.Host, method, "payment", request, opts...)
}

func TestTransport_RoundTrip_No402(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	}))
	defer srv.Close()

	tr := newTestTransport(nil, nil)
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
			w.Header().Set("WWW-Authenticate", mpptest.FormatChallenge(challenge))
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
	tr := newTestTransport([]Method{method}, nil)
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

func TestTransport_RoundTrip_RetriesFreshPaymentChallenges(t *testing.T) {
	var challenges []*mpp.Challenge
	callCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		if callCount <= len(challenges) {
			if callCount > 1 {
				assert.NotEmpty(t, r.Header.Get("Authorization"))
			}
			challenge := challenges[callCount-1]
			w.Header().Set("WWW-Authenticate", mpptest.FormatChallenge(challenge))
			w.WriteHeader(http.StatusPaymentRequired)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	challenges = []*mpp.Challenge{
		challengeForURL(t, srv.URL, "tempo", map[string]any{"attempt": 1}),
		challengeForURL(t, srv.URL, "tempo", map[string]any{"attempt": 2}),
	}
	method := &mockMethod{name: "tempo", cred: newTestCredential("tempo")}
	req, err := http.NewRequest(http.MethodGet, srv.URL, nil)
	require.NoError(t, err)

	resp, err := newTestTransport([]Method{method}, nil).RoundTrip(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, 3, callCount)
	assert.Equal(t, 2, method.calls)
}

func TestTransport_RoundTrip_StopsAtDefaultPaymentRetryLimit(t *testing.T) {
	var challenges []*mpp.Challenge
	callCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		challenge := challenges[callCount]
		callCount++
		w.Header().Set("WWW-Authenticate", mpptest.FormatChallenge(challenge))
		w.WriteHeader(http.StatusPaymentRequired)
	}))
	defer srv.Close()

	for attempt := 1; attempt <= defaultMaxPaymentRetries+1; attempt++ {
		challenges = append(challenges, challengeForURL(t, srv.URL, "tempo", map[string]any{"attempt": attempt}))
	}
	method := &mockMethod{name: "tempo", cred: newTestCredential("tempo")}
	req, err := http.NewRequest(http.MethodGet, srv.URL, nil)
	require.NoError(t, err)

	resp, err := newTestTransport([]Method{method}, nil).RoundTrip(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusPaymentRequired, resp.StatusCode)
	assert.Equal(t, defaultMaxPaymentRetries+1, callCount)
	assert.Equal(t, defaultMaxPaymentRetries, method.calls)
}

func TestTransport_RoundTrip_ReusesCredentialForRepeatedChallenge(t *testing.T) {
	var challenge *mpp.Challenge
	var authorizations []string
	callCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		if authorization := r.Header.Get("Authorization"); authorization != "" {
			authorizations = append(authorizations, authorization)
		}
		if callCount <= 2 {
			w.Header().Set("WWW-Authenticate", mpptest.FormatChallenge(challenge))
			w.WriteHeader(http.StatusPaymentRequired)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	challenge = challengeForURL(t, srv.URL, "tempo", nil)
	method := &mockMethod{name: "tempo", cred: newTestCredential("tempo")}
	req, err := http.NewRequest(http.MethodGet, srv.URL, nil)
	require.NoError(t, err)

	resp, err := newTestTransport([]Method{method}, nil).RoundTrip(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, 1, method.calls)
	require.Len(t, authorizations, 2)
	assert.Equal(t, authorizations[0], authorizations[1])
}

func TestTransport_RoundTrip_UsesAdvertisedPaymentAuthorizationHeader(t *testing.T) {
	callCount := 0
	var challenge *mpp.Challenge
	var retryAuth, retryPaymentAuth string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		if r.Header.Get(mpp.HeaderPaymentAuthorization) == "" {
			w.Header().Set("WWW-Authenticate", mpptest.FormatChallenge(challenge))
			w.WriteHeader(http.StatusPaymentRequired)
			w.Write([]byte("pay me"))
			return
		}
		retryAuth = r.Header.Get("Authorization")
		retryPaymentAuth = r.Header.Get(mpp.HeaderPaymentAuthorization)
		assert.Truef(t, strings.HasPrefix(retryPaymentAuth, "Payment "),
			"expected Payment auth scheme, got %q", retryPaymentAuth)

		w.WriteHeader(http.StatusOK)
		w.Write([]byte("paid"))
	}))
	defer srv.Close()
	challenge = challengeForURL(t, srv.URL, "tempo", nil, mpp.WithHeader(mpp.HeaderPaymentAuthorization))

	cred := newTestCredential("tempo")
	method := &mockMethod{name: "tempo", cred: cred}
	tr := newTestTransport([]Method{method}, nil)
	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	req.Header.Set("Authorization", "Bearer app-token")
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

	assert.Equal(t, 2, callCount)
	assert.Equal(t, "Bearer app-token", retryAuth)
	assert.True(t, strings.HasPrefix(retryPaymentAuth, "Payment "))
}

func TestTransport_RoundTrip_402NoMatchingMethod(t *testing.T) {
	challenge := mpptest.NewChallenge("secret", "realm", "stripe", "payment", nil)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("WWW-Authenticate", mpptest.FormatChallenge(challenge))
		w.WriteHeader(http.StatusPaymentRequired)
		w.Write([]byte("pay me"))
	}))
	defer srv.Close()

	method := &mockMethod{name: "tempo", cred: newTestCredential("tempo")}
	tr := newTestTransport([]Method{method}, nil)
	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	resp, err := tr.RoundTrip(req)
	// The unpaid request's 402 offers nothing the client pays: an error with
	// mppx's message, not the 402.
	assert.Nil(t, resp)
	require.ErrorIs(t, err, ErrNoMethodFound)
	assert.EqualError(t, err, "No method found for challenges: stripe.payment. Available: tempo.payment")
	var noMethod *NoMethodFoundError
	require.ErrorAs(t, err, &noMethod)
	require.Len(t, noMethod.Challenges, 1)
	assert.Equal(t, challenge.ID, noMethod.Challenges[0].ID)
	assert.Equal(t, []string{"tempo.payment"}, noMethod.Available)
}

func TestTransport_RoundTrip_402AfterPaidRetryIsReturned(t *testing.T) {
	var challenge *mpp.Challenge
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			w.Header().Set("WWW-Authenticate", mpptest.FormatChallenge(challenge))
		} else {
			// The credential is rejected with a challenge the client cannot pay.
			w.Header().Set("WWW-Authenticate", mpptest.FormatChallenge(mpptest.NewChallenge("secret", "realm", "stripe", "payment", nil)))
		}
		w.WriteHeader(http.StatusPaymentRequired)
	}))
	defer srv.Close()
	parsedURL, err := urlpkg.Parse(srv.URL)
	require.NoError(t, err)
	challenge = mpptest.NewChallenge("secret", parsedURL.Host, "tempo", "payment", nil)

	method := &mockMethod{name: "tempo", cred: newTestCredential("tempo")}
	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	resp, err := newTestTransport([]Method{method}, nil).RoundTrip(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusPaymentRequired, resp.StatusCode)
	assert.Equal(t, 1, method.calls)
}

func TestTransport_RoundTrip_MatchesMethodAndIntent(t *testing.T) {
	var challenges []*mpp.Challenge
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			for _, challenge := range challenges {
				w.Header().Add("WWW-Authenticate", mpptest.FormatChallenge(challenge))
			}
			w.WriteHeader(http.StatusPaymentRequired)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	parsedURL, err := urlpkg.Parse(srv.URL)
	if !assert.NoError(t, err) {
		return
	}
	challenges = []*mpp.Challenge{
		mpptest.NewChallenge("secret", parsedURL.Host, "tempo", "authorize", nil),
		mpptest.NewChallenge("secret", parsedURL.Host, "tempo", "charge", nil),
	}
	method := &mockMethod{
		name:   "tempo",
		intent: "charge",
		cred:   newTestCredential("tempo"),
	}

	req, err := http.NewRequest(http.MethodGet, srv.URL, nil)
	if !assert.NoError(t, err) {
		return
	}
	resp, err := newTestTransport([]Method{method}, nil).RoundTrip(req)
	if !assert.NoError(t, err) {
		return
	}
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, 1, method.calls)
	assert.Equal(t, []string{"charge"}, method.seen)
}

func TestTransport_RoundTrip_402ExpiredChallenge(t *testing.T) {
	// Use an expiry in the past.
	challenge := mpptest.NewChallenge("secret", "realm", "tempo", "payment", nil,
		mpp.WithExpires("2020-01-01T00:00:00.000Z"))

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("WWW-Authenticate", mpptest.FormatChallenge(challenge))
		w.WriteHeader(http.StatusPaymentRequired)
		w.Write([]byte("expired"))
	}))
	defer srv.Close()

	method := &mockMethod{name: "tempo", cred: newTestCredential("tempo")}
	tr := newTestTransport([]Method{method}, nil)
	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	resp, err := tr.RoundTrip(req)
	// Expired challenge → no payable challenge → error, nothing paid.
	assert.Nil(t, resp)
	require.ErrorIs(t, err, ErrNoMethodFound)
	assert.Zero(t, method.calls)
}

func TestTransport_RoundTrip_402UnparseableExpiresIsSkipped(t *testing.T) {
	// A challenge whose expires cannot be parsed must not be paid: the issuing
	// server would itself reject the resulting credential.
	challenge := mpptest.NewChallenge("secret", "realm", "tempo", "payment", nil,
		mpp.WithExpires("not-a-timestamp"))

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("WWW-Authenticate", mpptest.FormatChallenge(challenge))
		w.WriteHeader(http.StatusPaymentRequired)
		w.Write([]byte("bad expires"))
	}))
	defer srv.Close()

	method := &mockMethod{name: "tempo", cred: newTestCredential("tempo")}
	tr := newTestTransport([]Method{method}, nil)
	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	resp, err := tr.RoundTrip(req)
	// No parseable/valid challenge → error, no payment made.
	assert.Nil(t, resp)
	require.ErrorIs(t, err, ErrNoMethodFound)
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

			w.Header().Set("WWW-Authenticate", mpptest.FormatChallenge(challenge))
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
	tr := newTestTransport([]Method{method}, nil)
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
			w.Header().Add("WWW-Authenticate", mpptest.FormatChallenge(stripeChallenge))
			w.Header().Add("WWW-Authenticate", mpptest.FormatChallenge(tempoChallenge))
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
	tr := newTestTransport([]Method{method}, nil)
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
			w.Header().Set("WWW-Authenticate", `Bearer realm="example", `+mpptest.FormatChallenge(challenge))
			w.WriteHeader(http.StatusPaymentRequired)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	challenge = challengeForURL(t, srv.URL, "tempo", map[string]any{"amount": "100"})

	cred := newTestCredential("tempo")
	method := &mockMethod{name: "tempo", cred: cred}
	tr := newTestTransport([]Method{method}, nil)
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
	tr := newTestTransport([]Method{method}, nil)
	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	resp, err := tr.RoundTrip(req)
	// A 402 without Payment challenges is unpayable, as in mppx.
	assert.Nil(t, resp)
	require.ErrorIs(t, err, ErrNoMethodFound)
	assert.EqualError(t, err, "No method found for challenges: . Available: tempo.payment")
}

func TestTransport_RoundTrip_RejectsOriginMismatchFromContext(t *testing.T) {
	var challenge *mpp.Challenge
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("WWW-Authenticate", mpptest.FormatChallenge(challenge))
		w.WriteHeader(http.StatusPaymentRequired)
	}))
	defer srv.Close()
	challenge = challengeForURL(t, srv.URL, "tempo", nil)

	method := &mockMethod{name: "tempo", cred: newTestCredential("tempo")}
	tr := newTestTransport([]Method{method}, nil)
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

func TestTransport_RoundTrip_RejectsRelayedTempoProof(t *testing.T) {
	var challenge *mpp.Challenge
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("WWW-Authenticate", mpptest.FormatChallenge(challenge))
		w.WriteHeader(http.StatusPaymentRequired)
	}))
	defer srv.Close()
	challenge = mpptest.NewChallenge("secret", "target.example", "tempo", "charge", map[string]any{
		"amount": "0", "currency": "0x3333333333333333333333333333333333333333",
		"recipient": "0x2222222222222222222222222222222222222222",
	})

	method := &mockMethod{name: "tempo", intent: "charge", cred: newTestCredential("tempo")}
	tr := newTestTransport([]Method{method}, nil)
	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	_, err := tr.RoundTrip(req)
	require.ErrorContains(t, err, "refusing to sign Tempo proof for realm")
	assert.Equal(t, 0, method.calls)
}

func TestTransport_ZeroProofRequiresHTTPSDefaultPort(t *testing.T) {
	for _, tc := range []struct {
		name, url string
		allowed   bool
	}{
		{"HTTPS default", "https://api.example/paid", true},
		{"HTTPS port 443", "https://api.example:443/paid", true},
		{"HTTP", "http://api.example/paid", false},
		{"other HTTPS port", "https://api.example:8443/paid", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			transport := newTestTransport(nil, nil)
			request, err := http.NewRequest(http.MethodGet, tc.url, nil)
			require.NoError(t, err)
			challenge := mpptest.NewChallenge("secret", "api.example", "tempo", "charge", map[string]any{"amount": "0"})
			err = transport.validatePaymentOrigin(request, challengeCandidate{challenge: challenge, method: &mockMethod{name: "tempo", intent: "charge"}})
			if tc.allowed {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, "refusing to sign Tempo proof")
			}
		})
	}
}

func TestTransport_RoundTrip_RejectsAlternateZeroAmounts(t *testing.T) {
	for _, amount := range []any{"00", " 0 ", "\t00\n", 0} {
		t.Run(fmt.Sprint(amount), func(t *testing.T) {
			var challenge *mpp.Challenge
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("WWW-Authenticate", mpptest.FormatChallenge(challenge))
				w.WriteHeader(http.StatusPaymentRequired)
			}))
			defer srv.Close()
			challenge = mpptest.NewChallenge("secret", "target.example", "tempo", "charge", map[string]any{"amount": amount})
			method := &mockMethod{name: "tempo", intent: "charge", cred: newTestCredential("tempo")}
			req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
			_, err := newTestTransport([]Method{method}, nil).RoundTrip(req)
			require.ErrorContains(t, err, "refusing to sign Tempo proof for realm")
			assert.Zero(t, method.calls)
		})
	}
}

func TestTransport_RoundTrip_SkipsRelayedProofWhenAnotherChallengeIsValid(t *testing.T) {
	var relayed, valid *mpp.Challenge
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.Header().Add("WWW-Authenticate", mpptest.FormatChallenge(relayed))
		w.Header().Add("WWW-Authenticate", mpptest.FormatChallenge(valid))
		w.WriteHeader(http.StatusPaymentRequired)
	}))
	defer srv.Close()
	relayed = mpptest.NewChallenge("secret", "target.example", "tempo", "charge", map[string]any{"amount": "0"})
	valid = mpptest.NewChallenge("secret", "other.example", "stripe", "payment", map[string]any{"amount": "100"})
	proof := &mockMethod{name: "tempo", intent: "charge", cred: newTestCredential("tempo")}
	stripe := &mockMethod{name: "stripe", cred: newTestCredential("stripe")}
	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	resp, err := newTestTransport([]Method{proof, stripe}, nil).RoundTrip(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Zero(t, proof.calls)
	assert.Equal(t, 1, stripe.calls)
}

func TestTransport_RoundTrip_StandaloneRefusesRedirectedPayment(t *testing.T) {
	// A standalone Transport (no Client.Do wrapper) plugged into a redirect
	// following http.Client must not auto-pay an origin reached via redirect.
	var challenge *mpp.Challenge
	attacker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("WWW-Authenticate", mpptest.FormatChallenge(challenge))
		w.WriteHeader(http.StatusPaymentRequired)
	}))
	defer attacker.Close()
	challenge = challengeForURL(t, attacker.URL, "tempo", nil)

	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, attacker.URL, http.StatusFound)
	}))
	defer origin.Close()

	method := &mockMethod{name: "tempo", cred: newTestCredential("tempo")}
	tr := newTestTransport([]Method{method}, nil)
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
			w.Header().Set("WWW-Authenticate", mpptest.FormatChallenge(challenge))
			w.WriteHeader(http.StatusPaymentRequired)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	challenge = challengeForURL(t, srv.URL, "tempo", nil)

	method := &mockMethod{name: "tempo", cred: newTestCredential("tempo")}
	tr := newTestTransport([]Method{method}, nil)
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
			w.Header().Set("WWW-Authenticate", mpptest.FormatChallenge(challenge))
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
			w.Header().Set("WWW-Authenticate", mpptest.FormatChallenge(challenge))
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
		w.Header().Set("WWW-Authenticate", mpptest.FormatChallenge(challenge))
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

func TestTransport_PayRedirects_RefusesBrokenRedirectChain(t *testing.T) {
	transport := newTestTransport(nil, nil)
	transport.payRedirects = true
	challenge, err := mpp.NewChallengeWithError("secret", "evil.example", "tempo", "charge", map[string]any{"amount": "1"})
	require.NoError(t, err)
	candidate := challengeCandidate{challenge: challenge, method: &mockMethod{name: "tempo", intent: "charge"}}

	redirected, err := http.NewRequest(http.MethodGet, "https://evil.example/paid", nil)
	require.NoError(t, err)
	redirected.Response = &http.Response{StatusCode: http.StatusFound}
	err = transport.validatePaymentOrigin(redirected, candidate)
	require.ErrorContains(t, err, "refusing payment after redirect")

	original, err := http.NewRequest(http.MethodGet, "https://api.example/paid", nil)
	require.NoError(t, err)
	redirected.Response = &http.Response{StatusCode: http.StatusFound, Request: original}
	err = transport.validatePaymentOrigin(redirected, candidate)
	require.ErrorContains(t, err, "refusing payment for redirected origin")

	sameOrigin, err := http.NewRequest(http.MethodGet, "https://api.example/other", nil)
	require.NoError(t, err)
	sameOrigin.Response = &http.Response{StatusCode: http.StatusFound, Request: original}
	require.NoError(t, transport.validatePaymentOrigin(sameOrigin, candidate))
}

// A hash credential's transfer is already on chain: a 402 to it is final,
// and the Transport never pays again.
func TestTransport_RoundTrip_DoesNotRepayAfterHashCredential(t *testing.T) {
	var challenges []*mpp.Challenge
	callCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		challenge := challenges[min(callCount, len(challenges))-1]
		w.Header().Set("WWW-Authenticate", mpptest.FormatChallenge(challenge))
		w.WriteHeader(http.StatusPaymentRequired)
	}))
	defer srv.Close()

	challenges = []*mpp.Challenge{
		challengeForURL(t, srv.URL, "tempo", map[string]any{"attempt": 1}),
		challengeForURL(t, srv.URL, "tempo", map[string]any{"attempt": 2}),
	}
	cred := newTestCredential("tempo")
	cred.Payload = map[string]any{"type": "hash", "hash": "0x01"}
	method := &mockMethod{name: "tempo", cred: cred}
	req, err := http.NewRequest(http.MethodGet, srv.URL, nil)
	require.NoError(t, err)

	resp, err := newTestTransport([]Method{method}, nil).RoundTrip(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusPaymentRequired, resp.StatusCode)
	assert.Equal(t, 2, callCount)
	assert.Equal(t, 1, method.calls)
}
