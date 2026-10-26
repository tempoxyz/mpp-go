package chargeserver

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	tempotx "github.com/tempoxyz/tempo-go/pkg/transaction"

	"github.com/tempoxyz/mpp-go/pkg/tempo"
)

func TestNewMatchesMethodFromConfig(t *testing.T) {
	t.Parallel()
	method, err := New(Config{Store: tempo.NewMemoryStore(), Recipient: testRecipient, ChainID: tempo.ChainIDModerato})
	require.NoError(t, err)
	assert.Equal(t, tempo.ChainIDModerato, method.chainID)
	assert.Equal(t, tempo.DefaultCurrenciesForChain(tempo.ChainIDModerato), method.currencies)

	_, err = New(Config{Recipient: testRecipient})
	require.ErrorContains(t, err, "replay Store is required")
	_, err = New(Config{Store: tempo.NewMemoryStore(), Currencies: []string{"not-an-address"}})
	require.ErrorContains(t, err, "invalid currency address")
}

func TestChainIDConstantsMatchTempoGo(t *testing.T) {
	t.Parallel()
	assert.EqualValues(t, tempotx.ChainIdMainnet, tempo.ChainIDMainnet)
	assert.EqualValues(t, tempotx.ChainIdModerato, tempo.ChainIDModerato)
	assert.Equal(t, tempo.USDCEAddress, tempo.MainnetUSDCAddress) //nolint:staticcheck // deprecated alias under test
}

func TestAllowedFeeTokensWithSinglePolicy(t *testing.T) {
	t.Parallel()
	intent, err := NewIntent(IntentConfig{
		Store:            tempo.NewMemoryStore(),
		AllowedFeeTokens: []string{tempo.USDCEAddress, tempo.PathUSDAddress, tempo.USDCEAddress},
		FeePayerPolicy:   &FeePayerPolicy{MaxTotalFee: big.NewInt(1_000), RejectKeyAuthorization: true},
	})
	require.NoError(t, err)
	// Listed order is kept and duplicates are dropped.
	assert.Equal(t, []common.Address{common.HexToAddress(tempo.USDCEAddress), common.HexToAddress(tempo.PathUSDAddress)}, intent.feeTokens)
	require.Len(t, intent.feePayerPolicy, 2)
	policy, err := intent.feePayerPolicyFor(tempo.PathUSDAddress)
	require.NoError(t, err)
	assert.Zero(t, policy.MaxTotalFee.Cmp(big.NewInt(1_000)))
	// Unset limits keep the defaults.
	assert.Zero(t, policy.MaxFeePerGas.Cmp(feePayerMaxFeePerGas))
	assert.Zero(t, policy.MaxPriorityFeePerGas.Cmp(feePayerMaxPriorityFeePerGas))
	assert.True(t, policy.RejectKeyAuthorization)
	_, err = intent.feePayerPolicyFor(tempo.OUSDAddress)
	require.Error(t, err)
}

func TestFeePayerPolicyAloneAppliesToDefaultTokens(t *testing.T) {
	t.Parallel()
	intent, err := NewIntent(IntentConfig{
		Store:          tempo.NewMemoryStore(),
		FeePayerPolicy: &FeePayerPolicy{MaxTotalFee: big.NewInt(7)},
	})
	require.NoError(t, err)
	assert.Nil(t, intent.feeTokens, "the per-chain default fee tokens apply")
	assert.Len(t, intent.feePayerPolicy, len(defaultFeePayerPolicies()))
	for token, policy := range intent.feePayerPolicy {
		assert.Zerof(t, policy.MaxTotalFee.Cmp(big.NewInt(7)), "token %s", token)
	}
}

func TestFeePayerConfigConflicts(t *testing.T) {
	t.Parallel()
	legacy := map[string]FeePayerPolicy{tempo.PathUSDAddress: defaultFeePayerPolicy()}
	for name, config := range map[string]IntentConfig{
		"policies and tokens": {FeePayerPolicies: legacy, AllowedFeeTokens: []string{tempo.PathUSDAddress}},
		"policies and policy": {FeePayerPolicies: legacy, FeePayerPolicy: &FeePayerPolicy{}},
	} {
		config.Store = tempo.NewMemoryStore()
		_, err := NewIntent(config)
		require.ErrorContainsf(t, err, "cannot be combined", name)
	}
	_, err := NewIntent(IntentConfig{Store: tempo.NewMemoryStore(), AllowedFeeTokens: []string{}})
	require.ErrorContains(t, err, "at least one fee token")
	_, err = NewIntent(IntentConfig{Store: tempo.NewMemoryStore(), AllowedFeeTokens: []string{"0x1234"}})
	require.ErrorContains(t, err, "invalid allowed fee token")
	_, err = NewIntent(IntentConfig{Store: tempo.NewMemoryStore(), FeePayerPolicy: &FeePayerPolicy{MaxTotalFee: big.NewInt(0)}})
	require.ErrorContains(t, err, "invalid max total fee")
	_, err = New(Config{Store: tempo.NewMemoryStore(), FeePayerPolicies: legacy, AllowedFeeTokens: []string{tempo.PathUSDAddress}})
	require.ErrorContains(t, err, "cannot be combined")
}

func TestIntentReusesRPCClients(t *testing.T) {
	t.Parallel()
	intent, err := NewIntent(IntentConfig{Store: tempo.NewMemoryStore(), RPCURL: "https://rpc.example"})
	require.NoError(t, err)
	first, err := intent.resolveRPC(tempo.ChargeRequest{})
	require.NoError(t, err)
	second, err := intent.resolveRPC(tempo.ChargeRequest{})
	require.NoError(t, err)
	assert.Same(t, first, second)

	byChain, err := NewIntent(IntentConfig{Store: tempo.NewMemoryStore()})
	require.NoError(t, err)
	moderato := tempo.ChainIDModerato
	first, err = byChain.resolveRPC(tempo.ChargeRequest{MethodDetails: tempo.MethodDetails{ChainID: &moderato}})
	require.NoError(t, err)
	second, err = byChain.resolveRPC(tempo.ChargeRequest{MethodDetails: tempo.MethodDetails{ChainID: &moderato}})
	require.NoError(t, err)
	assert.Same(t, first, second)
	unknown := int64(999999)
	_, err = byChain.resolveRPC(tempo.ChargeRequest{MethodDetails: tempo.MethodDetails{ChainID: &unknown}})
	require.ErrorContains(t, err, "unknown chain id")
}

func TestFeeTokenMustBeAnAllowedFeeToken(t *testing.T) {
	t.Parallel()
	_, err := NewIntent(IntentConfig{
		Store:              tempo.NewMemoryStore(),
		FeePayerPrivateKey: feePayerKey,
		AllowedFeeTokens:   []string{tempo.PathUSDAddress},
		FeeToken:           tempo.USDCEAddress,
	})
	require.ErrorContains(t, err, "is not in AllowedFeeTokens")

	intent, err := NewIntent(IntentConfig{
		Store:              tempo.NewMemoryStore(),
		FeePayerPrivateKey: feePayerKey,
		AllowedFeeTokens:   []string{tempo.PathUSDAddress, tempo.USDCEAddress},
		FeeToken:           tempo.USDCEAddress,
	})
	require.NoError(t, err)
	assert.Equal(t, common.HexToAddress(tempo.USDCEAddress), intent.feeToken)
}
