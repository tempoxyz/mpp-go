package chargeclient

import (
	"context"
	"errors"
	"math/big"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tempoxyz/mpp-go/pkg/tempo"
)

func policyRequest(amount, currency string, chainID int64) tempo.ChargeRequest {
	return tempo.ChargeRequest{Amount: amount, Currency: currency, MethodDetails: tempo.MethodDetails{ChainID: &chainID}}
}

func TestMaxAmount(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	policy := MaxAmount(tempo.OUSDAddress, "0.5", 6)
	require.NoError(t, policy(ctx, policyRequest("500000", tempo.OUSDAddress, tempo.ChainIDMainnet)))
	require.NoError(t, policy(ctx, policyRequest("1", "0x20C0000000000000000000006A37DA5C996874BE", tempo.ChainIDMainnet)))
	require.ErrorIs(t, policy(ctx, policyRequest("500001", tempo.OUSDAddress, tempo.ChainIDMainnet)), ErrPaymentPolicy)
	require.ErrorIs(t, policy(ctx, policyRequest("1", tempo.USDCEAddress, tempo.ChainIDMainnet)), ErrPaymentPolicy)

	anyCurrency := MaxAmount("", "2", 6)
	require.NoError(t, anyCurrency(ctx, policyRequest("2000000", tempo.USDCEAddress, tempo.ChainIDMainnet)))
	require.Error(t, anyCurrency(ctx, policyRequest("2000001", tempo.OUSDAddress, tempo.ChainIDMainnet)))

	for _, invalid := range []string{"", ".", "-1", "1.0000001", "1e3", "abc"} {
		require.ErrorIsf(t, MaxAmount("", invalid, 6)(ctx, policyRequest("0", tempo.OUSDAddress, 0)), ErrPaymentPolicy, "max %q", invalid)
	}
	require.Error(t, MaxAmount("", "1", -1)(ctx, policyRequest("0", tempo.OUSDAddress, 0)))
	require.NoError(t, MaxAmount("", "1.500000", 6)(ctx, policyRequest("1500000", tempo.OUSDAddress, 0)))
}

func TestMaxAtomicAmount(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	limit := big.NewInt(100)
	policy := MaxAtomicAmount("", limit)
	limit.SetInt64(1) // the policy keeps its own copy
	require.NoError(t, policy(ctx, policyRequest("100", tempo.OUSDAddress, 0)))
	require.Error(t, policy(ctx, policyRequest("101", tempo.OUSDAddress, 0)))
	require.Error(t, policy(ctx, policyRequest("not a number", tempo.OUSDAddress, 0)))
	require.Error(t, MaxAtomicAmount("", nil)(ctx, policyRequest("0", tempo.OUSDAddress, 0)))
}

func TestAllowChainIDsAndAllPolicies(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	chains := AllowChainIDs(tempo.ChainIDModerato)
	require.NoError(t, chains(ctx, policyRequest("1", tempo.OUSDAddress, tempo.ChainIDModerato)))
	require.ErrorIs(t, chains(ctx, policyRequest("1", tempo.OUSDAddress, tempo.ChainIDMainnet)), ErrPaymentPolicy)
	require.Error(t, chains(ctx, tempo.ChargeRequest{Amount: "1"}))

	custom := errors.New("custom")
	all := AllPolicies(chains, nil, MaxAmount("", "1", 6), func(context.Context, tempo.ChargeRequest) error { return custom })
	require.ErrorIs(t, all(ctx, policyRequest("1", tempo.OUSDAddress, tempo.ChainIDMainnet)), ErrPaymentPolicy)
	require.ErrorIs(t, all(ctx, policyRequest("2000000", tempo.OUSDAddress, tempo.ChainIDModerato)), ErrPaymentPolicy)
	require.ErrorIs(t, all(ctx, policyRequest("1", tempo.OUSDAddress, tempo.ChainIDModerato)), custom)
	assert.NoError(t, AllPolicies()(ctx, tempo.ChargeRequest{}))
}
