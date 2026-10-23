package chargeserver

import (
	"math/big"
	"testing"

	"github.com/stretchr/testify/require"
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

	policies, err := normalizeFeePayerPolicies(map[string]FeePayerPolicy{
		tempotx.AlphaUSDAddress.Hex(): func() FeePayerPolicy {
			policy := defaultFeePayerPolicy()
			policy.RejectKeyAuthorization = true
			return policy
		}(),
	})
	require.NoError(t, err)
	rejecting := policies[tempotx.AlphaUSDAddress.Hex()]
	require.True(t, rejecting.RejectKeyAuthorization)
	require.ErrorContains(t, validateFeePayerTransaction(tx, "", rejecting), "keyAuthorization is not allowed")

	tx.KeyAuthorization = nil
	require.NoError(t, validateFeePayerTransaction(tx, "", rejecting), "transactions without a key authorization are unaffected")
}
