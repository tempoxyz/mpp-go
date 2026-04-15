package tempo

import (
	"math/big"
	"reflect"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
)

func TestNormalizeChargeRequest_RoundTripsCanonicalShape(t *testing.T) {
	t.Parallel()

	request, err := NormalizeChargeRequest(ChargeRequestParams{
		Amount:      "0.50",
		Currency:    "0x20c0000000000000000000000000000000000001",
		Recipient:   "0x70997970c51812dc3a010c7d01b50e0d17dc79c8",
		Decimals:    6,
		Description: "Coffee",
		ExternalID:  "ext-123",
		ChainID:     42431,
		FeePayer:    true,
		FeePayerURL: "https://fee-payer.example.com",
		Memo:        "0x" + strings.ToUpper(strings.Repeat("ab", 32)),
		Splits: []SplitParams{{
			Amount:    "0.10",
			Memo:      "0x" + strings.Repeat("cd", 32),
			Recipient: "0x3c44cdddb6a900fa2b585dd299e03d12fa4293bc",
		}},
		SupportedModes: []ChargeMode{ChargeModePull, ChargeModePush},
	})
	if err != nil {
		t.Fatalf("NormalizeChargeRequest() error = %v", err)
	}

	if request.Amount != "500000" {
		t.Fatalf("request.Amount = %q, want %q", request.Amount, "500000")
	}
	if request.Currency != common.HexToAddress("0x20c0000000000000000000000000000000000001").Hex() {
		t.Fatalf("request.Currency = %q", request.Currency)
	}
	if request.Recipient != common.HexToAddress("0x70997970c51812dc3a010c7d01b50e0d17dc79c8").Hex() {
		t.Fatalf("request.Recipient = %q", request.Recipient)
	}
	if request.MethodDetails.Memo != "0x"+strings.Repeat("ab", 32) {
		t.Fatalf("request.MethodDetails.Memo = %q", request.MethodDetails.Memo)
	}
	if request.MethodDetails.FeePayerURL != "https://fee-payer.example.com" {
		t.Fatalf("request.MethodDetails.FeePayerURL = %q", request.MethodDetails.FeePayerURL)
	}
	if got := len(request.MethodDetails.Splits); got != 1 {
		t.Fatalf("len(request.MethodDetails.Splits) = %d, want 1", got)
	}
	if request.MethodDetails.Splits[0].Amount != "100000" {
		t.Fatalf("request.MethodDetails.Splits[0].Amount = %q, want 100000", request.MethodDetails.Splits[0].Amount)
	}

	parsed, err := ParseChargeRequest(request.Map())
	if err != nil {
		t.Fatalf("ParseChargeRequest() error = %v", err)
	}
	if !reflect.DeepEqual(parsed, request) {
		t.Fatalf("ParseChargeRequest() = %#v, want %#v", parsed, request)
	}

	if !request.Allows(CredentialTypeTransaction) {
		t.Fatal("request should allow transaction credentials")
	}
	if !request.Allows(CredentialTypeHash) {
		t.Fatal("request should allow hash credentials")
	}
}

func TestNormalizeChargeRequest_RejectsInvalidMemo(t *testing.T) {
	t.Parallel()

	_, err := NormalizeChargeRequest(ChargeRequestParams{
		Amount:    "1",
		Currency:  "0x20c0000000000000000000000000000000000001",
		Recipient: "0x70997970c51812dc3a010c7d01b50e0d17dc79c8",
		Memo:      "0x1234",
	})
	if err == nil || !strings.Contains(err.Error(), "memo must be exactly 32 bytes") {
		t.Fatalf("NormalizeChargeRequest() error = %v, want invalid memo error", err)
	}
}

func TestNormalizeChargeRequest_RejectsNegativeDecimals(t *testing.T) {
	t.Parallel()

	_, err := NormalizeChargeRequest(ChargeRequestParams{
		Amount:    "1.25",
		Currency:  "0x20c0000000000000000000000000000000000001",
		Recipient: "0x70997970c51812dc3a010c7d01b50e0d17dc79c8",
		Decimals:  -1,
	})
	if err == nil || !strings.Contains(err.Error(), "decimals must be non-negative") {
		t.Fatalf("NormalizeChargeRequest() error = %v, want negative decimals error", err)
	}
}

func TestNormalizeChargeRequest_RejectsInvalidSplits(t *testing.T) {
	t.Parallel()

	_, err := NormalizeChargeRequest(ChargeRequestParams{
		Amount:    "1",
		Currency:  "0x20c0000000000000000000000000000000000001",
		Recipient: "0x70997970c51812dc3a010c7d01b50e0d17dc79c8",
		Splits: []SplitParams{{
			Amount:    "1",
			Recipient: "0x3c44cdddb6a900fa2b585dd299e03d12fa4293bc",
		}},
	})
	if err == nil || !strings.Contains(err.Error(), "split total must be less than the total amount") {
		t.Fatalf("NormalizeChargeRequest() error = %v, want invalid split total", err)
	}
}

func TestEncodeAttribution_VerifiesServerFingerprint(t *testing.T) {
	t.Parallel()

	memo := EncodeAttribution("api.example.com", "cli-app", "challenge-1")
	if len(memo) != 66 {
		t.Fatalf("len(memo) = %d, want 66", len(memo))
	}
	if !IsAttributionMemo(memo) {
		t.Fatalf("IsAttributionMemo(%q) = false, want true", memo)
	}
	if !VerifyAttributionServer(memo, "api.example.com") {
		t.Fatalf("VerifyAttributionServer(%q, api.example.com) = false, want true", memo)
	}
	if VerifyAttributionServer(memo, "other.example.com") {
		t.Fatalf("VerifyAttributionServer(%q, other.example.com) = true, want false", memo)
	}
	if !VerifyAttributionChallenge(memo, "challenge-1") {
		t.Fatalf("VerifyAttributionChallenge(%q, challenge-1) = false, want true", memo)
	}
	if VerifyAttributionChallenge(memo, "challenge-2") {
		t.Fatalf("VerifyAttributionChallenge(%q, challenge-2) = true, want false", memo)
	}
}

func TestMatchTransferCalldata_MemoAndAttributionFallback(t *testing.T) {
	t.Parallel()

	amount := big.NewInt(500000)
	recipient := "0x70997970c51812dc3a010c7d01b50e0d17dc79c8"
	explicitMemo := "0x" + strings.Repeat("ab", 32)

	explicitRequest := ChargeRequest{
		Amount:    amount.String(),
		Currency:  "0x20c0000000000000000000000000000000000001",
		Recipient: common.HexToAddress(recipient).Hex(),
		MethodDetails: MethodDetails{
			Memo: explicitMemo,
		},
	}

	calldata, err := EncodeTransferWithMemo(recipient, amount, explicitMemo)
	if err != nil {
		t.Fatalf("EncodeTransferWithMemo() error = %v", err)
	}
	if !MatchTransferCalldata(calldata, explicitRequest, "ignored.example.com", "ignored-challenge") {
		t.Fatal("MatchTransferCalldata() = false, want true for explicit memo")
	}

	implicitRequest := explicitRequest
	implicitRequest.MethodDetails.Memo = ""
	attributionMemo := EncodeAttribution("api.example.com", "cli-app", "challenge-1")
	attributedCalldata, err := EncodeTransferWithMemo(recipient, amount, attributionMemo)
	if err != nil {
		t.Fatalf("EncodeTransferWithMemo() attribution error = %v", err)
	}
	if !MatchTransferCalldata(attributedCalldata, implicitRequest, "api.example.com", "challenge-1") {
		t.Fatal("MatchTransferCalldata() = false, want true for attribution memo")
	}
	if MatchTransferCalldata(attributedCalldata, implicitRequest, "other.example.com", "challenge-1") {
		t.Fatal("MatchTransferCalldata() = true, want false for wrong attribution realm")
	}
	if MatchTransferCalldata(attributedCalldata, implicitRequest, "api.example.com", "challenge-2") {
		t.Fatal("MatchTransferCalldata() = true, want false for wrong challenge binding")
	}
}
