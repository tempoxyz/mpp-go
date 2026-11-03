package chargeclient

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	temposigner "github.com/tempoxyz/tempo-go/pkg/signer"

	"github.com/tempoxyz/mpp-go/pkg/internal/mpptest"
	"github.com/tempoxyz/mpp-go/pkg/mpp"
	"github.com/tempoxyz/mpp-go/pkg/tempo"
)

const subscriptionClientKey = "0x59c6995e998f97a5a0044966f0945389dc9e86dae88c7a8412f4603b6b78690d"

func subscriptionChallenge(t *testing.T, chainID int64, accessKey *tempo.SubscriptionAccessKey) *mpp.Challenge {
	t.Helper()
	request, err := tempo.NormalizeSubscriptionRequest(tempo.SubscriptionRequestParams{
		Amount:              "5",
		Currency:            tempo.PathUSDAddress,
		Recipient:           "0x70997970c51812dc3a010c7d01b50e0d17dc79c8",
		PeriodCount:         "1",
		PeriodUnit:          tempo.SubscriptionPeriodWeek,
		SubscriptionExpires: time.Now().Add(90 * 24 * time.Hour).Truncate(time.Second),
		ChainID:             chainID,
		AccessKey:           accessKey,
	})
	require.NoError(t, err)
	return mpptest.NewChallenge("subscription-client-test-secret-key-01", "api.example.com", tempo.MethodName, tempo.IntentSubscription, request.Map(), mpp.WithExpires(mpp.ExpiresIn(5*time.Minute)))
}

// acceptSubscription accepts every subscription, for tests only.
func acceptSubscription(context.Context, tempo.SubscriptionRequest) error { return nil }

var subscriptionClientAccessKey = &tempo.SubscriptionAccessKey{AccessKeyAddress: "0x" + strings.Repeat("ab", 20), KeyType: tempo.SubscriptionKeyTypeSecp256k1}

func TestSubscriptionClientSignsScopedKeyAuthorization(t *testing.T) {
	method, err := NewSubscription(SubscriptionConfig{PrivateKey: subscriptionClientKey, ValidateRequest: acceptSubscription})
	require.NoError(t, err)
	assert.Equal(t, tempo.IntentSubscription, method.Intent())
	challenge := subscriptionChallenge(t, 0, subscriptionClientAccessKey)

	credential, err := method.CreateCredential(context.Background(), challenge)
	require.NoError(t, err)
	root, err := temposigner.NewSigner(subscriptionClientKey)
	require.NoError(t, err)
	assert.Equal(t, "did:pkh:eip155:42431:"+strings.ToLower(root.Address().Hex()), credential.Source, "Tempo testnet is the default subscription chain")
	payload, err := tempo.ParseSubscriptionCredentialPayload(credential.Payload)
	require.NoError(t, err)
	request, err := tempo.ParseSubscriptionRequest(challenge.Request)
	require.NoError(t, err)
	verified, err := tempo.VerifySubscriptionKeyAuthorization(subscriptionClientAccessKey, challenge.ID, tempo.ChainIDModerato, payload, request)
	require.NoError(t, err)
	assert.Equal(t, root.Address(), verified.Source)
}

func TestSubscriptionClientValidatesBeforeSigning(t *testing.T) {
	refused := errors.New("subscription expires too late")
	var seen tempo.SubscriptionRequest
	method, err := NewSubscription(SubscriptionConfig{
		PrivateKey: subscriptionClientKey,
		ValidateRequest: func(_ context.Context, request tempo.SubscriptionRequest) error {
			seen = request
			return refused
		},
	})
	require.NoError(t, err)
	_, err = method.CreateCredential(context.Background(), subscriptionChallenge(t, tempo.ChainIDMainnet, subscriptionClientAccessKey))
	assert.ErrorIs(t, err, refused)
	assert.Equal(t, "5000000", seen.Amount)
}

func TestSubscriptionClientRejectsChallenges(t *testing.T) {
	method, err := NewSubscription(SubscriptionConfig{PrivateKey: subscriptionClientKey, ValidateRequest: acceptSubscription})
	require.NoError(t, err)
	_, err = method.CreateCredential(context.Background(), subscriptionChallenge(t, 0, nil))
	assert.ErrorContains(t, err, "no subscription access key")

	charge := subscriptionChallenge(t, 0, subscriptionClientAccessKey)
	charge.Intent = tempo.IntentCharge
	_, err = method.CreateCredential(context.Background(), charge)
	assert.ErrorContains(t, err, "unsupported challenge intent")

	_, err = NewSubscription(SubscriptionConfig{ValidateRequest: acceptSubscription})
	assert.Error(t, err)
}

func TestSubscriptionClientFailsClosedWithoutValidateRequest(t *testing.T) {
	_, err := NewSubscription(SubscriptionConfig{PrivateKey: subscriptionClientKey})
	assert.ErrorIs(t, err, ErrSubscriptionValidateRequired)

	// A zero-value method never signs either.
	var method SubscriptionMethod
	_, err = method.CreateCredential(context.Background(), subscriptionChallenge(t, 0, subscriptionClientAccessKey))
	assert.ErrorIs(t, err, ErrSubscriptionValidateRequired)
}

func TestSubscriptionClientRejectsOtherChain(t *testing.T) {
	validated := false
	method, err := NewSubscription(SubscriptionConfig{
		ChainID:    tempo.ChainIDModerato,
		PrivateKey: subscriptionClientKey,
		ValidateRequest: func(context.Context, tempo.SubscriptionRequest) error {
			validated = true
			return nil
		},
	})
	require.NoError(t, err)
	_, err = method.CreateCredential(context.Background(), subscriptionChallenge(t, tempo.ChainIDMainnet, subscriptionClientAccessKey))
	assert.ErrorContains(t, err, "challenge chain id mismatch (challenge=4217, expected=42431)")
	assert.False(t, validated, "nothing is signed for another chain")

	credential, err := method.CreateCredential(context.Background(), subscriptionChallenge(t, tempo.ChainIDModerato, subscriptionClientAccessKey))
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(credential.Source, "did:pkh:eip155:42431:"))
}

func TestSubscriptionClientUsesConfiguredAccessKey(t *testing.T) {
	override := &tempo.SubscriptionAccessKey{AccessKeyAddress: "0x" + strings.Repeat("cd", 20), KeyType: tempo.SubscriptionKeyTypeSecp256k1}
	method, err := NewSubscription(SubscriptionConfig{PrivateKey: subscriptionClientKey, AccessKey: override, ValidateRequest: acceptSubscription})
	require.NoError(t, err)
	challenge := subscriptionChallenge(t, 0, nil)
	credential, err := method.CreateCredential(context.Background(), challenge)
	require.NoError(t, err)
	payload, err := tempo.ParseSubscriptionCredentialPayload(credential.Payload)
	require.NoError(t, err)
	request, err := tempo.ParseSubscriptionRequest(challenge.Request)
	require.NoError(t, err)
	_, err = tempo.VerifySubscriptionKeyAuthorization(override, challenge.ID, tempo.ChainIDModerato, payload, request)
	assert.NoError(t, err)
}
