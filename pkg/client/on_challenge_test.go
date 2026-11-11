package client

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tempoxyz/mpp-go/pkg/internal/mpptest"

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
			w.Header().Set("WWW-Authenticate", mpptest.FormatChallenge(challenge))
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
	tr := newTestTransport([]Method{method}, nil, WithOnChallenge(
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
	tr := newTestTransport([]Method{method}, nil, WithOnChallenge(
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
	tr := newTestTransport([]Method{method}, nil, WithOnChallenge(
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
	tr := newTestTransport([]Method{method}, nil, WithOnChallenge(
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
	tr := newTestTransport([]Method{method}, nil, WithOnChallenge(
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
		w.Header().Set("WWW-Authenticate", mpptest.FormatChallenge(challenge))
		w.WriteHeader(http.StatusPaymentRequired)
	}))
	defer srv.Close()
	challenge = challengeForURL(t, srv.URL, "tempo", nil)

	method := &echoMethod{}
	hookCalls := 0
	tr := newTestTransport([]Method{method}, nil, WithOnChallenge(
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
	tr := newTestTransport([]Method{method}, nil, WithOnChallenge(
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
	tr := newTestTransport([]Method{method}, nil, WithOnChallenge(
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

// declineServer answers every request with a 402 carrying a challenge and a
// body, and counts the requests.
func declineServer(t *testing.T) (*httptest.Server, *int) {
	t.Helper()
	calls := 0
	var challenge *mpp.Challenge
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.Header().Set("WWW-Authenticate", mpptest.FormatChallenge(challenge))
		w.WriteHeader(http.StatusPaymentRequired)
		_, _ = w.Write([]byte("pay me"))
	}))
	t.Cleanup(srv.Close)
	challenge = challengeForURL(t, srv.URL, "tempo", nil)
	return srv, &calls
}

func TestTransport_OnChallenge_ErrPaymentDeclinedReturns402(t *testing.T) {
	srv, calls := declineServer(t)
	method := &echoMethod{}
	decline := func(context.Context, *mpp.Challenge, func(context.Context) (*mpp.Credential, error)) (*mpp.Credential, error) {
		return nil, fmt.Errorf("over budget: %w", ErrPaymentDeclined)
	}

	check := func(t *testing.T, resp *http.Response, err error) {
		t.Helper()
		require.NoError(t, err)
		require.NotNil(t, resp)
		defer func() { _ = resp.Body.Close() }()
		assert.Equal(t, http.StatusPaymentRequired, resp.StatusCode)
		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		assert.Equal(t, "pay me", string(body))
		assert.NotEmpty(t, resp.Header.Get("WWW-Authenticate"))
		assert.Zero(t, method.calls, "a declined payment must not create a credential")
	}

	t.Run("Transport", func(t *testing.T) {
		*calls = 0
		tr := newTestTransport([]Method{method}, nil, WithOnChallenge(decline))
		req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
		resp, err := tr.RoundTrip(req)
		check(t, resp, err)
		assert.Equal(t, 1, *calls)
	})
	t.Run("Client", func(t *testing.T) {
		*calls = 0
		resp, err := New([]Method{method}, WithOnChallenge(decline)).Get(context.Background(), srv.URL)
		check(t, resp, err)
		assert.Equal(t, 1, *calls)
	})
	t.Run("HTTPClient", func(t *testing.T) {
		*calls = 0
		hc, err := NewHTTPClient([]Method{method}, WithOnChallenge(declineHook))
		require.NoError(t, err)
		resp, err := hc.Get(srv.URL)
		check(t, resp, err)
		assert.Equal(t, 1, *calls)
	})
}

// declineHook declines every payment with bare ErrPaymentDeclined.
func declineHook(context.Context, *mpp.Challenge, func(context.Context) (*mpp.Credential, error)) (*mpp.Credential, error) {
	return nil, ErrPaymentDeclined
}

func TestTransport_OnChallenge_OtherErrorStillFails(t *testing.T) {
	srv, calls := declineServer(t)
	method := &echoMethod{}
	reason := errors.New("amount too high")
	tr := newTestTransport([]Method{method}, nil, WithOnChallenge(
		func(context.Context, *mpp.Challenge, func(context.Context) (*mpp.Credential, error)) (*mpp.Credential, error) {
			return nil, reason
		}))
	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	resp, err := tr.RoundTrip(req)
	assert.Nil(t, resp)
	require.ErrorIs(t, err, reason)
	require.ErrorIs(t, err, ErrPaymentDeclined)
	assert.Zero(t, method.calls)
	assert.Equal(t, 1, *calls)
}

func TestTransport_OnChallenge_ErrPaymentDeclinedAfterCreateIsError(t *testing.T) {
	srv, _ := declineServer(t)
	method := &echoMethod{}
	tr := newTestTransport([]Method{method}, nil, WithOnChallenge(
		func(ctx context.Context, _ *mpp.Challenge, create func(context.Context) (*mpp.Credential, error)) (*mpp.Credential, error) {
			if _, err := create(ctx); err != nil {
				return nil, err
			}
			return nil, ErrPaymentDeclined
		}))
	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	resp, err := tr.RoundTrip(req)
	assert.Nil(t, resp)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "after creating credential")
	assert.Equal(t, 1, method.calls)
}

func TestTransport_OnChallenge_ErrPaymentDeclinedAfterFailedCreateIsError(t *testing.T) {
	srv, calls := declineServer(t)
	boom := errors.New("broadcast failed after submit")
	method := &echoMethod{err: boom}
	tr := newTestTransport([]Method{method}, nil, WithOnChallenge(
		func(ctx context.Context, _ *mpp.Challenge, create func(context.Context) (*mpp.Credential, error)) (*mpp.Credential, error) {
			_, _ = create(ctx)
			return nil, ErrPaymentDeclined
		}))
	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	resp, err := tr.RoundTrip(req)
	assert.Nil(t, resp, "the method ran, so the 402 is not a clean decline")
	require.ErrorIs(t, err, boom)
	assert.Contains(t, err.Error(), "after creating credential failed")
	assert.Equal(t, 1, method.calls)
	assert.Equal(t, 1, *calls)
}

// A 402 body that stalls after its headers must not hold back the paid retry
// once the method created (and possibly broadcast) a payment.
func TestTransport_StalledChallengeBodyDoesNotDelayPaidRetry(t *testing.T) {
	release := make(chan struct{})
	var once sync.Once
	var challenge *mpp.Challenge
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			once.Do(func() { close(release) })
			w.WriteHeader(http.StatusOK)
			return
		}
		w.Header().Set("WWW-Authenticate", mpptest.FormatChallenge(challenge))
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusPaymentRequired)
		_, _ = w.Write([]byte("payment required"))
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-r.Context().Done():
		case <-time.After(10 * time.Second):
		}
	}))
	defer srv.Close()
	defer once.Do(func() { close(release) })
	challenge = challengeForURL(t, srv.URL, "tempo", nil)

	method := &echoMethod{}
	tr := newTestTransport([]Method{method}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
	resp, err := tr.RoundTrip(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, 1, method.calls)
}
