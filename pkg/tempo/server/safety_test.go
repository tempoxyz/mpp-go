package chargeserver

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/tempoxyz/mpp-go/pkg/mpp"
	"github.com/tempoxyz/mpp-go/pkg/tempo"
)

func TestVerifierRequiresExplicitReplayStore(t *testing.T) {
	request := buildRequest(t, false, nil)
	request.Amount = "0"
	rpc := newMockRPC(request)
	credential, err := newClientMethod(t, rpc, tempo.CredentialTypeProof).CreateCredential(context.Background(), buildChallenge(t, request))
	require.NoError(t, err)
	intent, err := NewIntent(IntentConfig{RPC: rpc})
	require.ErrorContains(t, err, "replay Store is required")
	require.Nil(t, intent)
	method, err := MethodFromConfig(Config{RPC: rpc})
	require.ErrorContains(t, err, "replay Store is required")
	require.Nil(t, method)
	for _, unconfigured := range []*Intent{nil, {}} {
		require.Panics(t, func() { NewMethod(MethodConfig{Intent: unconfigured}) })
		method, err := MethodFromConfig(Config{Intent: unconfigured})
		require.ErrorContains(t, err, "replay Store is required")
		require.Nil(t, method)
	}
	require.Empty(t, rpc.sentRawTxs)

	store := tempo.NewMemoryStore()
	first, err := NewIntent(IntentConfig{RPC: rpc, Store: store})
	require.NoError(t, err)
	second, err := NewIntent(IntentConfig{RPC: rpc, Store: store})
	require.NoError(t, err)
	_, err = first.Verify(context.Background(), credential, request.Map())
	require.NoError(t, err)
	_, err = second.Verify(context.Background(), credential, request.Map())
	require.Error(t, err, "another verifier must not accept the same proof")
}

func TestZeroAmountSponsoredCredentialsRequireProof(t *testing.T) {
	for _, amount := range []string{"0", "00", "000000", " 0 ", "\t00\n"} {
		for _, kind := range []tempo.CredentialType{tempo.CredentialTypeTransaction, tempo.CredentialTypeHash} {
			t.Run(amount+"/"+string(kind), func(t *testing.T) {
				request := buildRequest(t, true, nil)
				request.Amount = amount
				rpc := newMockRPC(request)
				intent, err := NewIntent(IntentConfig{RPC: rpc, Store: tempo.NewMemoryStore(), FeePayerPrivateKey: feePayerKey})
				require.NoError(t, err)
				credential := &mpp.Credential{Payload: tempo.ChargeCredentialPayload{Type: kind, Signature: "0x01", Hash: "0x01"}.Map()}
				_, err = intent.Verify(context.Background(), credential, request.Map())
				require.ErrorContains(t, err, "zero-amount fee payer challenges require a proof")
				require.Empty(t, rpc.sentRawTxs)
				require.Empty(t, rpc.callRequests)
			})
		}
	}

}

func TestSponsoredValidityDeadline(t *testing.T) {
	now := time.Unix(2_000_000_000, 0)
	for _, tc := range []struct {
		name    string
		seconds int64
		ok      bool
	}{
		{"expired", -1, false}, {"now", 0, false}, {"too short", 14, false},
		{"minimum", 15, true}, {"client default", 25, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateFeePayerDeadline(uint64(now.Unix()+tc.seconds), now)
			if tc.ok {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
	require.Error(t, validateFeePayerDeadline(0, now))
}

func testStoredIntent() *Intent {
	intent, err := NewIntent(IntentConfig{Store: tempo.NewMemoryStore()})
	if err != nil {
		panic(err)
	}
	return intent
}
