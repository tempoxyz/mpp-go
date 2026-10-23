package chargeclient

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/tempoxyz/mpp-go/pkg/mpp"
	"github.com/tempoxyz/mpp-go/pkg/tempo"
	tempotx "github.com/tempoxyz/tempo-go/pkg/transaction"
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
	t.Run("caps at the second after the challenge expiry", func(t *testing.T) {
		expires := format(30 * time.Second)
		got, err := sponsoredValidBefore(expires, now)
		require.NoError(t, err)
		expiry, err := time.Parse(time.RFC3339, expires)
		require.NoError(t, err)
		require.Equal(t, uint64(expiry.Unix()+1), got)
		require.Less(t, got, uint64(now.Unix()+60))
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
	return mpp.NewChallenge("secret-key", testRealm, tempo.MethodName, tempo.IntentCharge, request.Map(), mpp.WithExpires(expires))
}

func TestCreateCredentialSponsoredValidityWindow(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name    string
		expires string
		window  time.Duration
	}{
		{name: "five minute challenge gets the 60 second window", expires: mpp.Expires.Minutes(5), window: tempo.SponsoredValidBeforeWindow},
		{name: "short challenge is capped at its expiry", expires: mpp.Expires.Seconds(30), window: 30 * time.Second},
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

		credential, err := method.CreateCredential(context.Background(), sponsoredChallenge(t, mpp.Expires.Seconds(10)))
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
	challenge := mpp.NewChallenge("secret-key", testRealm, tempo.MethodName, tempo.IntentCharge, request.Map(), mpp.WithExpires(mpp.Expires.Seconds(10)))

	credential, err := method.CreateCredential(context.Background(), challenge)
	require.NoError(t, err)
	signature, _ := credential.Payload["signature"].(string)
	tx, err := tempotx.Deserialize(signature)
	require.NoError(t, err)
	require.Zero(t, tx.ValidBefore)
}
