package chargeclient

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	temporpc "github.com/tempoxyz/tempo-go/pkg/client"
	"github.com/tempoxyz/tempo-go/pkg/keychain"
	temposigner "github.com/tempoxyz/tempo-go/pkg/signer"

	"github.com/tempoxyz/mpp-go/pkg/mpp"
	"github.com/tempoxyz/mpp-go/pkg/tempo"
)

// slowKeychainRPC answers AccountKeychain.getKey with an unauthorized key
// after running onKeychain, so a test can make preparation slow between
// computing valid_before and signing.
type slowKeychainRPC struct {
	*mockRPC
	onKeychain func()
}

func (r *slowKeychainRPC) SendRequest(ctx context.Context, method string, params ...interface{}) (*temporpc.JSONRPCResponse, error) {
	if method == "eth_call" {
		r.onKeychain()
		// KeyInfo with a zero keyId: the key is not authorized yet.
		return &temporpc.JSONRPCResponse{Result: "0x" + strings.Repeat("0", 64*5)}, nil
	}
	return r.mockRPC.SendRequest(ctx, method, params...)
}

func slowAccessKeyMethod(t *testing.T, rpc tempo.RPCClient, clock *fakeClock) *Method {
	t.Helper()
	root, err := temposigner.NewSigner("0x59c6995e998f97a5a0044966f0945389dc9e86dae88c7a8412f4603b6b78690d")
	require.NoError(t, err)
	key, err := temposigner.NewSigner("0x5de4111afa1a4b94908f83103eb1f1706367c2e68ca870fc3fb9a804cdab365a")
	require.NoError(t, err)
	authorization, err := keychain.NewKeyAuthorization(42431, 0, key.Address()).Sign(root)
	require.NoError(t, err)
	method, err := New(Config{
		AccessKey:     &AccessKey{Signer: key, Account: root.Address(), KeyAuthorization: authorization},
		RPC:           rpc,
		ChainID:       42431,
		PaymentPolicy: func(context.Context, tempo.ChargeRequest) error { return nil },
	})
	require.NoError(t, err)
	method.now = clock.Now
	return method
}

func TestCreateCredentialRechecksSponsoredValidityBeforeSigning(t *testing.T) {
	t.Parallel()

	t.Run("refuses once valid_before has under 15s left after slow preparation", func(t *testing.T) {
		t.Parallel()
		clock := &fakeClock{now: time.Now()}
		rpc := &slowKeychainRPC{
			mockRPC: &mockRPC{chainID: 42431, gasPrice: "0x1", estimateGas: "0x5208"},
			// valid_before is now + 60s; 50s of preparation leaves 10s.
			onKeychain: func() { clock.Advance(50 * time.Second) },
		}
		method := slowAccessKeyMethod(t, rpc, clock)

		credential, err := method.CreateCredential(context.Background(), sponsoredChallenge(t, mpp.ExpiresIn(5*time.Minute)))
		require.ErrorIs(t, err, ErrSponsoredPreparationTooSlow)
		require.NotErrorIs(t, err, ErrSponsoredChallengeExpiresTooSoon)
		require.Nil(t, credential)
		require.Empty(t, rpc.sentRawTxs)
	})

	t.Run("refuses once the challenge expires within 15s after slow preparation", func(t *testing.T) {
		t.Parallel()
		clock := &fakeClock{now: time.Now()}
		rpc := &slowKeychainRPC{
			mockRPC:    &mockRPC{chainID: 42431, gasPrice: "0x1", estimateGas: "0x5208"},
			onKeychain: func() { clock.Advance(10 * time.Second) },
		}
		method := slowAccessKeyMethod(t, rpc, clock)
		expires := clock.Now().Add(20 * time.Second).UTC().Format("2006-01-02T15:04:05.000Z")

		credential, err := method.CreateCredential(context.Background(), sponsoredChallenge(t, expires))
		require.ErrorIs(t, err, ErrSponsoredChallengeExpiresTooSoon)
		require.Nil(t, credential)
		require.Empty(t, rpc.sentRawTxs)
	})

	t.Run("signs when enough validity remains", func(t *testing.T) {
		t.Parallel()
		clock := &fakeClock{now: time.Now()}
		rpc := &slowKeychainRPC{
			mockRPC:    &mockRPC{chainID: 42431, gasPrice: "0x1", estimateGas: "0x5208"},
			onKeychain: func() { clock.Advance(5 * time.Second) },
		}
		method := slowAccessKeyMethod(t, rpc, clock)

		credential, err := method.CreateCredential(context.Background(), sponsoredChallenge(t, mpp.ExpiresIn(5*time.Minute)))
		require.NoError(t, err)
		signature, _ := credential.Payload["signature"].(string)
		tx, err := deserializeTestTx(signature)
		require.NoError(t, err)
		require.True(t, tx.AwaitingFeePayer)
		require.NotNil(t, tx.KeyAuthorization)
	})
}

func TestCheckSponsoredSignable(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_800_000_000, 0).UTC()
	format := func(d time.Duration) string { return now.Add(d).Format("2006-01-02T15:04:05.000Z") }
	validBefore := func(d time.Duration) uint64 { return uint64(now.Add(d).Unix()) }

	require.NoError(t, checkSponsoredSignable(format(time.Minute), validBefore(15*time.Second), now))
	require.NoError(t, checkSponsoredSignable("", validBefore(time.Minute), now))
	require.ErrorIs(t, checkSponsoredSignable(format(14*time.Second), validBefore(time.Minute), now), ErrSponsoredChallengeExpiresTooSoon)
	require.ErrorIs(t, checkSponsoredSignable(format(time.Minute), validBefore(14*time.Second), now), ErrSponsoredPreparationTooSlow)
	require.ErrorIs(t, checkSponsoredSignable("", validBefore(14*time.Second), now), ErrSponsoredPreparationTooSlow)
	require.ErrorContains(t, checkSponsoredSignable("tomorrow", validBefore(time.Minute), now), "invalid challenge expires")
}
