package chargeserver

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tempoxyz/mpp-go/pkg/mpp"
	"github.com/tempoxyz/mpp-go/pkg/tempo"
)

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

func TestAlternateZeroAmountsCompleteProofFlow(t *testing.T) {
	for _, amount := range []string{"0", "00", "000000", " 0 ", "\t00\n"} {
		t.Run(amount, func(t *testing.T) {
			request := buildRequest(t, true, nil)
			request.Amount = amount
			rpc := newMockRPC(request)
			challenge := buildChallenge(t, request)
			credential, err := newClientMethod(t, rpc, tempo.CredentialTypeTransaction).CreateCredential(context.Background(), challenge)
			require.NoError(t, err)
			require.Equal(t, string(tempo.CredentialTypeProof), credential.Payload["type"])
			require.Equal(t, challenge.ToEcho(), credential.Challenge)
			intent, err := NewIntent(IntentConfig{RPC: rpc, Store: tempo.NewMemoryStore()})
			require.NoError(t, err)
			_, err = intent.Verify(context.Background(), credential, request.Map())
			require.NoError(t, err)
			require.Empty(t, rpc.sentRawTxs)
		})
	}
}
