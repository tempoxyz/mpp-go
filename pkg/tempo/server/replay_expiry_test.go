package chargeserver

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tempoxyz/mpp-go/pkg/mpp"
	"github.com/tempoxyz/mpp-go/pkg/tempo"
)

// expiringRecordingStore records the expiry the verifier asks for.
type expiringRecordingStore struct {
	*tempo.MemoryStore
	expiries map[string]time.Time
}

func (s *expiringRecordingStore) PutIfAbsentUntil(ctx context.Context, key, value string, expiresAt time.Time) (bool, error) {
	s.expiries[key] = expiresAt
	return s.MemoryStore.PutIfAbsentUntil(ctx, key, value, expiresAt)
}

func TestReplayClaimsExpireAfterChallengeWithExpiringStore(t *testing.T) {
	for _, feePayer := range []bool{false, true} {
		t.Run(map[bool]string{false: "transaction", true: "sponsored"}[feePayer], func(t *testing.T) {
			ctx := context.Background()
			request := buildRequest(t, feePayer, nil)
			rpc := newMockRPC(request)
			credential, err := newClientMethod(t, rpc, tempo.CredentialTypeTransaction).CreateCredential(ctx, buildChallenge(t, request))
			require.NoError(t, err)
			store := &expiringRecordingStore{MemoryStore: tempo.NewMemoryStore(), expiries: map[string]time.Time{}}
			config := IntentConfig{Store: store, RPC: rpc, ExpireReplayKeys: true}
			if feePayer {
				config.FeePayerPrivateKey = feePayerKey
			}
			intent, err := NewIntent(config)
			require.NoError(t, err)

			receipt, err := intent.Verify(ctx, credential, request.Map())
			require.NoError(t, err)

			challengeExpires, err := parseExpires(credential.Challenge.Expires)
			require.NoError(t, err)
			want := challengeExpires.Add(replayClaimExpiryGrace)
			hashKey := tempo.ChargeStoreKey(receipt.Reference)
			require.Contains(t, store.expiries, hashKey)
			if feePayer {
				assert.Len(t, store.expiries, 2, "sponsored transaction and hash claims")
			} else {
				assert.Len(t, store.expiries, 1)
			}
			for key, expiresAt := range store.expiries {
				assert.Truef(t, expiresAt.Equal(want), "%s expires at %s, want %s", key, expiresAt, want)
			}

			_, err = intent.Verify(ctx, credential, request.Map())
			assert.ErrorContains(t, err, "already used")
		})
	}
}

func TestReplayClaimsPersistByDefaultWithExpiringStore(t *testing.T) {
	ctx := context.Background()
	request := buildRequest(t, false, nil)
	rpc := newMockRPC(request)
	credential, err := newClientMethod(t, rpc, tempo.CredentialTypeTransaction).CreateCredential(ctx, buildChallenge(t, request))
	require.NoError(t, err)
	store := &expiringRecordingStore{MemoryStore: tempo.NewMemoryStore(), expiries: map[string]time.Time{}}
	method, err := MethodFromConfig(Config{Store: store, RPC: rpc})
	require.NoError(t, err)

	receipt, err := method.intent.Verify(ctx, credential, request.Map())
	require.NoError(t, err)

	// Without ExpireReplayKeys the verifier never asks for an expiry, even
	// though the store supports one: Verify called directly trusts the
	// credential's echoed expiry, so its keys must outlive any expiry.
	assert.Empty(t, store.expiries)
	_, exists, err := store.Get(ctx, tempo.ChargeStoreKey(receipt.Reference))
	require.NoError(t, err)
	assert.True(t, exists)
}

func TestExpireReplayKeysRequiresExpiringStore(t *testing.T) {
	_, err := NewIntent(IntentConfig{Store: newRecordingStore(), ExpireReplayKeys: true})
	require.ErrorContains(t, err, "ExpireReplayKeys requires a Store that implements tempo.ExpiringStore")
	_, err = MethodFromConfig(Config{Store: newRecordingStore(), Recipient: testRecipient, Currency: testCurrency, ExpireReplayKeys: true})
	require.ErrorContains(t, err, "ExpireReplayKeys requires")

	intent, err := NewIntent(IntentConfig{Store: tempo.NewMemoryStore(), ExpireReplayKeys: true})
	require.NoError(t, err)
	assert.True(t, intent.expireReplayKeys)
}

func TestReplayClaimExpiryRequiresChallengeExpiry(t *testing.T) {
	_, ok := replayClaimExpiry(nil)
	assert.False(t, ok)
	credential := &mpp.Credential{}
	_, ok = replayClaimExpiry(credential)
	assert.False(t, ok)
	credential.Challenge.Expires = "not a time"
	_, ok = replayClaimExpiry(credential)
	assert.False(t, ok)
	credential.Challenge.Expires = "2030-01-01T00:00:00Z"
	expiresAt, ok := replayClaimExpiry(credential)
	require.True(t, ok)
	assert.Equal(t, time.Date(2030, 1, 1, 0, 10, 0, 0, time.UTC), expiresAt.UTC())
}
