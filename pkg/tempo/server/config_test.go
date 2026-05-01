package chargeserver

import (
	"math/big"
	"testing"

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
	if err != nil {
		t.Fatalf("MethodFromConfig() error = %v", err)
	}
	requestMap, err := method.BuildChargeRequest(mppserver.ChargeParams{Amount: "0.50"})
	if err != nil {
		t.Fatalf("BuildChargeRequest() error = %v", err)
	}
	request, err := tempo.ParseChargeRequest(requestMap)
	if err != nil {
		t.Fatalf("ParseChargeRequest() error = %v", err)
	}
	if request.MethodDetails.ChainID == nil || *request.MethodDetails.ChainID != tempotx.ChainIdModerato {
		t.Fatalf("request.MethodDetails.ChainID = %v, want %d", request.MethodDetails.ChainID, tempotx.ChainIdModerato)
	}
	if request.Currency != tempotx.AlphaUSDAddress.Hex() {
		t.Fatalf("request.Currency = %q, want %q", request.Currency, tempotx.AlphaUSDAddress.Hex())
	}
}

func TestNewIntentLoadsFeePayerPrivateKeyFromEnv(t *testing.T) {
	t.Setenv("FEE_PAYER_KEY", feePayerKey)
	intent, err := NewIntent(IntentConfig{FeePayerPrivateKeyEnv: "FEE_PAYER_KEY"})
	if err != nil {
		t.Fatalf("NewIntent() error = %v", err)
	}
	if intent.feePayerSigner == nil {
		t.Fatal("intent.feePayerSigner = nil, want signer")
	}
}

func TestNewIntent_DefaultFeePayerPoliciesIncludeKnownTokens(t *testing.T) {
	intent, err := NewIntent(IntentConfig{})
	if err != nil {
		t.Fatalf("NewIntent() error = %v", err)
	}
	moderatoPolicy, ok := intent.feePayerPolicy[tempotx.AlphaUSDAddress.Hex()]
	if !ok {
		t.Fatalf("missing default policy for %s", tempotx.AlphaUSDAddress.Hex())
	}
	if moderatoPolicy.MaxTotalFee.Cmp(big.NewInt(1_000_000)) != 0 {
		t.Fatalf("moderato max total fee = %s, want 1000000", moderatoPolicy.MaxTotalFee)
	}
	if moderatoPolicy.MaxFeePerGas.Cmp(big.NewInt(1)) != 0 {
		t.Fatalf("moderato max fee per gas = %s, want 1", moderatoPolicy.MaxFeePerGas)
	}
	mainnetPolicy, ok := intent.feePayerPolicy[tempo.MainnetUSDCAddress]
	if !ok {
		t.Fatalf("missing default policy for %s", tempo.MainnetUSDCAddress)
	}
	if mainnetPolicy.MaxPriorityFeePerGas.Cmp(big.NewInt(1)) != 0 {
		t.Fatalf("mainnet max priority fee per gas = %s, want 1", mainnetPolicy.MaxPriorityFeePerGas)
	}
}

func TestNewIntentRejectsInvalidFeePayerPolicy(t *testing.T) {
	_, err := NewIntent(IntentConfig{
		FeePayerPolicies: map[string]FeePayerPolicy{
			testCurrency: {
				Decimals:             6,
				MaxFeePerGas:         big.NewInt(1),
				MaxPriorityFeePerGas: big.NewInt(2),
				MaxTotalFee:          big.NewInt(10),
			},
		},
	})
	if err == nil || err.Error() != "tempo server: max priority fee per gas exceeds max fee per gas for 0x20c0000000000000000000000000000000000001" {
		t.Fatalf("NewIntent() error = %v, want max priority fee validation error", err)
	}
}
