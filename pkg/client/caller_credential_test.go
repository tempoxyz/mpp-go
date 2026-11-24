package client

import (
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tempoxyz/mpp-go/pkg/internal/mpptest"
	"github.com/tempoxyz/mpp-go/pkg/mpp"
)

func TestTransport_RoundTrip_DoesNotPayAfterCallerCredentialWithLineBreak(t *testing.T) {
	challenge := challengeForURL(t, "http://api.example.com", "tempo", map[string]any{"amount": "1"})
	inner := roundTripFunc(func(*http.Request) (*http.Response, error) {
		header := http.Header{}
		header.Set("WWW-Authenticate", mpptest.FormatChallenge(challenge))
		return &http.Response{StatusCode: http.StatusPaymentRequired, Header: header, Body: http.NoBody}, nil
	})
	method := &mockMethod{name: "tempo", cred: newTestCredential("tempo")}
	authorization, err := mpp.FormatCredential(newTestCredential("tempo"))
	require.NoError(t, err)
	token := strings.TrimPrefix(authorization, "Payment ")
	req, err := http.NewRequest(http.MethodGet, "http://api.example.com", nil)
	require.NoError(t, err)
	// A line break in the token: detected as a Payment credential, but
	// B64Decode rejects it, so the client cannot tell whether it was paid.
	req.Header["Authorization"] = []string{"Payment " + token[:8] + "\n" + token[8:]}

	resp, err := newTestTransport([]Method{method}, inner).RoundTrip(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusPaymentRequired, resp.StatusCode)
	assert.Zero(t, method.calls)
}
