package client

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/tempoxyz/mpp-go/pkg/mpp"
)

// newPaymentRequiredServer answers every request with a Payment challenge and
// counts how many requests reached it.
func newPaymentRequiredServer(t *testing.T, calls *int) *httptest.Server {
	t.Helper()
	var challenge *mpp.Challenge
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*calls++
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("WWW-Authenticate", challenge.ToAuthenticate(challenge.Realm))
		w.WriteHeader(http.StatusPaymentRequired)
	}))
	challenge = challengeForURL(t, srv.URL, "tempo", nil)
	return srv
}

// trackedBody records whether a body handed out by GetBody was closed.
type trackedBody struct {
	io.Reader
	closed bool
}

func (b *trackedBody) Close() error {
	b.closed = true
	return nil
}

func TestTransport_RoundTrip_DoesNotPayForUnreplayableBody(t *testing.T) {
	calls := 0
	srv := newPaymentRequiredServer(t, &calls)
	defer srv.Close()

	method := &mockMethod{name: "tempo", cred: newTestCredential("tempo")}
	tr := NewTransport([]Method{method}, nil)
	req, _ := http.NewRequest(http.MethodPost, srv.URL, strings.NewReader("request-body"))
	req.GetBody = nil // a streamed body: once sent, it cannot be sent again

	resp, err := tr.RoundTrip(req)
	if resp != nil {
		resp.Body.Close()
	}
	assert.ErrorContainsf(t, err, "GetBody is not set",
		"RoundTrip() error = %v, want an unreplayable-body error", err)
	assert.Equalf(t, 0, method.calls,
		"CreateCredential() calls = %d, want 0: the paid retry could never be sent", method.calls)
	assert.Equalf(t, 1, calls, "server calls = %d, want 1", calls)
}

func TestTransport_RoundTrip_DoesNotPayWhenGetBodyFails(t *testing.T) {
	calls := 0
	srv := newPaymentRequiredServer(t, &calls)
	defer srv.Close()

	method := &mockMethod{name: "tempo", cred: newTestCredential("tempo")}
	tr := NewTransport([]Method{method}, nil)
	req, _ := http.NewRequest(http.MethodPost, srv.URL, strings.NewReader("request-body"))
	req.GetBody = func() (io.ReadCloser, error) {
		return nil, errors.New("body source is gone")
	}

	resp, err := tr.RoundTrip(req)
	if resp != nil {
		resp.Body.Close()
	}
	assert.ErrorContainsf(t, err, "body source is gone",
		"RoundTrip() error = %v, want the GetBody error", err)
	assert.Equalf(t, 0, method.calls,
		"CreateCredential() calls = %d, want 0: the paid retry could never be sent", method.calls)
}

func TestTransport_RoundTrip_ClosesReplayBodyWhenCredentialFails(t *testing.T) {
	calls := 0
	srv := newPaymentRequiredServer(t, &calls)
	defer srv.Close()

	method := &mockMethod{name: "tempo", err: errors.New("wallet locked")}
	tr := NewTransport([]Method{method}, nil)
	var opened []*trackedBody
	req, _ := http.NewRequest(http.MethodPost, srv.URL, strings.NewReader("request-body"))
	req.GetBody = func() (io.ReadCloser, error) {
		body := &trackedBody{Reader: strings.NewReader("request-body")}
		opened = append(opened, body)
		return body, nil
	}

	resp, err := tr.RoundTrip(req)
	if resp != nil {
		resp.Body.Close()
	}
	assert.ErrorContainsf(t, err, "wallet locked",
		"RoundTrip() error = %v, want the CreateCredential error", err)
	assert.Lenf(t, opened, 1,
		"GetBody calls = %d, want 1: the retry is prepared before the credential is created", len(opened))
	for i, body := range opened {
		assert.Truef(t, body.closed, "replay body %d was opened for a retry that was never sent and not closed", i)
	}
}
