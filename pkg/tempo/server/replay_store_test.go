package chargeserver

import (
	"context"
	"github.com/stretchr/testify/require"
	"github.com/tempoxyz/mpp-go/pkg/tempo"
	"testing"
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

func TestTypedNilReplayStoresRejectedBeforeChallenge(t *testing.T) {
	for _, store := range []tempo.Store{nil, (*tempo.MemoryStore)(nil), (*tempo.RedisStore)(nil)} {
		intent, err := NewIntent(IntentConfig{Store: store})
		require.ErrorContains(t, err, "replay Store is required")
		require.Nil(t, intent)
		for _, config := range []Config{{Store: store}, {Intent: &Intent{store: store}}} {
			method, err := MethodFromConfig(config)
			require.ErrorContains(t, err, "replay Store is required")
			require.Nil(t, method)
		}
		require.Panics(t, func() { NewMethod(MethodConfig{Intent: &Intent{store: store}}) })
	}
}

func testStoredIntent() *Intent {
	intent, err := NewIntent(IntentConfig{Store: tempo.NewMemoryStore()})
	if err != nil {
		panic(err)
	}
	return intent
}
