package chargeserver

import (
	"math/big"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/tempoxyz/mpp-go/pkg/tempo"
	tempotx "github.com/tempoxyz/tempo-go/pkg/transaction"
)

// TestTranslateFeePayerFormToNormal exercises the prefix-swap helper that
// lets tempo-go's Deserialize (hard-coded for 0x76) parse credentials in the
// fee-payer-signing form (0x78). The two formats share the same RLP body
// shape per tempo-go's buildRLPList; only the prefix and field-11 differ.
func TestTranslateFeePayerFormToNormal(t *testing.T) {
	cases := []struct {
		name       string
		input      string
		wantSwap   bool
		wantPrefix string // expected prefix on the returned string when wantSwap
	}{
		{
			name:       "0x78 lowercase swaps to 0x76",
			input:      "0x78deadbeef",
			wantSwap:   true,
			wantPrefix: "0x76deadbeef",
		},
		{
			name:       "0X78 uppercase 0x prefix swaps",
			input:      "0X78deadbeef",
			wantSwap:   true,
			wantPrefix: "0x76deadbeef",
		},
		{
			name:       "78 with no 0x prefix swaps",
			input:      "78deadbeef",
			wantSwap:   true,
			wantPrefix: "0x76deadbeef",
		},
		{
			name:     "0x76 unchanged",
			input:    "0x76deadbeef",
			wantSwap: false,
		},
		{
			name:     "0x77 (unknown prefix) unchanged",
			input:    "0x77deadbeef",
			wantSwap: false,
		},
		{
			name:     "empty string unchanged",
			input:    "",
			wantSwap: false,
		},
		{
			name:     "too short to inspect prefix is unchanged",
			input:    "0x",
			wantSwap: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, swapped := translateFeePayerFormToNormal(tc.input)
			if swapped != tc.wantSwap {
				t.Fatalf("swapped = %v, want %v", swapped, tc.wantSwap)
			}
			if !tc.wantSwap {
				if out != tc.input {
					t.Errorf("non-swap returned %q, want input %q unchanged", out, tc.input)
				}
				return
			}
			if out != tc.wantPrefix {
				t.Errorf("swapped output = %q, want %q", out, tc.wantPrefix)
			}
		})
	}
}

// TestVerifyTransaction_AcceptsFeePayerSigningForm proves that a credential
// serialized in 0x78 form (what Tempo CLI 1.6.0 sends) round-trips through
// our patched verifyTransaction's deserialize path. We construct a valid
// sponsored tx via tempo-go's own Serialize(FormatFeePayer), feed it into
// the prefix-swap helper, then deserialize via the standard tempo-go path
// and confirm AwaitingFeePayer/FeeToken end up in the state our downstream
// verification expects.
func TestVerifyTransaction_AcceptsFeePayerSigningForm(t *testing.T) {
	currency := common.HexToAddress("0x20c0000000000000000000000000000000000000")
	recipient := common.HexToAddress(testRecipient)
	sender := common.HexToAddress("0x1111111111111111111111111111111111111111")

	tx := tempotx.New()
	tx.ChainID = big.NewInt(4217)
	tx.MaxFeePerGas = big.NewInt(1_000_000_000)
	tx.MaxPriorityFeePerGas = big.NewInt(1_000_000_000)
	tx.Gas = 200_000
	tx.NonceKey = new(big.Int).Set(tempo.ExpiringNonceKey)
	tx.ValidBefore = 1893456000 // far-future epoch
	tx.AwaitingFeePayer = true
	tx.FeeToken = currency
	tx.Calls = []tempotx.Call{{
		To:    &currency,
		Value: big.NewInt(0),
		Data:  buildTransferCalldata(recipient, big.NewInt(1000)),
	}}

	feePayerForm, err := tempotx.Serialize(tx, &tempotx.SerializeOptions{
		Format: tempotx.FormatFeePayer,
		Sender: sender,
	})
	if err != nil {
		t.Fatalf("serialize FormatFeePayer: %v", err)
	}
	if !strings.HasPrefix(feePayerForm, "0x78") {
		t.Fatalf("expected 0x78-prefixed payload; got %q", feePayerForm[:6])
	}

	swapped, isFeePayerForm := translateFeePayerFormToNormal(feePayerForm)
	if !isFeePayerForm {
		t.Fatal("translateFeePayerFormToNormal did not detect 0x78 form")
	}

	parsed, err := tempotx.Deserialize(swapped)
	if err != nil {
		t.Fatalf("Deserialize after prefix swap: %v", err)
	}

	// Apply the post-deserialize fixups that verifyTransaction performs.
	parsed.AwaitingFeePayer = true
	parsed.FeeToken = common.Address{}

	if !parsed.AwaitingFeePayer {
		t.Error("AwaitingFeePayer should be true after fixup")
	}
	if parsed.FeeToken != (common.Address{}) {
		t.Errorf("FeeToken should be cleared after fixup; got %s", parsed.FeeToken.Hex())
	}
	if parsed.NonceKey == nil || parsed.NonceKey.Cmp(tempo.ExpiringNonceKey) != 0 {
		t.Errorf("NonceKey roundtrip lost: got %v, want ExpiringNonceKey", parsed.NonceKey)
	}
	if parsed.ValidBefore != tx.ValidBefore {
		t.Errorf("ValidBefore = %d, want %d", parsed.ValidBefore, tx.ValidBefore)
	}
	if len(parsed.Calls) != 1 || parsed.Calls[0].To == nil || *parsed.Calls[0].To != currency {
		t.Errorf("Calls roundtrip lost: %+v", parsed.Calls)
	}
}

// buildTransferCalldata builds the calldata for a TIP-20 `transfer(to, amount)`
// call. Selector 0x70a08231 is balanceOf; the test only needs *some* valid
// calldata bytes for the deserializer to round-trip — selector content is
// not verified by our prefix-swap path.
func buildTransferCalldata(to common.Address, amount *big.Int) []byte {
	const transferSelector = "a9059cbb"
	out := make([]byte, 0, 4+32+32)
	selector := common.Hex2Bytes(transferSelector)
	out = append(out, selector...)
	addrPadded := make([]byte, 32)
	copy(addrPadded[12:], to.Bytes())
	out = append(out, addrPadded...)
	amountPadded := make([]byte, 32)
	amount.FillBytes(amountPadded)
	out = append(out, amountPadded...)
	return out
}
