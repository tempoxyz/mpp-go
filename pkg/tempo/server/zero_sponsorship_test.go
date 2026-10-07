package chargeserver

import (
	"context"
	"github.com/stretchr/testify/require"
	"github.com/tempoxyz/mpp-go/pkg/mpp"
	"github.com/tempoxyz/mpp-go/pkg/tempo"
	"testing"
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
