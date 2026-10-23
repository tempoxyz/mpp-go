package chargeserver

import (
	"math/big"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tempoxyz/mpp-go/pkg/tempo"
	tempotx "github.com/tempoxyz/tempo-go/pkg/transaction"
)

func TestFeePayerPolicyKeyAuthorization(t *testing.T) {
	t.Parallel()

	tx := &tempotx.Tx{
		Gas:              100_000,
		MaxFeePerGas:     big.NewInt(1),
		KeyAuthorization: []interface{}{[]byte{1}, []byte{2}, []byte{3}},
	}

	require.NoError(t, validateFeePayerTransaction(tx, "", defaultFeePayerPolicy()), "allowed by default")

	// Resolve the policy the way the sponsored Verify paths do, so the flag
	// must survive IntentConfig normalization and per-currency lookup.
	intent, err := NewIntent(IntentConfig{
		Store: tempo.NewMemoryStore(),
		FeePayerPolicies: map[string]FeePayerPolicy{
			tempotx.AlphaUSDAddress.Hex(): func() FeePayerPolicy {
				policy := defaultFeePayerPolicy()
				policy.RejectKeyAuthorization = true
				return policy
			}(),
		},
	})
	require.NoError(t, err)
	rejecting, err := intent.feePayerPolicyFor(tempotx.AlphaUSDAddress.Hex())
	require.NoError(t, err)
	require.True(t, rejecting.RejectKeyAuthorization)
	require.ErrorContains(t, validateFeePayerTransaction(tx, "", rejecting), "keyAuthorization is not allowed")

	tx.KeyAuthorization = nil
	require.NoError(t, validateFeePayerTransaction(tx, "", rejecting), "transactions without a key authorization are unaffected")
}
