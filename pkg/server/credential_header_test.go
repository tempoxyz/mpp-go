package server

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tempoxyz/mpp-go/pkg/internal/mpptest"
	"github.com/tempoxyz/mpp-go/pkg/mpp"
)

// A challenge whose ID binds a lowercase payment-authorization (as another
// server sharing the secret may issue) verifies when the client echoes that
// spelling unchanged.
func TestVerifyOrChallengeAcceptsLowercaseBoundCredentialHeader(t *testing.T) {
	request := map[string]any{"amount": "100", "currency": "0xabc", "recipient": "0xdef"}
	expires := mpp.ExpiresIn(5 * time.Minute)
	id, err := mpp.GenerateChallengeIDWithError(mpp.GenerateChallengeIDInput{
		SecretKey: "verify-test-secret-key-of-at-least-32-bytes",
		Realm:     "api.example.com",
		Method:    "tempo",
		Intent:    "charge",
		Request:   request,
		Expires:   expires,
		Header:    "payment-authorization",
	})
	require.NoError(t, err)
	formatted, err := mpp.FormatChallenge(&mpp.Challenge{
		ID: id, Realm: "api.example.com", Method: "tempo", Intent: "charge",
		Request: request, Expires: expires, Header: "payment-authorization",
	})
	require.NoError(t, err)

	challenge, err := mpp.ParseChallenge(formatted)
	require.NoError(t, err)
	credential := challenge.NewCredential(map[string]any{"type": "hash", "hash": "0xabc123"})
	assert.Equal(t, "payment-authorization", credential.Challenge.Header)

	result, err := VerifyOrChallenge(context.Background(), VerifyParams{
		Authorization: mpptest.FormatCredential(credential),
		Intent:        verifyTestIntent{},
		Request:       request,
		Realm:         "api.example.com",
		SecretKey:     "verify-test-secret-key-of-at-least-32-bytes",
		Method:        "tempo",
		Header:        mpp.HeaderPaymentAuthorization,
		Expires:       expires,
	})
	require.NoError(t, err)
	require.NotNil(t, result.Receipt)
	assert.Equal(t, "0xreceipt", result.Receipt.Reference)

	// A tampered spelling no longer matches the bound ID.
	credential.Challenge.Header = mpp.HeaderPaymentAuthorization
	result, err = VerifyOrChallenge(context.Background(), VerifyParams{
		Authorization: mpptest.FormatCredential(credential),
		Intent:        verifyTestIntent{},
		Request:       request,
		Realm:         "api.example.com",
		SecretKey:     "verify-test-secret-key-of-at-least-32-bytes",
		Method:        "tempo",
		Header:        mpp.HeaderPaymentAuthorization,
		Expires:       expires,
	})
	assert.True(t, err != nil || result == nil || result.Receipt == nil, "a different echoed spelling must not verify")
}
