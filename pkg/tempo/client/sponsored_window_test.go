package chargeclient

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tempoxyz/mpp-go/pkg/internal/mpptest"

	"github.com/stretchr/testify/require"
	tempotx "github.com/tempoxyz/tempo-go/pkg/transaction"

	"github.com/tempoxyz/mpp-go/pkg/mpp"
	"github.com/tempoxyz/mpp-go/pkg/tempo"
)

func TestSponsoredValidBefore(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_800_000_000, 250_000_000).UTC()
	format := func(d time.Duration) string { return now.Add(d).Format("2006-01-02T15:04:05.000Z") }

	t.Run("uses the 60 second window", func(t *testing.T) {
		got, err := sponsoredValidBefore(format(5*time.Minute), now)
		require.NoError(t, err)
		require.Equal(t, uint64(now.Unix()+60), got)
	})
	t.Run("without expires uses the window", func(t *testing.T) {
		got, err := sponsoredValidBefore("", now)
		require.NoError(t, err)
		require.Equal(t, uint64(now.Unix()+60), got)
	})
	t.Run("caps at 15 seconds past the second after the challenge expiry", func(t *testing.T) {
		expires := format(30 * time.Second)
		got, err := sponsoredValidBefore(expires, now)
		require.NoError(t, err)
		expiry, err := time.Parse(time.RFC3339, expires)
		require.NoError(t, err)
		require.Equal(t, uint64(expiry.Unix()+1+15), got)
		require.Less(t, got, uint64(now.Unix()+60))
		// Servers accept valid_before up to a minute past the challenge expiry.
		require.LessOrEqual(t, got, uint64(expiry.Add(time.Minute).Unix()))
	})
	t.Run("short challenge keeps 15 seconds of validity until it expires", func(t *testing.T) {
		expires := format(16 * time.Second)
		got, err := sponsoredValidBefore(expires, now)
		require.NoError(t, err)
		// A credential arriving just before the challenge expires still has
		// the server's 15 second minimum left (validateFeePayerDeadline).
		arrival := now.Add(16*time.Second - time.Millisecond)
		require.GreaterOrEqual(t, got, uint64(arrival.Add(15*time.Second+time.Second-1).Unix()))
	})
	t.Run("accepts exactly 15 seconds of validity", func(t *testing.T) {
		_, err := sponsoredValidBefore(format(15*time.Second), now)
		require.NoError(t, err)
	})
	for _, remaining := range []time.Duration{15*time.Second - time.Millisecond, 10 * time.Second, -time.Second} {
		t.Run("refuses "+remaining.String()+" of validity", func(t *testing.T) {
			_, err := sponsoredValidBefore(format(remaining), now)
			require.ErrorIs(t, err, ErrSponsoredChallengeExpiresTooSoon)
		})
	}
	t.Run("rejects unparseable expires", func(t *testing.T) {
		_, err := sponsoredValidBefore("tomorrow", now)
		require.ErrorContains(t, err, "invalid challenge expires")
	})
}

func sponsoredChallenge(t *testing.T, expires string) *mpp.Challenge {
	t.Helper()
	request, err := tempo.NormalizeChargeRequest(tempo.ChargeRequestParams{
		Amount: "0.50", Currency: testCurrency, Recipient: testRecipient, Decimals: 6, ChainID: 42431, FeePayer: true,
	})
	require.NoError(t, err)
	return mpptest.NewChallenge("secret-key", testRealm, tempo.MethodName, tempo.IntentCharge, request.Map(), mpp.WithExpires(expires))
}

func TestCreateCredentialSponsoredValidityWindow(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name    string
		expires string
		window  time.Duration
	}{
		{name: "five minute challenge gets the 60 second window", expires: mpp.ExpiresIn(5 * time.Minute), window: tempo.SponsoredValidBeforeWindow},
		{name: "short challenge is capped 15s past its expiry", expires: mpp.ExpiresIn(30 * time.Second), window: 30*time.Second + time.Second + tempo.FeePayerMinRemainingValidity},
	} {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			rpc := &mockRPC{chainID: 42431, gasPrice: "0x1", estimateGas: "0x5208"}
			method, err := New(Config{
				PrivateKey: testPrivateKey, RPC: rpc, ChainID: 42431,
				PaymentPolicy: func(context.Context, tempo.ChargeRequest) error { return nil },
			})
			require.NoError(t, err)

			before := time.Now()
			credential, err := method.CreateCredential(context.Background(), sponsoredChallenge(t, tt.expires))
			after := time.Now()
			require.NoError(t, err)
			require.Empty(t, rpc.sentRawTxs)

			signature, _ := credential.Payload["signature"].(string)
			tx, err := tempotx.Deserialize(signature)
			require.NoError(t, err)
			require.True(t, tx.AwaitingFeePayer)
			require.GreaterOrEqual(t, tx.ValidBefore, uint64(before.Add(tt.window).Unix()))
			require.LessOrEqual(t, tx.ValidBefore, uint64(after.Add(tt.window).Unix())+1)
		})
	}
}

func TestCreateCredentialRefusesSponsoredChallengeExpiringWithin15Seconds(t *testing.T) {
	t.Parallel()

	for _, credentialType := range []tempo.CredentialType{"", tempo.CredentialTypeTransaction} {
		rpc := &mockRPC{chainID: 42431, gasPrice: "0x1", estimateGas: "0x5208"}
		policyCalled := false
		method, err := New(Config{
			PrivateKey: testPrivateKey, RPC: rpc, ChainID: 42431, CredentialType: credentialType,
			PaymentPolicy: func(context.Context, tempo.ChargeRequest) error {
				policyCalled = true
				return nil
			},
		})
		require.NoError(t, err)

		credential, err := method.CreateCredential(context.Background(), sponsoredChallenge(t, mpp.ExpiresIn(10*time.Second)))
		require.ErrorIs(t, err, ErrSponsoredChallengeExpiresTooSoon)
		require.True(t, strings.Contains(err.Error(), "need at least 15s"), err.Error())
		require.Nil(t, credential)
		require.False(t, policyCalled, "refusal must happen before authorizing or signing")
		require.Empty(t, rpc.sentRawTxs)
	}
}

func TestCreateCredentialUnsponsoredShortChallengeStillSigns(t *testing.T) {
	t.Parallel()

	rpc := &mockRPC{chainID: 42431, gasPrice: "0x1", estimateGas: "0x5208"}
	method, err := New(Config{
		PrivateKey: testPrivateKey, RPC: rpc, ChainID: 42431,
		PaymentPolicy: func(context.Context, tempo.ChargeRequest) error { return nil },
	})
	require.NoError(t, err)
	request, err := tempo.NormalizeChargeRequest(tempo.ChargeRequestParams{
		Amount: "0.50", Currency: testCurrency, Recipient: testRecipient, Decimals: 6, ChainID: 42431,
	})
	require.NoError(t, err)
	challenge := mpptest.NewChallenge("secret-key", testRealm, tempo.MethodName, tempo.IntentCharge, request.Map(), mpp.WithExpires(mpp.ExpiresIn(10*time.Second)))

	credential, err := method.CreateCredential(context.Background(), challenge)
	require.NoError(t, err)
	signature, _ := credential.Payload["signature"].(string)
	tx, err := tempotx.Deserialize(signature)
	require.NoError(t, err)
	require.Zero(t, tx.ValidBefore)
}

// fakeClock is a manually advanced clock for sponsored validity tests.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func TestCreateCredentialComputesSponsoredValidBeforeAfterPaymentPolicy(t *testing.T) {
	t.Parallel()

	clock := &fakeClock{now: time.Now()}
	rpc := &mockRPC{chainID: 42431, gasPrice: "0x1", estimateGas: "0x5208"}
	method, err := New(Config{
		PrivateKey: testPrivateKey, RPC: rpc, ChainID: 42431,
		PaymentPolicy: func(context.Context, tempo.ChargeRequest) error {
			// A slow policy, such as one prompting the user.
			clock.Advance(50 * time.Second)
			return nil
		},
	})
	require.NoError(t, err)
	method.now = clock.Now

	credential, err := method.CreateCredential(context.Background(), sponsoredChallenge(t, mpp.ExpiresIn(5*time.Minute)))
	require.NoError(t, err)
	signature, _ := credential.Payload["signature"].(string)
	tx, err := tempotx.Deserialize(signature)
	require.NoError(t, err)
	require.Equal(t, uint64(clock.Now().Add(tempo.SponsoredValidBeforeWindow).Unix()), tx.ValidBefore)
}

func TestCreateCredentialRechecksSponsoredFloorBeforeSigning(t *testing.T) {
	t.Parallel()

	clock := &fakeClock{now: time.Now()}
	rpc := &mockRPC{chainID: 42431, gasPrice: "0x1", estimateGas: "0x5208"}
	method, err := New(Config{
		PrivateKey: testPrivateKey, RPC: rpc, ChainID: 42431,
		PaymentPolicy: func(context.Context, tempo.ChargeRequest) error {
			clock.Advance(20 * time.Second)
			return nil
		},
	})
	require.NoError(t, err)
	method.now = clock.Now

	credential, err := method.CreateCredential(context.Background(), sponsoredChallenge(t, mpp.ExpiresIn(30*time.Second)))
	require.ErrorIs(t, err, ErrSponsoredChallengeExpiresTooSoon)
	require.Nil(t, credential)
	require.Empty(t, rpc.sentRawTxs)
}

func TestCanHandleChallengeSkipsSponsoredChallengeExpiringTooSoon(t *testing.T) {
	t.Parallel()

	method, err := New(Config{PrivateKey: testPrivateKey, ChainID: 42431})
	require.NoError(t, err)
	challenge := func(amount string, feePayer bool, expires string) *mpp.Challenge {
		request, err := tempo.NormalizeChargeRequest(tempo.ChargeRequestParams{
			Amount: amount, Currency: testCurrency, Recipient: testRecipient, Decimals: 6, ChainID: 42431, FeePayer: feePayer,
		})
		require.NoError(t, err)
		return mpptest.NewChallenge("secret-key", testRealm, tempo.MethodName, tempo.IntentCharge, request.Map(), mpp.WithExpires(expires))
	}

	require.False(t, method.CanHandleChallenge(challenge("0.50", true, mpp.ExpiresIn(10*time.Second))))
	require.True(t, method.CanHandleChallenge(challenge("0.50", true, mpp.ExpiresIn(5*time.Minute))))
	require.True(t, method.CanHandleChallenge(challenge("0.50", false, mpp.ExpiresIn(10*time.Second))))
	require.True(t, method.CanHandleChallenge(challenge("0", true, mpp.ExpiresIn(10*time.Second))))
}
