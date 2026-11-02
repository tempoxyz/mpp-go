package client

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tempoxyz/mpp-go/pkg/internal/mpptest"
	"github.com/tempoxyz/mpp-go/pkg/mpp"
)

func TestTransportCallerCredentialRetry(t *testing.T) {
	for _, tc := range []struct {
		name, problem      string
		different          bool
		payments, requests int
	}{
		{name: "receipt recovery", payments: 0, requests: 2},
		{name: "invalid payload", problem: "invalid-payload", payments: 1, requests: 2},
		{name: "different challenge", different: true, payments: 1, requests: 2},
		{name: "unknown outcome", problem: "payment-outcome-unknown", payments: 0, requests: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			challenge := challengeForURL(t, "http://api.example.com", "tempo", nil)
			_, authorization := callerAuthorization(t, challenge)
			offered := challenge
			if tc.different {
				offered = challengeForURL(t, "http://api.example.com", "tempo", map[string]any{"amount": "2"})
			}
			method := &mockMethod{name: "tempo", cred: offered.NewCredential(map[string]any{"hash": "replacement"})}
			attempts := 0
			inner := roundTripFunc(func(r *http.Request) (*http.Response, error) {
				attempts++
				if attempts == 2 {
					if tc.payments == 0 {
						assert.Equal(t, authorization, r.Header.Get("Authorization"))
					} else {
						assert.NotEqual(t, authorization, r.Header.Get("Authorization"))
					}
					return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("ok"))}, nil
				}
				header := make(http.Header)
				header.Set(mpp.HeaderWWWAuthenticate, mpptest.FormatChallenge(offered))
				header.Set("Content-Type", "application/problem+json")
				return &http.Response{StatusCode: http.StatusPaymentRequired, Header: header, Body: io.NopCloser(strings.NewReader(`{"type":"https://paymentauth.org/problems/` + tc.problem + `"}`))}, nil
			})
			req, err := http.NewRequest(http.MethodGet, "http://api.example.com", nil)
			require.NoError(t, err)
			req.Header.Set("Authorization", authorization)
			response, err := newTestTransport([]Method{method}, inner).RoundTrip(req)
			if tc.problem == "payment-outcome-unknown" {
				var unknown *PaymentOutcomeUnknownError
				require.ErrorAs(t, err, &unknown)
				require.NotNil(t, unknown.Credential)
			} else {
				require.NoError(t, err)
				defer response.Body.Close()
			}
			assert.Equal(t, tc.payments, method.calls)
			assert.Equal(t, tc.requests, attempts)
		})
	}
}
