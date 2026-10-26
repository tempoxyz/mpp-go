package mpp

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAuthorizationHeaderDoesNotChangeChallengeID(t *testing.T) {
	t.Parallel()

	params := map[string]any{"amount": "1000000"}
	implicit := newChallenge("test-secret-key-12345", "api.example.com", "tempo", "charge", params)
	explicit := newChallenge(
		"test-secret-key-12345",
		"api.example.com",
		"tempo",
		"charge",
		params,
		WithHeader(HeaderAuthorization),
	)

	assert.Empty(t, implicit.Header)
	assert.Empty(t, explicit.Header)
	assert.Equal(t, implicit.ID, explicit.ID)
	assert.NotContains(t, implicit.toAuthenticate("api.example.com"), "header=")
	assert.Equal(t, HeaderAuthorization, implicit.CredentialHeader())
}

func TestPaymentAuthorizationHeaderIsBoundIntoID(t *testing.T) {
	t.Parallel()

	params := map[string]any{"amount": "1000000"}
	implicit := newChallenge("test-secret-key-12345", "api.example.com", "tempo", "charge", params)
	advertised := newChallenge(
		"test-secret-key-12345",
		"api.example.com",
		"tempo",
		"charge",
		params,
		WithHeader(HeaderPaymentAuthorization),
	)

	assert.NotEqual(t, implicit.ID, advertised.ID)
	assert.Equal(t, HeaderPaymentAuthorization, advertised.Header)
	assert.True(t, advertised.verify("test-secret-key-12345", "api.example.com"))
	assert.Contains(t, advertised.toAuthenticate("api.example.com"), `header="`+HeaderPaymentAuthorization+`"`)
	assert.Equal(t, HeaderPaymentAuthorization, advertised.CredentialHeader())
}

func TestWWWAuthenticateRoundTripPreservesCredentialHeader(t *testing.T) {
	t.Parallel()

	challenge := newChallenge(
		"test-secret",
		"api.example.com",
		"tempo",
		"charge",
		map[string]any{"amount": "1000000"},
		WithHeader(HeaderPaymentAuthorization),
	)
	header := challenge.toAuthenticate("api.example.com")
	parsed, err := ParseChallenge(header)
	require.NoError(t, err)

	assert.Contains(t, header, `header="`+HeaderPaymentAuthorization+`"`)
	assert.Equal(t, HeaderPaymentAuthorization, parsed.Header)
	assert.Equal(t, HeaderPaymentAuthorization, parsed.CredentialHeader())
	assert.True(t, parsed.verify("test-secret", "api.example.com"))
}

func TestChallengeCreationRejectsInvalidCredentialHeader(t *testing.T) {
	t.Parallel()

	_, err := NewChallengeWithError(
		"test-secret",
		"api.example.com",
		"tempo",
		"charge",
		map[string]any{"amount": "1000000"},
		WithHeader("Payment Authorization"),
	)
	require.EqualError(t, err, "mpp: invalid HTTP header name")
}

func TestChallengeIDRejectsInvalidCredentialHeader(t *testing.T) {
	t.Parallel()

	_, err := GenerateChallengeIDWithError(GenerateChallengeIDInput{
		SecretKey: "test-secret",
		Realm:     "api.example.com",
		Method:    "tempo",
		Intent:    "charge",
		Request:   map[string]any{"amount": "1000000"},
		Header:    "Payment Authorization",
	})
	require.EqualError(t, err, "mpp: invalid HTTP header name")
}

func TestFormatAuthenticateStrictRejectsInvalidCredentialHeader(t *testing.T) {
	t.Parallel()

	challenge := &Challenge{
		ID:      "challenge-id",
		Realm:   "api.example.com",
		Method:  "tempo",
		Intent:  "charge",
		Request: map[string]any{"amount": "1000000"},
		Header:  "Payment Authorization",
	}
	_, err := challenge.toAuthenticateStrict(challenge.Realm)
	require.EqualError(t, err, "mpp: invalid HTTP header name")
}
