package chargeserver

import (
	"testing"

	mppserver "github.com/tempoxyz/mpp-go/pkg/server"
	"github.com/tempoxyz/mpp-go/pkg/tempo"
	tempotx "github.com/tempoxyz/tempo-go/pkg/transaction"
)

func TestNewConvenienceBuildsMethod(t *testing.T) {
	t.Parallel()

	method, err := New(Config{
		RPCURL:    tempotx.RpcUrlModerato,
		Recipient: testRecipient,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
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
