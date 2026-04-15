package tempo

import (
	"testing"

	tempotx "github.com/tempoxyz/tempo-go/pkg/transaction"
)

func TestDefaultCurrencyForChain(t *testing.T) {
	t.Parallel()

	if got := DefaultCurrencyForChain(tempotx.ChainIdMainnet); got != mainnetUSDCAddress.Hex() {
		t.Fatalf("DefaultCurrencyForChain(mainnet) = %q, want %q", got, mainnetUSDCAddress.Hex())
	}
	if got := DefaultCurrencyForChain(tempotx.ChainIdModerato); got != tempotx.AlphaUSDAddress.Hex() {
		t.Fatalf("DefaultCurrencyForChain(moderato) = %q, want %q", got, tempotx.AlphaUSDAddress.Hex())
	}
	if got := DefaultCurrencyForChain(999999); got != tempotx.AlphaUSDAddress.Hex() {
		t.Fatalf("DefaultCurrencyForChain(unknown) = %q, want %q", got, tempotx.AlphaUSDAddress.Hex())
	}
}
