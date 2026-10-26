package server_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tempoxyz/mpp-go/pkg/mpp"
	"github.com/tempoxyz/mpp-go/pkg/server"
)

func writerTestChallenge(t *testing.T, realm, method string) *mpp.Challenge {
	t.Helper()
	challenge, err := mpp.NewChallengeWithError("secret", realm, method, mpp.IntentCharge, map[string]any{"amount": "1"})
	require.NoError(t, err)
	return challenge
}

func problemType(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var problem map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &problem))
	return problem["type"].(string)
}

func TestWritePaymentResponseInitialChallenges(t *testing.T) {
	t.Parallel()

	tempo := writerTestChallenge(t, "api.example.com", "tempo")
	stripe := writerTestChallenge(t, "api.example.com", "stripe")
	rec := httptest.NewRecorder()
	server.WritePaymentResponse(rec, nil, tempo, nil, stripe)

	assert.Equal(t, http.StatusPaymentRequired, rec.Code)
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	values := rec.Header().Values(mpp.HeaderWWWAuthenticate)
	require.Len(t, values, 2)
	for i, want := range []*mpp.Challenge{tempo, stripe} {
		parsed, err := mpp.ParseChallenge(values[i])
		require.NoError(t, err)
		assert.Equal(t, want.ID, parsed.ID)
		assert.True(t, parsed.VerifyID("secret"), "realm comes from the challenge")
	}
}

func TestWritePaymentResponseNoChallengesIsInternalError(t *testing.T) {
	t.Parallel()

	rec := httptest.NewRecorder()
	server.WritePaymentResponse(rec, nil)
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.Equal(t, string(mpp.ErrorTypeInternalPayment), problemType(t, rec))
}

func TestWritePaymentResponseErrorWithRetryChallenges(t *testing.T) {
	t.Parallel()

	rec := httptest.NewRecorder()
	server.WritePaymentResponse(rec, mpp.ErrVerificationFailed("bad signature"), writerTestChallenge(t, "api.example.com", "tempo"))
	assert.Equal(t, http.StatusPaymentRequired, rec.Code)
	assert.Equal(t, "application/problem+json", rec.Header().Get("Content-Type"))
	assert.Len(t, rec.Header().Values(mpp.HeaderWWWAuthenticate), 1)
	assert.Equal(t, string(mpp.ErrorTypeVerificationFailed), problemType(t, rec))
}

func TestWritePaymentResponseOutcomeUnknownOmitsChallenges(t *testing.T) {
	t.Parallel()

	rec := httptest.NewRecorder()
	server.WritePaymentResponse(rec, mpp.ErrPaymentOutcomeUnknown("abc"), writerTestChallenge(t, "api.example.com", "tempo"))
	assert.Equal(t, http.StatusPaymentRequired, rec.Code)
	assert.Empty(t, rec.Header().Values(mpp.HeaderWWWAuthenticate))
	assert.Equal(t, string(mpp.ErrorTypePaymentOutcomeUnknown), problemType(t, rec))
}

func TestWritePaymentResponseHidesUnexpectedErrors(t *testing.T) {
	t.Parallel()

	rec := httptest.NewRecorder()
	server.WritePaymentResponse(rec, errors.New("dial tcp 10.0.0.1: refused"))
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.NotContains(t, rec.Body.String(), "10.0.0.1")
}

func TestWritePaymentResponseSerializationFailure(t *testing.T) {
	t.Parallel()

	broken := *writerTestChallenge(t, "api.example.com", "tempo")
	broken.Realm = ""
	rec := httptest.NewRecorder()
	server.WritePaymentResponse(rec, nil, writerTestChallenge(t, "api.example.com", "tempo"), &broken)
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.Empty(t, rec.Header().Values(mpp.HeaderWWWAuthenticate), "no partial set of offers")

	scoped, err := mpp.NewChallengeWithError("secret", "api.example.com", "tempo", mpp.IntentCharge, map[string]any{
		"amount":      "1",
		"_mppx_scope": map[string]any{"route": "/paid", "resource": "/paid/" + strings.Repeat("a", 16<<10)},
	})
	require.NoError(t, err)
	rec = httptest.NewRecorder()
	server.WritePaymentResponse(rec, nil, scoped)
	assert.Equal(t, http.StatusRequestURITooLong, rec.Code)
}
