package mpp

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAuthorizationHeaderDoesNotChangeChallengeID(t *testing.T) {
	t.Parallel()

	params := map[string]any{"amount": "1000000"}
	implicit := NewChallenge("test-secret-key-12345", "api.example.com", "tempo", "charge", params)
	explicit := NewChallenge(
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
	assert.NotContains(t, implicit.ToAuthenticate("api.example.com"), "header=")
	assert.Equal(t, HeaderAuthorization, implicit.CredentialHeader())
}

func TestPaymentAuthorizationHeaderIsBoundIntoID(t *testing.T) {
	t.Parallel()

	params := map[string]any{"amount": "1000000"}
	implicit := NewChallenge("test-secret-key-12345", "api.example.com", "tempo", "charge", params)
	advertised := NewChallenge(
		"test-secret-key-12345",
		"api.example.com",
		"tempo",
		"charge",
		params,
		WithHeader(HeaderPaymentAuthorization),
	)

	assert.NotEqual(t, implicit.ID, advertised.ID)
	assert.Equal(t, HeaderPaymentAuthorization, advertised.Header)
	assert.True(t, advertised.Verify("test-secret-key-12345", "api.example.com"))
	assert.Contains(t, advertised.ToAuthenticate("api.example.com"), `header="`+HeaderPaymentAuthorization+`"`)
	assert.Equal(t, HeaderPaymentAuthorization, advertised.CredentialHeader())
}

func TestWWWAuthenticateRoundTripPreservesCredentialHeader(t *testing.T) {
	t.Parallel()

	challenge := NewChallenge(
		"test-secret",
		"api.example.com",
		"tempo",
		"charge",
		map[string]any{"amount": "1000000"},
		WithHeader(HeaderPaymentAuthorization),
	)
	header := challenge.ToAuthenticate("api.example.com")
	parsed, err := ParseChallenge(header)
	require.NoError(t, err)

	assert.Contains(t, header, `header="`+HeaderPaymentAuthorization+`"`)
	assert.Equal(t, HeaderPaymentAuthorization, parsed.Header)
	assert.Equal(t, HeaderPaymentAuthorization, parsed.CredentialHeader())
	assert.True(t, parsed.Verify("test-secret", "api.example.com"))
}
