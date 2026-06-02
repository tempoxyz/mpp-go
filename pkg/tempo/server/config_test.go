package chargeserver

import (
	"math/big"
	"testing"

	"github.com/stretchr/testify/assert"
	mppserver "github.com/tempoxyz/mpp-go/pkg/server"
	"github.com/tempoxyz/mpp-go/pkg/tempo"
	tempotx "github.com/tempoxyz/tempo-go/pkg/transaction"
)

func TestMethodFromConfigBuildsMethod(t *testing.T) {
	t.Parallel()

	method, err := MethodFromConfig(Config{
		RPCURL:    tempotx.RpcUrlModerato,
		Recipient: testRecipient,
	})
	if !assert.NoErrorf(t, err,
		"MethodFromConfig() error = %v", err) {
		return
	}

	requestMap, err := method.BuildChargeRequest(mppserver.ChargeParams{Amount: "0.50"})
	if !assert.NoErrorf(t, err,
		"BuildChargeRequest() error = %v", err) {
		return
	}

	request, err := tempo.ParseChargeRequest(requestMap)
	if !assert.NoErrorf(t, err,
		"ParseChargeRequest() error = %v", err) {
		return
	}
	if !assert.Falsef(t, request.MethodDetails.ChainID == nil || *request.MethodDetails.ChainID != tempotx.ChainIdModerato,
		"request.MethodDetails.ChainID = %v, want %d", request.MethodDetails.ChainID, tempotx.ChainIdModerato) {
		return
	}
	if !assert.Equalf(t, tempotx.AlphaUSDAddress.Hex(), request.Currency,
		"request.Currency = %q, want %q", request.Currency, tempotx.AlphaUSDAddress.Hex()) {
		return
	}

}

func TestNewIntentLoadsFeePayerPrivateKeyFromEnv(t *testing.T) {
	t.Setenv("FEE_PAYER_KEY", feePayerKey)
	intent, err := NewIntent(IntentConfig{FeePayerPrivateKeyEnv: "FEE_PAYER_KEY"})
	if !assert.NoErrorf(t, err,
		"NewIntent() error = %v", err) {
		return
	}
	if !assert.NotNil(t, intent.feePayerSigner,
		"intent.feePayerSigner = nil, want signer") {
		return
	}

}

func TestNewIntent_DefaultFeePayerPoliciesIncludeKnownTokens(t *testing.T) {
	intent, err := NewIntent(IntentConfig{})
	if !assert.NoErrorf(t, err,
		"NewIntent() error = %v", err) {
		return
	}

	moderatoPolicy, ok := intent.feePayerPolicy[tempotx.AlphaUSDAddress.Hex()]
	if !assert.Truef(t, ok,
		"missing default policy for %s", tempotx.AlphaUSDAddress.Hex()) {
		return
	}
	if !assert.Equalf(t, 0, moderatoPolicy.MaxTotalFee.Cmp(big.NewInt(50_000_000_000_000_000)),
		"moderato max total fee = %s, want 50000000000000000", moderatoPolicy.MaxTotalFee) {
		return
	}
	if !assert.Equalf(t, 0, moderatoPolicy.MaxFeePerGas.Cmp(big.NewInt(100_000_000_000)),
		"moderato max fee per gas = %s, want 100000000000", moderatoPolicy.MaxFeePerGas) {
		return
	}

	legacyPolicy, ok := intent.feePayerPolicy["0x20C0000000000000000000000000000000000000"]
	if !assert.True(t, ok,
		"missing default policy for legacy moderato fee token") {
		return
	}
	if !assert.Equalf(t, 0, legacyPolicy.MaxTotalFee.Cmp(big.NewInt(50_000_000_000_000_000)),
		"legacy moderato max total fee = %s, want 50000000000000000", legacyPolicy.MaxTotalFee) {
		return
	}

	mainnetPolicy, ok := intent.feePayerPolicy[tempo.MainnetUSDCAddress]
	if !assert.Truef(t, ok,
		"missing default policy for %s", tempo.MainnetUSDCAddress) {
		return
	}
	if !assert.Equalf(t, 0, mainnetPolicy.MaxPriorityFeePerGas.Cmp(big.NewInt(100_000_000_000)),
		"mainnet max priority fee per gas = %s, want 100000000000", mainnetPolicy.MaxPriorityFeePerGas) {
		return
	}

}

func TestNewIntentRejectsInvalidFeePayerPolicy(t *testing.T) {
	_, err := NewIntent(IntentConfig{
		FeePayerPolicies: map[string]FeePayerPolicy{
			testCurrency: {
				MaxFeePerGas:         big.NewInt(1),
				MaxPriorityFeePerGas: big.NewInt(2),
				MaxTotalFee:          big.NewInt(10),
			},
		},
	})
	if !assert.Falsef(t, err == nil || err.Error() != "tempo server: max priority fee per gas exceeds max fee per gas for 0x20c0000000000000000000000000000000000001",
		"NewIntent() error = %v, want max priority fee validation error", err) {
		return
	}

}
