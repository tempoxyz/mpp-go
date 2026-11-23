package client

import (
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tempoxyz/mpp-go/pkg/internal/mpptest"
	"github.com/tempoxyz/mpp-go/pkg/mpp"
)

// TestTransport_RoundTrip_DoesNotPayAfterUnreadableLaterAuthorizationValue
// checks every Authorization value, not only the first: a later Payment value
// that does not parse may name a payment already made.
func TestTransport_RoundTrip_DoesNotPayAfterUnreadableLaterAuthorizationValue(t *testing.T) {
	challenge := challengeForURL(t, "http://api.example.com", "tempo", map[string]any{"amount": "1"})
	inner := roundTripFunc(func(*http.Request) (*http.Response, error) {
		header := http.Header{}
		header.Set("WWW-Authenticate", mpptest.FormatChallenge(challenge))
		return &http.Response{StatusCode: http.StatusPaymentRequired, Header: header, Body: http.NoBody}, nil
	})
	for _, name := range []string{"Authorization", "Payment-Authorization"} {
		t.Run(name, func(t *testing.T) {
			method := &mockMethod{name: "tempo", cred: newTestCredential("tempo")}
			req, err := http.NewRequest(http.MethodGet, "http://api.example.com", nil)
			require.NoError(t, err)
			// "e30" decodes to {}, which is not a Credential.
			req.Header[name] = []string{"Basic dXNlcjpwYXNz", "Payment e30"}

			resp, err := newTestTransport([]Method{method}, inner).RoundTrip(req)
			require.NoError(t, err)
			defer resp.Body.Close()
			assert.Equal(t, http.StatusPaymentRequired, resp.StatusCode)
			assert.Zero(t, method.calls)
		})
	}
}

// TestTransport_RoundTrip_PrefersAuthorizationOverOtherPaymentShapedHeader
// reads the caller's Credential from Authorization even when another field
// holds a Payment-shaped value that does not parse, whatever the order the
// header map yields them in.
func TestTransport_RoundTrip_PrefersAuthorizationOverOtherPaymentShapedHeader(t *testing.T) {
	challenge := challengeForURL(t, "http://api.example.com", "tempo", map[string]any{"amount": "1"})
	authorization, err := mpp.FormatCredential(newTestCredential("tempo"))
	require.NoError(t, err)
	// Map iteration order varies, so repeat to cover both orders.
	for range 32 {
		var calls atomic.Int32
		inner := roundTripFunc(func(*http.Request) (*http.Response, error) {
			if calls.Add(1) > 1 {
				return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: http.NoBody}, nil
			}
			header := http.Header{}
			header.Set("WWW-Authenticate", mpptest.FormatChallenge(challenge))
			return &http.Response{StatusCode: http.StatusPaymentRequired, Header: header, Body: http.NoBody}, nil
		})
		method := &mockMethod{name: "tempo", cred: newTestCredential("tempo")}
		req, err := http.NewRequest(http.MethodGet, "http://api.example.com", nil)
		require.NoError(t, err)
		req.Header.Set("Authorization", authorization)
		req.Header.Set("X-Status", "Payment pending")

		resp, err := newTestTransport([]Method{method}, inner).RoundTrip(req)
		require.NoError(t, err)
		resp.Body.Close()
		require.Equal(t, http.StatusOK, resp.StatusCode)
		require.Equal(t, 1, method.calls)
	}
}

// TestTransport_RoundTrip_PaysDespitePaymentInQuotedAuthParam reads no Payment
// credential from inside another scheme's quoted auth-param, as servers do
// not, so it pays the 402.
func TestTransport_RoundTrip_PaysDespitePaymentInQuotedAuthParam(t *testing.T) {
	challenge := challengeForURL(t, "http://api.example.com", "tempo", map[string]any{"amount": "1"})
	for _, name := range []string{"Authorization", "Payment-Authorization"} {
		t.Run(name, func(t *testing.T) {
			var calls atomic.Int32
			inner := roundTripFunc(func(*http.Request) (*http.Response, error) {
				if calls.Add(1) > 1 {
					return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: http.NoBody}, nil
				}
				header := http.Header{}
				header.Set("WWW-Authenticate", mpptest.FormatChallenge(challenge))
				return &http.Response{StatusCode: http.StatusPaymentRequired, Header: header, Body: http.NoBody}, nil
			})
			method := &mockMethod{name: "tempo", cred: newTestCredential("tempo")}
			req, err := http.NewRequest(http.MethodGet, "http://api.example.com", nil)
			require.NoError(t, err)
			req.Header.Set(name, `Digest username="alice, Payment e30, bob", realm="api"`)

			resp, err := newTestTransport([]Method{method}, inner).RoundTrip(req)
			require.NoError(t, err)
			defer resp.Body.Close()
			assert.Equal(t, http.StatusOK, resp.StatusCode)
			assert.Equal(t, 1, method.calls)
		})
	}
}
