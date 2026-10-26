package client

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tempoxyz/mpp-go/pkg/mpp"
)

// payingServer answers /paid with 402 and the challenges until a Payment
// credential arrives, and counts the paid requests.
func payingServer(t *testing.T, challenges func(host string) []*mpp.Challenge) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var paid atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, "/paid", http.StatusFound)
			return
		}
		if r.Header.Get("Authorization") != "" {
			paid.Add(1)
			w.WriteHeader(http.StatusOK)
			return
		}
		for _, challenge := range challenges(r.Host) {
			header, err := mpp.FormatChallenge(challenge)
			if err != nil {
				panic(err)
			}
			w.Header().Add("WWW-Authenticate", header)
		}
		w.WriteHeader(http.StatusPaymentRequired)
	}))
	t.Cleanup(srv.Close)
	return srv, &paid
}

func paymentChallenge(host string) []*mpp.Challenge {
	return []*mpp.Challenge{mustChallenge(host, "test", "payment", map[string]any{"amount": "1"})}
}

func TestNewHTTPClientPaysAndFollowsSameOriginRedirects(t *testing.T) {
	srv, paid := payingServer(t, paymentChallenge)
	method := &mockMethod{name: "test", cred: newTestCredential("test")}
	hc, err := NewHTTPClient([]Method{method})
	require.NoError(t, err)

	resp, err := hc.Get(srv.URL + "/redirect")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, 1, method.calls)
	assert.Equal(t, int32(1), paid.Load())
}

func TestNewHTTPClientRefusesCrossOriginRedirects(t *testing.T) {
	target, _ := payingServer(t, paymentChallenge)
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/paid", http.StatusFound)
	}))
	defer origin.Close()
	method := &mockMethod{name: "test", cred: newTestCredential("test")}
	base := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return nil }}
	hc, err := NewHTTPClient([]Method{method}, WithHTTPClient(base))
	require.NoError(t, err)
	assert.NotSame(t, base, hc)
	assert.Nil(t, base.Transport, "the caller's http.Client must not be modified")

	_, err = hc.Get(origin.URL)
	require.ErrorContains(t, err, "refusing cross-origin redirect")
	assert.Zero(t, method.calls)
}

func TestNewHTTPClientReportsConfigErrors(t *testing.T) {
	method := &mockMethod{name: "test"}
	_, err := NewHTTPClient([]Method{method}, WithPaymentPreferences(PaymentPreferences{"unknown/charge": 1}))
	require.Error(t, err)
	_, err = NewHTTPTransport([]Method{method}, nil, WithMaxRetries(-1))
	require.ErrorContains(t, err, "must not be negative")
	transport, err := NewHTTPTransport([]Method{method}, nil)
	require.NoError(t, err)
	assert.NotNil(t, transport)
}

func TestWithMaxRetriesZeroNeverPays(t *testing.T) {
	srv, paid := payingServer(t, paymentChallenge)
	method := &mockMethod{name: "test", cred: newTestCredential("test")}
	c := New([]Method{method}, WithMaxRetries(0))
	resp, err := c.Get(context.Background(), srv.URL+"/paid")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusPaymentRequired, resp.StatusCode)
	assert.Zero(t, method.calls)
	assert.Zero(t, paid.Load())
}

func TestWithOrderChallengesPicksTheFirstReturned(t *testing.T) {
	srv, _ := payingServer(t, func(host string) []*mpp.Challenge {
		return []*mpp.Challenge{
			mustChallenge(host, "first", "payment", map[string]any{"amount": "1"}),
			mustChallenge(host, "second", "payment", map[string]any{"amount": "2"}),
		}
	})
	first := &mockMethod{name: "first", cred: newTestCredential("first")}
	second := &mockMethod{name: "second", cred: newTestCredential("second")}
	var offered []string
	reverse := func(_ context.Context, candidates []ChallengeCandidate) ([]ChallengeCandidate, error) {
		for _, candidate := range candidates {
			offered = append(offered, candidate.Challenge.Method)
		}
		return []ChallengeCandidate{candidates[1], candidates[0]}, nil
	}
	c := New([]Method{first, second}, WithOrderChallenges(reverse))
	resp, err := c.Get(context.Background(), srv.URL+"/paid")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, []string{"first", "second"}, offered)
	assert.Zero(t, first.calls)
	assert.Equal(t, 1, second.calls)

	// An empty order pays nothing and returns the 402; an error fails.
	none := func(context.Context, []ChallengeCandidate) ([]ChallengeCandidate, error) { return nil, nil }
	resp, err = New([]Method{first, second}, WithOrderChallenges(none)).Get(context.Background(), srv.URL+"/paid")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusPaymentRequired, resp.StatusCode)
	boom := errors.New("boom")
	failing := func(context.Context, []ChallengeCandidate) ([]ChallengeCandidate, error) { return nil, boom }
	_, err = New([]Method{first, second}, WithOrderChallenges(failing)).Get(context.Background(), srv.URL+"/paid")
	require.ErrorIs(t, err, boom)
	assert.Equal(t, 1, second.calls)
}

func TestOnChallengeDeclineMatchesErrPaymentDeclined(t *testing.T) {
	srv, _ := payingServer(t, paymentChallenge)
	method := &mockMethod{name: "test", cred: newTestCredential("test")}
	reason := errors.New("over budget")
	decline := func(context.Context, *mpp.Challenge, func(context.Context) (*mpp.Credential, error)) (*mpp.Credential, error) {
		return nil, reason
	}
	_, err := New([]Method{method}, WithOnChallenge(decline)).Get(context.Background(), srv.URL+"/paid")
	require.ErrorIs(t, err, ErrPaymentDeclined)
	require.ErrorIs(t, err, reason)
	require.ErrorContains(t, err, "mpp: onChallenge: over budget")
	assert.Zero(t, method.calls)

	// A payment-method failure the hook forwards is not a decline.
	method.err = errors.New("insufficient funds")
	forward := func(ctx context.Context, _ *mpp.Challenge, create func(context.Context) (*mpp.Credential, error)) (*mpp.Credential, error) {
		return create(ctx)
	}
	_, err = New([]Method{method}, WithOnChallenge(forward)).Get(context.Background(), srv.URL+"/paid")
	require.ErrorIs(t, err, method.err)
	assert.NotErrorIs(t, err, ErrPaymentDeclined)
}

// originMethod is a mockMethod that validates origins.
type originMethod struct {
	mockMethod
	validated []string
	err       error
}

func (m *originMethod) ValidateOrigin(origin *url.URL, _ *mpp.Challenge) error {
	m.validated = append(m.validated, requestOrigin(origin))
	return m.err
}

func TestTransportDefersToOriginValidator(t *testing.T) {
	// A zero-amount tempo/charge challenge for a foreign realm: the built-in
	// check would refuse it, but the method's own validator decides.
	srv, paid := payingServer(t, func(string) []*mpp.Challenge {
		return []*mpp.Challenge{mustChallenge("target.example", "tempo", "charge", map[string]any{"amount": "0"})}
	})
	method := &originMethod{mockMethod: mockMethod{name: "tempo", intent: "charge", cred: newTestCredential("tempo")}}
	resp, err := New([]Method{method}).Get(context.Background(), srv.URL+"/paid")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, []string{requestOrigin(mustParseURL(t, srv.URL))}, method.validated)
	assert.Equal(t, int32(1), paid.Load())

	method.err = errors.New("untrusted realm")
	_, err = New([]Method{method}).Get(context.Background(), srv.URL+"/paid")
	require.ErrorIs(t, err, method.err)

	// The deprecated origin pin still overrides the validator.
	resp, err = New([]Method{method}, WithTrustedTempoProofRealm(srv.URL, "target.example")).Get(context.Background(), srv.URL+"/paid")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

func mustChallenge(realm, method, intent string, request map[string]any) *mpp.Challenge {
	challenge, err := mpp.NewChallengeWithError("secret", realm, method, intent, request)
	if err != nil {
		panic(err)
	}
	return challenge
}

func mustParseURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	require.NoError(t, err)
	return u
}
