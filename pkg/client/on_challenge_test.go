package client

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tempoxyz/mpp-go/pkg/mpp"
)

type onChallengeCtxKey struct{}

// echoMethod creates credentials that echo the challenge, so the test server
// can parse them, and counts calls and records the context value it saw.
type echoMethod struct {
	calls int
	err   error
	got   any
}

func (m *echoMethod) Name() string   { return "tempo" }
func (m *echoMethod) Intent() string { return "payment" }
func (m *echoMethod) CreateCredential(ctx context.Context, ch *mpp.Challenge) (*mpp.Credential, error) {
	m.calls++
	m.got = ctx.Value(onChallengeCtxKey{})
	if m.err != nil {
		return nil, m.err
	}
	return &mpp.Credential{Challenge: ch.ToEcho(), Source: "test", Payload: map[string]any{"type": "hash"}}, nil
}

// onChallengeServer answers 402 with challenge until a Payment credential
// arrives, and records the credential source it received.
func onChallengeServer(t *testing.T) (*httptest.Server, *int, *[]string) {
	t.Helper()
	calls := 0
	var sources []string
	var challenge *mpp.Challenge
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		auth := r.Header.Get("Authorization")
		if auth == "" {
			w.Header().Set("WWW-Authenticate", challenge.ToAuthenticate(challenge.Realm))
			w.WriteHeader(http.StatusPaymentRequired)
			return
		}
		cred, err := mpp.ParseCredential(auth)
		require.NoError(t, err)
		sources = append(sources, cred.Source)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	challenge = challengeForURL(t, srv.URL, "tempo", nil)
	return srv, &calls, &sources
}

func TestTransport_OnChallenge_DeclineReturnsErrorWithoutPaying(t *testing.T) {
	srv, calls, _ := onChallengeServer(t)
	declined := errors.New("amount too high")
	method := &echoMethod{}
	var seen *mpp.Challenge
	tr := NewTransport([]Method{method}, nil, WithTransportOnChallenge(
		func(_ context.Context, ch *mpp.Challenge, _ func(context.Context) (*mpp.Credential, error)) (*mpp.Credential, error) {
			seen = ch
			return nil, declined
		}))

	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	resp, err := tr.RoundTrip(req)
	assert.Nil(t, resp)
	require.ErrorIs(t, err, declined)
	assert.Contains(t, err.Error(), "onChallenge")
	assert.Equal(t, 0, method.calls, "declined payment must not create a credential")
	assert.Equal(t, 1, *calls, "declined payment must not retry")
	require.NotNil(t, seen)
	assert.Equal(t, "tempo", seen.Method)
	assert.Equal(t, "payment", seen.Intent)
}

func TestTransport_OnChallenge_NilCredentialUsesMethod(t *testing.T) {
	srv, calls, sources := onChallengeServer(t)
	method := &echoMethod{}
	hookCalls := 0
	tr := NewTransport([]Method{method}, nil, WithTransportOnChallenge(
		func(context.Context, *mpp.Challenge, func(context.Context) (*mpp.Credential, error)) (*mpp.Credential, error) {
			hookCalls++
			return nil, nil
		}))

	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	resp, err := tr.RoundTrip(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, 1, hookCalls)
	assert.Equal(t, 1, method.calls)
	assert.Equal(t, 2, *calls)
	assert.Equal(t, []string{"test"}, *sources)
}

func TestTransport_OnChallenge_ReturnedCredentialIsSent(t *testing.T) {
	srv, _, sources := onChallengeServer(t)
	method := &echoMethod{}
	tr := NewTransport([]Method{method}, nil, WithTransportOnChallenge(
		func(_ context.Context, ch *mpp.Challenge, _ func(context.Context) (*mpp.Credential, error)) (*mpp.Credential, error) {
			return &mpp.Credential{Challenge: ch.ToEcho(), Source: "from-hook", Payload: map[string]any{"type": "hash"}}, nil
		}))

	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	resp, err := tr.RoundTrip(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, 0, method.calls, "a hook credential replaces the method's")
	assert.Equal(t, []string{"from-hook"}, *sources)
}

func TestTransport_OnChallenge_CreateCredentialPassesContext(t *testing.T) {
	srv, _, sources := onChallengeServer(t)
	method := &echoMethod{}
	tr := NewTransport([]Method{method}, nil, WithTransportOnChallenge(
		func(ctx context.Context, _ *mpp.Challenge, create func(context.Context) (*mpp.Credential, error)) (*mpp.Credential, error) {
			return create(context.WithValue(ctx, onChallengeCtxKey{}, "per-request"))
		}))

	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	resp, err := tr.RoundTrip(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, "per-request", method.got)
	assert.Equal(t, 1, method.calls)
	assert.Equal(t, []string{"test"}, *sources)
}

func TestTransport_OnChallenge_CreateCredentialErrorIsWrapped(t *testing.T) {
	srv, calls, _ := onChallengeServer(t)
	boom := errors.New("signer offline")
	method := &echoMethod{err: boom}
	tr := NewTransport([]Method{method}, nil, WithTransportOnChallenge(
		func(ctx context.Context, _ *mpp.Challenge, create func(context.Context) (*mpp.Credential, error)) (*mpp.Credential, error) {
			return create(ctx)
		}))

	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	_, err := tr.RoundTrip(req)
	require.ErrorIs(t, err, boom)
	assert.True(t, strings.HasPrefix(err.Error(), "mpp: creating credential for method \"tempo\""),
		"a forwarded method error must not be reported as an onChallenge decline: %v", err)
	assert.Equal(t, 1, *calls)
}

func TestTransport_OnChallenge_NotCalledForReusedCredential(t *testing.T) {
	calls := 0
	var challenge *mpp.Challenge
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.Header().Set("WWW-Authenticate", challenge.ToAuthenticate(challenge.Realm))
		w.WriteHeader(http.StatusPaymentRequired)
	}))
	defer srv.Close()
	challenge = challengeForURL(t, srv.URL, "tempo", nil)

	method := &echoMethod{}
	hookCalls := 0
	tr := NewTransport([]Method{method}, nil, WithTransportOnChallenge(
		func(context.Context, *mpp.Challenge, func(context.Context) (*mpp.Credential, error)) (*mpp.Credential, error) {
			hookCalls++
			return nil, nil
		}))

	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	resp, err := tr.RoundTrip(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusPaymentRequired, resp.StatusCode)
	assert.Equal(t, defaultMaxPaymentRetries+1, calls)
	assert.Equal(t, 1, hookCalls, "the same challenge must not be approved twice")
	assert.Equal(t, 1, method.calls)
}

func TestClient_WithOnChallenge(t *testing.T) {
	srv, calls, _ := onChallengeServer(t)
	declined := errors.New("not today")
	method := &echoMethod{}
	c := New([]Method{method}, WithOnChallenge(
		func(context.Context, *mpp.Challenge, func(context.Context) (*mpp.Credential, error)) (*mpp.Credential, error) {
			return nil, declined
		}))

	_, err := c.Get(context.Background(), srv.URL)
	require.ErrorIs(t, err, declined)
	assert.Equal(t, 0, method.calls)
	assert.Equal(t, 1, *calls)
}

func TestTransport_OnChallenge_NilAfterCreateDoesNotCreateTwice(t *testing.T) {
	srv, calls, sources := onChallengeServer(t)
	method := &echoMethod{}
	tr := NewTransport([]Method{method}, nil, WithTransportOnChallenge(
		func(ctx context.Context, _ *mpp.Challenge, create func(context.Context) (*mpp.Credential, error)) (*mpp.Credential, error) {
			if _, err := create(ctx); err != nil {
				return nil, err
			}
			_, _ = create(ctx) // a second call returns the first result
			return nil, nil
		}))

	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	resp, err := tr.RoundTrip(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, 1, method.calls, "the method may broadcast a payment, so it must run once")
	assert.Equal(t, 2, *calls)
	assert.Equal(t, []string{"test"}, *sources)
}

func TestTransport_OnChallenge_NilAfterFailedCreateReturnsThatError(t *testing.T) {
	srv, calls, _ := onChallengeServer(t)
	boom := errors.New("signer offline")
	method := &echoMethod{err: boom}
	tr := NewTransport([]Method{method}, nil, WithTransportOnChallenge(
		func(ctx context.Context, _ *mpp.Challenge, create func(context.Context) (*mpp.Credential, error)) (*mpp.Credential, error) {
			_, _ = create(ctx) // logged and ignored
			return nil, nil
		}))

	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	_, err := tr.RoundTrip(req)
	require.ErrorIs(t, err, boom)
	assert.Equal(t, 1, method.calls)
	assert.Equal(t, 1, *calls)
}
