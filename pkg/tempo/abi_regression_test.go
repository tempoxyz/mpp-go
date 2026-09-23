package tempo

import "testing"

// TestMatchTransferCalldata_RejectsMalformedAmountHex proves that
// MatchTransferCalldata no longer silently treats a non-hex amount field as
// zero. Before the fix, big.Int.SetString's ok return value was ignored, so
// malformed calldata with the correct selector and length would parse the
// amount as 0 and could spuriously match a zero-amount ChargeRequest.
func TestMatchTransferCalldata_RejectsMalformedAmountHex(t *testing.T) {
	t.Parallel()

	selector := TransferWithMemoSelector
	toAddr := "000000000000000000000000" + "70997970c51812dc3a010c7d01b50e0d17dc79c8"
	// 64 hex chars expected for amount, but this is not valid hex.
	malformedAmount := "zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz"
	memo := "0000000000000000000000000000000000000000000000000000000000000000"[:64]
	dataHex := "0x" + selector + toAddr + malformedAmount + memo

	request := ChargeRequest{
		Recipient: "0x70997970c51812dc3a010c7d01b50e0d17dc79c8",
		Amount:    "0", // matches what a swallowed parse error would silently produce
	}

	if MatchTransferCalldata(dataHex, request, "example.com", "challenge-1") {
		t.Fatal("MatchTransferCalldata() = true, want false for calldata with non-hex amount field")
	}
}
