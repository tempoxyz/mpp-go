package client

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

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

func TestClient_Do_FollowsCrossOriginRedirectWithoutPayment(t *testing.T) {
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "free")
	}))
	defer other.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL, http.StatusFound)
	}))
	defer origin.Close()

	resp, err := New(nil).Get(context.Background(), origin.URL)
	require.NoError(t, err)
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	assert.Equal(t, "free", string(body))
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
			w.Header().Set("WWW-Authenticate", challenge.ToAuthenticate(challenge.Realm))
			w.WriteHeader(http.StatusPaymentRequired)
			return
		}
		http.Redirect(w, r, other.URL, http.StatusTemporaryRedirect)
	}))
	defer origin.Close()
	challenge = challengeForURL(t, origin.URL, "tempo", nil)

	method := &mockMethod{name: "tempo", cred: newTestCredential("tempo")}
	resp, err := New([]Method{method}).Get(context.Background(), origin.URL)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, 1, method.calls)
	mu.Lock()
	defer mu.Unlock()
	require.NotEmpty(t, otherAuth)
	for _, value := range otherAuth {
		assert.NotContains(t, strings.ToLower(value), "payment")
	}
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
}

func (c *closeTracker) Close() error {
	c.closed.Store(true)
	return nil
}

func TestTransport_RoundTrip_ClosesBodyOnConfigError(t *testing.T) {
	method := &mockMethod{name: "tempo"}
	tr := NewTransport([]Method{method}, http.DefaultTransport, WithTransportPaymentPreferences(PaymentPreferences{"unknown/charge": 1}))
	body := &closeTracker{Reader: strings.NewReader("x")}
	req, err := http.NewRequest(http.MethodPost, "http://127.0.0.1:1", body)
	require.NoError(t, err)

	_, err = tr.RoundTrip(req)
	require.Error(t, err)
	assert.True(t, body.closed.Load())
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestTransport_RoundTrip_ClosesBodyWhenRetryCannotBeBuilt(t *testing.T) {
	challenge := challengeForURL(t, "http://api.example.com", "tempo", nil)
	inner := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		header := http.Header{}
		header.Set(mpp.HeaderWWWAuthenticate, challenge.ToAuthenticate(challenge.Realm))
		return &http.Response{StatusCode: http.StatusPaymentRequired, Header: header, Body: io.NopCloser(strings.NewReader("")), Request: req}, nil
	})
	method := &mockMethod{name: "tempo", cred: newTestCredential("tempo")}
	body := &closeTracker{Reader: strings.NewReader("x")}
	req, err := http.NewRequest(http.MethodPost, "http://api.example.com", body)
	require.NoError(t, err)
	req.GetBody = nil

	_, err = NewTransport([]Method{method}, inner).RoundTrip(req)
	require.ErrorContains(t, err, "cloning request for retry")
	assert.True(t, body.closed.Load())
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
			header.Set(mpp.HeaderWWWAuthenticate, challenge.ToAuthenticate(challenge.Realm))
			return &http.Response{StatusCode: http.StatusPaymentRequired, Header: header, Body: io.NopCloser(strings.NewReader("")), Request: req}, nil
		}
		return nil, networkErr
	})
	cred := newTestCredential("tempo")
	method := &mockMethod{name: "tempo", cred: cred}
	req, err := http.NewRequest(http.MethodGet, "http://api.example.com", nil)
	require.NoError(t, err)

	_, err = (&http.Client{Transport: NewTransport([]Method{method}, inner)}).Do(req)
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

	_, err = NewTransport(nil, inner).RoundTrip(req)
	assert.Same(t, networkErr, err)
}
