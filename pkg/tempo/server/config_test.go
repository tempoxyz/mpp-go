package chargeserver

import (
	"math/big"
	"testing"

	"github.com/stretchr/testify/assert"
	mppserver "github.com/tempoxyz/mpp-go/pkg/server"
	"github.com/tempoxyz/mpp-go/pkg/tempo"
	tempotx "github.com/tempoxyz/tempo-go/pkg/transaction"
)

func TestNewBuildsMethod(t *testing.T) {
	t.Parallel()

	method, err := New(Config{Store: tempo.NewMemoryStore(),
		RPCURL:    tempotx.RpcUrlModerato,
		Recipient: testRecipient,
	})
	if !assert.NoErrorf(t, err,
		"New() error = %v", err) {
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
	// Moderato's first default offer is OUSD (followed by pathUSD).
	if !assert.Equalf(t, tempo.OUSDAddress, request.Currency,
		"request.Currency = %q, want %q", request.Currency, tempo.OUSDAddress) {
		return
	}

}

func TestNewIntent_DefaultFeePayerPoliciesIncludeKnownTokens(t *testing.T) {
	intent, err := NewIntent(IntentConfig{Store: tempo.NewMemoryStore()})
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

	mainnetPolicy, ok := intent.feePayerPolicy[tempo.USDCEAddress]
	if !assert.Truef(t, ok,
		"missing default policy for %s", tempo.USDCEAddress) {
		return
	}
	if !assert.Equalf(t, 0, mainnetPolicy.MaxPriorityFeePerGas.Cmp(big.NewInt(100_000_000_000)),
		"mainnet max priority fee per gas = %s, want 100000000000", mainnetPolicy.MaxPriorityFeePerGas) {
		return
	}

}

func TestNewIntentRejectsInvalidFeePayerPolicy(t *testing.T) {
	_, err := NewIntent(IntentConfig{Store: tempo.NewMemoryStore(),
		AllowedFeeTokens: []string{testCurrency},
		FeePayerPolicy: &FeePayerPolicy{
			MaxFeePerGas:         big.NewInt(1),
			MaxPriorityFeePerGas: big.NewInt(2),
			MaxTotalFee:          big.NewInt(10),
		},
	})
	assert.ErrorContains(t, err, "max priority fee per gas exceeds max fee per gas")
}
