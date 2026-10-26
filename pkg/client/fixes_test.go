package client

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tempoxyz/mpp-go/pkg/internal/mpptest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tempoxyz/mpp-go/pkg/mpp"
)

func TestClient_Do_KeepsDefaultRedirectLimit(t *testing.T) {
	var hits atomic.Int32
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		http.Redirect(w, r, srv.URL+"/loop", http.StatusFound)
	}))
	defer srv.Close()

	_, err := New(nil).Get(context.Background(), srv.URL)
	require.ErrorContains(t, err, "stopped after 10 redirects")
	assert.Equal(t, int32(10), hits.Load())
}

func TestClient_Do_FollowsSameOriginRedirect(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/start" {
			http.Redirect(w, r, "/end", http.StatusFound)
			return
		}
		_, _ = io.WriteString(w, "done")
	}))
	defer srv.Close()

	resp, err := New(nil).Get(context.Background(), srv.URL+"/start")
	require.NoError(t, err)
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	assert.Equal(t, "done", string(body))
}

func TestClient_Do_RefusesCrossOriginRedirectWithoutPayment(t *testing.T) {
	var otherHits atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		otherHits.Add(1)
		_, _ = io.WriteString(w, "free")
	}))
	defer other.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL, http.StatusFound)
	}))
	defer origin.Close()

	// mppx and the mpp-tools conformance flow
	// cross_origin_redirect_before_402_rejected refuse it before any 402.
	_, err := New(nil).Get(context.Background(), origin.URL)
	require.ErrorContains(t, err, "refusing cross-origin redirect")
	assert.Zero(t, otherHits.Load())
}

func TestClient_Do_NeverSendsPaymentCredentialCrossOrigin(t *testing.T) {
	var mu sync.Mutex
	var otherAuth []string
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		otherAuth = append(otherAuth, r.Header.Get("Authorization"), r.Header.Get(mpp.HeaderPaymentAuthorization))
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer other.Close()

	var challenge *mpp.Challenge
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			w.Header().Set("WWW-Authenticate", mpptest.FormatChallenge(challenge))
			w.WriteHeader(http.StatusPaymentRequired)
			return
		}
		http.Redirect(w, r, other.URL, http.StatusTemporaryRedirect)
	}))
	defer origin.Close()
	challenge = challengeForURL(t, origin.URL, "tempo", nil)

	method := &mockMethod{name: "tempo", cred: newTestCredential("tempo")}
	_, err := New([]Method{method}).Get(context.Background(), origin.URL)
	require.ErrorContains(t, err, "refusing cross-origin redirect")
	assert.Equal(t, 1, method.calls)
	mu.Lock()
	defer mu.Unlock()
	assert.Empty(t, otherAuth)
}

func TestClient_Do_RefusesCrossOriginRedirectWithCallerCredential(t *testing.T) {
	var otherHits atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		otherHits.Add(1)
	}))
	defer other.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL, http.StatusFound)
	}))
	defer origin.Close()

	req, err := http.NewRequest(http.MethodGet, origin.URL, nil)
	require.NoError(t, err)
	req.Header.Set(mpp.HeaderPaymentAuthorization, "Payment abc")
	_, err = New(nil).Do(req)
	require.ErrorContains(t, err, "refusing cross-origin redirect")
	assert.Zero(t, otherHits.Load())
}

type closeTracker struct {
	io.Reader
	closed atomic.Bool
	closes atomic.Int32
}

func (c *closeTracker) Close() error {
	c.closed.Store(true)
	c.closes.Add(1)
	return nil
}

func TestTransport_RoundTrip_ClosesBodyOnConfigError(t *testing.T) {
	method := &mockMethod{name: "tempo"}
	tr := newTestTransport([]Method{method}, http.DefaultTransport, WithPaymentPreferences(PaymentPreferences{"unknown/charge": 1}))
	body := &closeTracker{Reader: strings.NewReader("x")}
	req, err := http.NewRequest(http.MethodPost, "http://127.0.0.1:1", body)
	require.NoError(t, err)

	_, err = tr.RoundTrip(req)
	require.Error(t, err)
	assert.True(t, body.closed.Load())
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestTransport_RoundTrip_ClosesBodyOnceWhenRetryCannotBeBuilt(t *testing.T) {
	challenge := challengeForURL(t, "http://api.example.com", "tempo", nil)
	inner := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		// Like net/http's transports, the inner RoundTripper owns and closes
		// the request body.
		_ = req.Body.Close()
		header := http.Header{}
		header.Set(mpp.HeaderWWWAuthenticate, mpptest.FormatChallenge(challenge))
		return &http.Response{StatusCode: http.StatusPaymentRequired, Header: header, Body: io.NopCloser(strings.NewReader("")), Request: req}, nil
	})
	method := &mockMethod{name: "tempo", cred: newTestCredential("tempo")}
	body := &closeTracker{Reader: strings.NewReader("x")}
	req, err := http.NewRequest(http.MethodPost, "http://api.example.com", body)
	require.NoError(t, err)
	req.GetBody = nil

	_, err = newTestTransport([]Method{method}, inner).RoundTrip(req)
	require.ErrorContains(t, err, "cloning request for retry")
	assert.Equal(t, int32(1), body.closes.Load())
	assert.Zero(t, method.calls)
}

func TestTransport_RoundTrip_ReportsUnknownOutcomeAfterSendingCredential(t *testing.T) {
	challenge := challengeForURL(t, "http://api.example.com", "tempo", nil)
	networkErr := errors.New("connection reset")
	calls := 0
	inner := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			header := http.Header{}
			header.Set(mpp.HeaderWWWAuthenticate, mpptest.FormatChallenge(challenge))
			return &http.Response{StatusCode: http.StatusPaymentRequired, Header: header, Body: io.NopCloser(strings.NewReader("")), Request: req}, nil
		}
		return nil, networkErr
	})
	cred := newTestCredential("tempo")
	method := &mockMethod{name: "tempo", cred: cred}
	req, err := http.NewRequest(http.MethodGet, "http://api.example.com", nil)
	require.NoError(t, err)

	_, err = (&http.Client{Transport: newTestTransport([]Method{method}, inner)}).Do(req)
	var unknown *PaymentOutcomeUnknownError
	require.ErrorAs(t, err, &unknown)
	assert.Equal(t, challenge.ID, unknown.Challenge.ID)
	assert.Same(t, cred, unknown.Credential)
	assert.ErrorIs(t, err, networkErr)
	assert.Contains(t, err.Error(), "do not blindly retry")
}

func TestTransport_RoundTrip_PlainErrorBeforeCredentialSent(t *testing.T) {
	networkErr := errors.New("dial failed")
	inner := roundTripFunc(func(*http.Request) (*http.Response, error) { return nil, networkErr })
	req, err := http.NewRequest(http.MethodGet, "http://api.example.com", nil)
	require.NoError(t, err)

	_, err = newTestTransport(nil, inner).RoundTrip(req)
	assert.Same(t, networkErr, err)
}

func callerAuthorization(t *testing.T, challenge *mpp.Challenge) (*mpp.Credential, string) {
	t.Helper()
	cred := &mpp.Credential{Challenge: challenge.ToEcho(), Payload: map[string]any{"hash": "0xabc"}}
	authorization, err := mpp.FormatCredential(cred)
	require.NoError(t, err)
	return cred, authorization
}

func TestTransport_RoundTrip_ReportsUnknownOutcomeForCallerCredential(t *testing.T) {
	challenge := challengeForURL(t, "http://api.example.com", "tempo", nil)
	cred, authorization := callerAuthorization(t, challenge)
	networkErr := errors.New("connection reset")
	inner := roundTripFunc(func(*http.Request) (*http.Response, error) { return nil, networkErr })

	for _, header := range []string{"Authorization", mpp.HeaderPaymentAuthorization, "x-custom-payment"} {
		t.Run(header, func(t *testing.T) {
			req, err := http.NewRequest(http.MethodGet, "http://api.example.com", nil)
			require.NoError(t, err)
			req.Header[header] = []string{authorization}

			_, err = newTestTransport(nil, inner).RoundTrip(req)
			var unknown *PaymentOutcomeUnknownError
			require.ErrorAs(t, err, &unknown)
			require.NotNil(t, unknown.Challenge)
			assert.Equal(t, challenge.ID, unknown.Challenge.ID)
			require.NotNil(t, unknown.Credential)
			assert.Equal(t, cred.Payload, unknown.Credential.Payload)
			assert.ErrorIs(t, err, networkErr)
		})
	}
}

func TestTransport_RoundTrip_ReportsUnknownOutcomeForUnparseableCallerCredential(t *testing.T) {
	networkErr := errors.New("connection reset")
	inner := roundTripFunc(func(*http.Request) (*http.Response, error) { return nil, networkErr })
	req, err := http.NewRequest(http.MethodGet, "http://api.example.com", nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Payment not-a-credential")

	_, err = newTestTransport(nil, inner).RoundTrip(req)
	var unknown *PaymentOutcomeUnknownError
	require.ErrorAs(t, err, &unknown)
	assert.Nil(t, unknown.Challenge)
	assert.Nil(t, unknown.Credential)
	assert.Contains(t, err.Error(), "do not blindly retry")
}

func TestClient_Do_RunsCallerRedirectHookFirst(t *testing.T) {
	var otherHits atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		otherHits.Add(1)
	}))
	defer other.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL, http.StatusFound)
	}))
	defer origin.Close()

	hc := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	resp, err := New(nil, WithHTTPClient(hc)).Get(context.Background(), origin.URL)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusFound, resp.StatusCode)
	assert.Zero(t, otherHits.Load())
}

func TestClient_Do_RefusesPaymentAfterRedirectThroughAnotherOrigin(t *testing.T) {
	var challenge *mpp.Challenge
	var otherURL string
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/start":
			http.Redirect(w, r, otherURL, http.StatusFound)
		case "/priced":
			w.Header().Set("WWW-Authenticate", mpptest.FormatChallenge(challenge))
			w.WriteHeader(http.StatusPaymentRequired)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer origin.Close()
	challenge = challengeForURL(t, origin.URL, "tempo", nil)
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, origin.URL+"/priced", http.StatusFound)
	}))
	defer other.Close()
	otherURL = other.URL

	method := &mockMethod{name: "tempo", cred: newTestCredential("tempo")}
	// origin/start -> other -> origin/priced: other would choose the paid
	// resource, so the first hop away from origin is refused.
	_, err := New([]Method{method}).Get(context.Background(), origin.URL+"/start")
	require.ErrorContains(t, err, "refusing cross-origin redirect")
	assert.Zero(t, method.calls)
}

func TestClient_Do_KeepsUnknownOutcomeOnClientTimeout(t *testing.T) {
	var challenge *mpp.Challenge
	release := make(chan struct{})
	defer close(release)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			w.Header().Set("WWW-Authenticate", mpptest.FormatChallenge(challenge))
			w.WriteHeader(http.StatusPaymentRequired)
			return
		}
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	challenge = challengeForURL(t, srv.URL, "tempo", nil)

	cred := newTestCredential("tempo")
	method := &mockMethod{name: "tempo", cred: cred}
	c := New([]Method{method}, WithHTTPClient(&http.Client{Timeout: 300 * time.Millisecond}))
	_, err := c.Get(context.Background(), srv.URL)
	var unknown *PaymentOutcomeUnknownError
	require.ErrorAs(t, err, &unknown)
	assert.Equal(t, challenge.ID, unknown.Challenge.ID)
	assert.Same(t, cred, unknown.Credential)
	var urlErr *url.Error
	require.ErrorAs(t, err, &urlErr)
	// Whether net/http replaces the error with its own "Client.Timeout
	// exceeded" text depends on which deadline fires first; either way the
	// error must stay a PaymentOutcomeUnknownError that reports a timeout.
	assert.True(t, urlErr.Timeout())
}

func TestTransport_RoundTrip_DoesNotPayAgainOnOutcomeUnknownProblem(t *testing.T) {
	challenge := challengeForURL(t, "http://api.example.com", "tempo", nil)
	_, authorization := callerAuthorization(t, challenge)
	problem := `{"type":"https://paymentauth.org/problems/payment-outcome-unknown","status":402}`
	respond := func(req *http.Request, body string) *http.Response {
		header := http.Header{}
		// A server may still offer a challenge next to the problem.
		header.Set(mpp.HeaderWWWAuthenticate, mpptest.FormatChallenge(challenge))
		if body != "" {
			header.Set("Content-Type", "application/problem+json")
		}
		return &http.Response{StatusCode: http.StatusPaymentRequired, Header: header, Body: io.NopCloser(strings.NewReader(body)), Request: req}
	}

	for _, tc := range []struct {
		name           string
		caller         bool
		wantRoundTrips int
		wantPayments   int
	}{
		{name: "sdk credential", wantRoundTrips: 2, wantPayments: 1},
		{name: "caller credential", caller: true, wantRoundTrips: 1, wantPayments: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			roundTrips := 0
			inner := roundTripFunc(func(req *http.Request) (*http.Response, error) {
				roundTrips++
				if req.Header.Get(mpp.HeaderAuthorization) == "" {
					return respond(req, ""), nil
				}
				return respond(req, problem), nil
			})
			method := &mockMethod{name: "tempo", cred: newTestCredential("tempo")}
			req, err := http.NewRequest(http.MethodGet, "http://api.example.com", nil)
			require.NoError(t, err)
			if tc.caller {
				req.Header.Set(mpp.HeaderAuthorization, authorization)
			}

			resp, err := newTestTransport([]Method{method}, inner).RoundTrip(req)
			require.NoError(t, err)
			defer resp.Body.Close()
			assert.Equal(t, http.StatusPaymentRequired, resp.StatusCode)
			body, err := io.ReadAll(resp.Body)
			require.NoError(t, err)
			assert.JSONEq(t, problem, string(body), "the caller still reads the server's problem")
			assert.Equal(t, tc.wantRoundTrips, roundTrips)
			assert.Equal(t, tc.wantPayments, method.calls)
		})
	}
}
