package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tempoxyz/mpp-go/pkg/internal/mpptest"
	"github.com/tempoxyz/mpp-go/pkg/mpp"
)

// The tests in this file cover the request handling the core specification
// (draft-httpauth-payment) sets for servers.

func specHandler(t *testing.T, opts ...Option) http.Handler {
	t.Helper()
	return ChargeMiddleware(apiTestServer(t, opts...), ChargeParams{Amount: "1.00"})(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
}

func specIssue(t *testing.T, handler http.Handler) *mpp.Challenge {
	t.Helper()
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/premium", nil))
	require.Equal(t, http.StatusPaymentRequired, recorder.Code)
	challenge, err := mpp.ParseChallenge(recorder.Header().Get(mpp.HeaderWWWAuthenticate))
	require.NoError(t, err)
	return challenge
}

func specSend(handler http.Handler, header http.Header) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodGet, "/premium", nil)
	for name, values := range header {
		for _, value := range values {
			r.Header.Add(name, value)
		}
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, r)
	return recorder
}

func specProblemType(t *testing.T, recorder *httptest.ResponseRecorder) string {
	t.Helper()
	if recorder.Body.Len() == 0 {
		return ""
	}
	var problem map[string]any
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &problem))
	typ, _ := problem["type"].(string)
	return typ
}

func TestSpecMultiplePaymentCredentialsAreBadRequest(t *testing.T) {
	t.Parallel()

	handler := specHandler(t)
	challenge := specIssue(t, handler)
	credential := paymentFor(challenge)
	for name, header := range map[string]http.Header{
		"both fields":    {mpp.HeaderAuthorization: {credential}, mpp.HeaderPaymentAuthorization: {credential}},
		"comma-joined":   {mpp.HeaderAuthorization: {credential + ", " + credential}},
		"repeated field": {mpp.HeaderAuthorization: {credential, credential}},
		"oversized comma-joined": {mpp.HeaderAuthorization: {
			credential + ", Payment " + strings.Repeat("A", 70000),
		}},
	} {
		t.Run(name, func(t *testing.T) {
			recorder := specSend(handler, header)
			assert.Equal(t, http.StatusBadRequest, recorder.Code)
			assert.Empty(t, recorder.Header().Values(mpp.HeaderWWWAuthenticate))
			assert.Equal(t, string(mpp.ErrorTypeBadRequest), specProblemType(t, recorder))
		})
	}
}

func TestSpecAbsentAndIgnoredCredentialsGetPaymentRequired(t *testing.T) {
	t.Parallel()

	handler := specHandler(t)
	challenge := specIssue(t, handler)
	for name, header := range map[string]http.Header{
		// A bare scheme with no token counts as absent.
		"bare scheme": {mpp.HeaderAuthorization: {"Payment"}},
		// A credential in a field the Challenge did not select is ignored.
		"unselected field": {mpp.HeaderPaymentAuthorization: {paymentFor(challenge)}},
	} {
		t.Run(name, func(t *testing.T) {
			recorder := specSend(handler, header)
			assert.Equal(t, http.StatusPaymentRequired, recorder.Code)
			assert.Len(t, recorder.Header().Values(mpp.HeaderWWWAuthenticate), 1)
			assert.NotEqual(t, "ok", recorder.Body.String())
			typ := specProblemType(t, recorder)
			assert.True(t, typ == "" || typ == string(mpp.ErrorTypePaymentRequired), "problem type %q", typ)
		})
	}

	// On a handler created WithRequiresAuth, a Payment credential in
	// Authorization is ignored too.
	authed := specHandler(t, WithRequiresAuth(true))
	authedChallenge := specIssue(t, authed)
	recorder := specSend(authed, http.Header{mpp.HeaderAuthorization: {paymentFor(authedChallenge)}})
	assert.Equal(t, http.StatusPaymentRequired, recorder.Code)
	assert.Len(t, recorder.Header().Values(mpp.HeaderWWWAuthenticate), 1)
	assert.NotEqual(t, "ok", recorder.Body.String())
}

func TestSpecMalformedCredentials(t *testing.T) {
	t.Parallel()

	handler := specHandler(t)
	for name, value := range map[string]string{
		"not base64url JSON": "Payment !!!not-json!!!",
		"over 65536 octets":  "Payment " + strings.Repeat("A", 64*1024),
	} {
		t.Run(name, func(t *testing.T) {
			recorder := specSend(handler, http.Header{mpp.HeaderAuthorization: {value}})
			assert.Equal(t, http.StatusPaymentRequired, recorder.Code)
			assert.NotEmpty(t, recorder.Header().Values(mpp.HeaderWWWAuthenticate))
			assert.Equal(t, string(mpp.ErrorTypeMalformedCredential), specProblemType(t, recorder))
		})
	}
}

// replayIntent rejects every Credential as a replay, as a method's replay
// store does for a Challenge or payment already used.
type replayIntent struct{}

func (replayIntent) Name() string { return "charge" }

func (replayIntent) Verify(context.Context, *mpp.Credential, map[string]any) (*mpp.Receipt, error) {
	return nil, mpp.ErrCredentialReplayed("challenge already used")
}

func TestSpecReplayGetsFreshChallengeAndInvalidChallenge(t *testing.T) {
	t.Parallel()

	params := VerifyParams{Intent: replayIntent{}, Request: map[string]any{"amount": "100"}, Realm: "example.com", SecretKey: composeSecret, Method: "tempo", Expires: mpp.ExpiresIn(5 * time.Minute)}
	issued, err := VerifyOrChallenge(context.Background(), params)
	require.NoError(t, err)
	params.Authorization = mpptest.FormatCredential(issued.Challenge.NewCredential(map[string]any{"type": "hash"}))
	result, err := VerifyOrChallenge(context.Background(), params)
	require.ErrorIs(t, err, mpp.ErrorTypeInvalidChallenge)
	var pe *mpp.PaymentError
	require.ErrorAs(t, err, &pe)
	assert.Equal(t, http.StatusPaymentRequired, pe.Status)
	require.NotNil(t, result.Challenge)
	assert.NotEqual(t, issued.Challenge.ID, result.Challenge.ID, "a replay is answered with a fresh Challenge")
}

func TestSpecBindingIsCheckedBeforeExpiry(t *testing.T) {
	t.Parallel()

	params := VerifyParams{Intent: verifyTestIntent{}, Request: map[string]any{"amount": "100"}, Realm: "example.com", SecretKey: composeSecret, Method: "tempo"}
	expired := time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)
	stale := mpptest.NewChallenge(params.SecretKey, params.Realm, params.Method, "charge", params.Request, mpp.WithExpires(expired), mpp.WithMeta(map[string]string{"_mppx_scope": "GET /other"}))
	params.Authorization = mpptest.FormatCredential(stale.NewCredential(map[string]any{"type": "hash"}))
	_, err := VerifyOrChallenge(context.Background(), params)
	assert.ErrorIs(t, err, mpp.ErrorTypeInvalidChallenge, "a route mismatch is reported before expiry")

	bound := mpptest.NewChallenge(params.SecretKey, params.Realm, params.Method, "charge", params.Request, mpp.WithExpires(expired))
	params.Authorization = mpptest.FormatCredential(bound.NewCredential(map[string]any{"type": "hash"}))
	_, err = VerifyOrChallenge(context.Background(), params)
	assert.ErrorIs(t, err, mpp.ErrorTypePaymentExpired)
}

func TestSpecVerifyOrChallengeRefusesShortSecret(t *testing.T) {
	t.Parallel()

	_, err := VerifyOrChallenge(context.Background(), VerifyParams{Intent: verifyTestIntent{}, Request: map[string]any{"amount": "100"}, Realm: "example.com", SecretKey: strings.Repeat("s", 31), Method: "tempo"})
	assert.ErrorContains(t, err, "at least 32 bytes")
}

func TestSpecCredentialInLaterFieldLineIsVerified(t *testing.T) {
	t.Parallel()

	handler := specHandler(t)
	challenge := specIssue(t, handler)
	recorder := specSend(handler, http.Header{mpp.HeaderAuthorization: {"Bearer app-key", paymentFor(challenge)}})
	assert.Equal(t, http.StatusOK, recorder.Code)
	assert.Equal(t, "ok", recorder.Body.String())
}
