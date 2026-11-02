package server

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tempoxyz/mpp-go/pkg/internal/mpptest"
	"github.com/tempoxyz/mpp-go/pkg/mpp"
)

// mppxScopedChallenge was issued by mppx 0.11.0:
//
//	Challenge.serialize(Challenge.from({
//	  secretKey: 'test-secret-key-minimum-32-byte-secret',
//	  realm: 'api.example.com', method: 'tempo', intent: 'charge',
//	  request: { amount: '0.50', currency: '' },
//	  expires: '2099-01-01T00:00:00.000Z',
//	  meta: { _mppx_scope: 'GET /paid/{id}' },
//	}))
const mppxScopedChallenge = `Payment id="N4I1oMR1Alb0WEqfZYCa8-O5r78H-l6JCalaUo49kZk", realm="api.example.com", method="tempo", intent="charge", request="eyJhbW91bnQiOiIwLjUwIiwiY3VycmVuY3kiOiIifQ", expires="2099-01-01T00:00:00.000Z", opaque="eyJfbXBweF9zY29wZSI6IkdFVCAvcGFpZC97aWR9In0"`

func TestMppxScopeMatchesMppxWireFormat(t *testing.T) {
	// The reference Challenge carries no issuance nonce, so issue without one.
	payment := newTestServer(t, middlewareTestMethod{}, "api.example.com", "test-secret-key-minimum-32-byte-secret", WithChallengeNonce(false))

	// Go issues the same opaque and ID as mppx for the same scope.
	issued, err := payment.Charge(context.Background(), ChargeParams{
		Amount:    "0.50",
		Expires:   "2099-01-01T00:00:00.000Z",
		MppxScope: "GET /paid/{id}",
	})
	require.NoError(t, err)
	require.True(t, issued.IsChallenge())
	header, err := mpp.FormatChallenge(issued.Challenge)
	require.NoError(t, err)
	assert.Equal(t, "N4I1oMR1Alb0WEqfZYCa8-O5r78H-l6JCalaUo49kZk", issued.Challenge.ID)
	assert.Equal(t, `opaque="eyJfbXBweF9zY29wZSI6IkdFVCAvcGFpZC97aWR9In0"`, regexp.MustCompile(`opaque="[^"]*"`).FindString(header))

	// A Credential for the mppx-issued Challenge is accepted on its route and
	// rejected on another one.
	challenge, err := mpp.ParseChallenge(mppxScopedChallenge)
	require.NoError(t, err)
	handler := ChargeMiddleware(payment, ChargeParams{Amount: "0.50"})(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "paid")
	}))
	mux := http.NewServeMux()
	mux.Handle("GET /paid/{id}", handler)
	mux.Handle("GET /other/{id}", handler)
	credential := mpptest.FormatCredential(challenge.NewCredential(map[string]any{"type": "hash", "hash": "0xabc123"}))

	for path, want := range map[string]int{
		"/paid/7?view=full": http.StatusOK,
		"/other/7":          http.StatusPaymentRequired,
	} {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		request.Header.Set(mpp.HeaderAuthorization, credential)
		recorder := httptest.NewRecorder()
		mux.ServeHTTP(recorder, request)
		assert.Equal(t, want, recorder.Code, path)
	}
}

func TestChargeParamsConflictingScopeIsServerError(t *testing.T) {
	payment := newTestServer(t, middlewareTestMethod{}, "api.example.com", "test-secret-key-minimum-32-byte-secret")
	_, err := payment.Charge(context.Background(), ChargeParams{
		Amount:    "0.50",
		Meta:      map[string]string{"_mppx_scope": "GET /a"},
		MppxScope: "GET /b",
	})
	require.ErrorContains(t, err, "conflicting scope values")

	// A scope set through Meta alone is kept by the middleware.
	params := ChargeParams{Meta: map[string]string{"_mppx_scope": "GET /a"}}.WithDefaultScope("GET /b")
	assert.Empty(t, params.MppxScope)
}

func TestWithDefaultScopeKeepsExplicitEmptyMetaScope(t *testing.T) {
	payment := newTestServer(t, middlewareTestMethod{}, "api.example.com", "test-secret-key-minimum-32-byte-secret")
	params := ChargeParams{Amount: "0.50", Meta: map[string]string{"_mppx_scope": ""}}.WithDefaultScope("GET /paid")
	assert.Empty(t, params.MppxScope)
	issued, err := payment.Charge(context.Background(), params)
	require.NoError(t, err)
	assert.True(t, issued.IsChallenge())
}

func TestScopeFromHTTPRequestFallsBackToEscapedPath(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/files/%FF%20a?x=1", nil)
	assert.Equal(t, "GET /files/%FF%20a", ScopeFromHTTPRequest(request, ""))
}
