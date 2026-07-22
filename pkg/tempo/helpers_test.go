package tempo

import (
	"math/big"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/assert"
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
	if !assert.NoErrorf(t, err,
		"NormalizeChargeRequest() error = %v", err) {
		return
	}
	if !assert.Equalf(t, "500000", request.Amount,
		"request.Amount = %q, want %q", request.Amount, "500000") {
		return
	}
	if !assert.Equalf(t, common.HexToAddress("0x20c0000000000000000000000000000000000001").Hex(), request.Currency,
		"request.Currency = %q", request.Currency) {
		return
	}
	if !assert.Equalf(t, common.HexToAddress("0x70997970c51812dc3a010c7d01b50e0d17dc79c8").Hex(), request.Recipient,
		"request.Recipient = %q", request.Recipient) {
		return
	}
	if !assert.Equalf(t, "0x"+strings.Repeat("ab", 32), request.MethodDetails.Memo,
		"request.MethodDetails.Memo = %q", request.MethodDetails.Memo) {
		return
	}
	if !assert.Equalf(t, "https://fee-payer.example.com", request.MethodDetails.FeePayerURL,
		"request.MethodDetails.FeePayerURL = %q", request.MethodDetails.FeePayerURL) {
		return
	}

	if got := len(request.MethodDetails.Splits); got != 1 {
		assert.Failf(t, "", "len(request.MethodDetails.Splits) = %d, want 1", got)
		return
	}
	if !assert.Equalf(t, "100000", request.MethodDetails.Splits[0].Amount,
		"request.MethodDetails.Splits[0].Amount = %q, want 100000", request.MethodDetails.Splits[0].Amount) {
		return
	}

	parsed, err := ParseChargeRequest(request.Map())
	if !assert.NoErrorf(t, err,
		"ParseChargeRequest() error = %v", err) {
		return
	}
	if !assert.Equalf(t, request, parsed,
		"ParseChargeRequest() = %#v, want %#v", parsed, request) {
		return
	}
	if !assert.True(t, request.Allows(CredentialTypeTransaction),
		"request should allow transaction credentials") {
		return
	}
	if !assert.True(t, request.Allows(CredentialTypeHash),
		"request should allow hash credentials") {
		return
	}

}

	tests := []struct {
	name      string
	input     any
	wantValue int64
	wantOK    bool
	wantErr   string
}{
	{
		name:      "int",
		input:     42431,
		wantValue: 42431,
		wantOK:    true,
	},
	{
		name:      "int64",
		input:     int64(42431),
		wantValue: 42431,
		wantOK:    true,
	},
	{
		name:      "whole number float",
		input:     float64(42431),
		wantValue: 42431,
		wantOK:    true,
	},
	{
		name:    "fractional float",
		input:   float64(42431.5),
		wantErr: "chainId must be an integer",
	},
	{
		name:      "valid string",
		input:     "42431",
		wantValue: 42431,
		wantOK:    true,
	},
	{
		name:    "malformed string",
		input:   "42431abc",
		wantErr: `invalid chainId "42431abc"`,
	},
	{
		name:  "empty string",
		input: "",
	},
	{
		name:  "unsupported type",
		input: true,
	},
	{
		name:  "nil",
		input: nil,
	},
}

for _, tt := range tests {
	tt := tt
	t.Run(tt.name, func(t *testing.T) {
		t.Parallel()

		got, ok, err := asInt64(tt.input)

		if tt.wantErr != "" {
			assert.ErrorContains(t, err, tt.wantErr)
			return
		}

		assert.NoError(t, err)
		assert.Equal(t, tt.wantOK, ok)
		assert.Equal(t, tt.wantValue, got)
	})
}

func TestNormalizeChargeRequest_RejectsInvalidMemo(t *testing.T) {
	t.Parallel()

	_, err := NormalizeChargeRequest(ChargeRequestParams{
		Amount:    "1",
		Currency:  "0x20c0000000000000000000000000000000000001",
		Recipient: "0x70997970c51812dc3a010c7d01b50e0d17dc79c8",
		Memo:      "0x1234",
	})
	if !assert.Falsef(t, err == nil || !strings.Contains(err.Error(), "memo must be exactly 32 bytes"),
		"NormalizeChargeRequest() error = %v, want invalid memo error", err) {
		return
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
	if !assert.Falsef(t, err == nil || !strings.Contains(err.Error(), "decimals must be non-negative"),
		"NormalizeChargeRequest() error = %v, want negative decimals error", err) {
		return
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
	if !assert.Falsef(t, err == nil || !strings.Contains(err.Error(), "split total must be less than the total amount"),
		"NormalizeChargeRequest() error = %v, want invalid split total", err) {
		return
	}

}

func TestEncodeAttribution_VerifiesServerFingerprint(t *testing.T) {
	t.Parallel()

	memo := EncodeAttribution("api.example.com", "cli-app", "challenge-1")
	if !assert.Lenf(t, memo, 66,
		"len(memo) = %d, want 66", len(memo)) {
		return
	}
	if !assert.Truef(t, IsAttributionMemo(memo),
		"IsAttributionMemo(%q) = false, want true", memo) {
		return
	}
	if !assert.Truef(t, VerifyAttributionServer(memo, "api.example.com"),
		"VerifyAttributionServer(%q, api.example.com) = false, want true", memo) {
		return
	}
	if !assert.Falsef(t, VerifyAttributionServer(memo, "other.example.com"),
		"VerifyAttributionServer(%q, other.example.com) = true, want false", memo) {
		return
	}
	if !assert.Truef(t, VerifyAttributionChallenge(memo, "challenge-1"),
		"VerifyAttributionChallenge(%q, challenge-1) = false, want true", memo) {
		return
	}
	if !assert.Falsef(t, VerifyAttributionChallenge(memo, "challenge-2"),
		"VerifyAttributionChallenge(%q, challenge-2) = true, want false", memo) {
		return
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
	if !assert.NoErrorf(t, err,
		"EncodeTransferWithMemo() error = %v", err) {
		return
	}
	if !assert.True(t, MatchTransferCalldata(calldata, explicitRequest, "ignored.example.com", "ignored-challenge"),
		"MatchTransferCalldata() = false, want true for explicit memo") {
		return
	}
	if !assert.False(t, MatchTransferCalldata(calldata+"01", explicitRequest, "ignored.example.com", "ignored-challenge"),
		"MatchTransferCalldata() = true, want false for padded explicit memo calldata") {
		return
	}

	implicitRequest := explicitRequest
	implicitRequest.MethodDetails.Memo = ""
	attributionMemo := EncodeAttribution("api.example.com", "cli-app", "challenge-1")
	attributedCalldata, err := EncodeTransferWithMemo(recipient, amount, attributionMemo)
	if !assert.NoErrorf(t, err,
		"EncodeTransferWithMemo() attribution error = %v", err) {
		return
	}
	if !assert.True(t, MatchTransferCalldata(attributedCalldata, implicitRequest, "api.example.com", "challenge-1"),
		"MatchTransferCalldata() = false, want true for attribution memo") {
		return
	}
	if !assert.False(t, MatchTransferCalldata(attributedCalldata+"01", implicitRequest, "api.example.com", "challenge-1"),
		"MatchTransferCalldata() = true, want false for padded attribution memo calldata") {
		return
	}
	if !assert.False(t, MatchTransferCalldata(attributedCalldata, implicitRequest, "other.example.com", "challenge-1"),
		"MatchTransferCalldata() = true, want false for wrong attribution realm") {
		return
	}
	if !assert.False(t, MatchTransferCalldata(attributedCalldata, implicitRequest, "api.example.com", "challenge-2"),
		"MatchTransferCalldata() = true, want false for wrong challenge binding") {
		return
	}

}

func TestParseChargeCredentialPayload_RoundTripsPayloadShapes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		input   map[string]any
		want    ChargeCredentialPayload
		wantErr string
	}{
		{
			name:  "hash payload",
			input: map[string]any{"type": "hash", "hash": "0xabc123"},
			want:  ChargeCredentialPayload{Type: CredentialTypeHash, Hash: "0xabc123"},
		},
		{
			name:  "transaction payload",
			input: map[string]any{"type": "transaction", "signature": "0xsigned"},
			want:  ChargeCredentialPayload{Type: CredentialTypeTransaction, Signature: "0xsigned"},
		},
		{
			name:  "proof payload",
			input: map[string]any{"type": "proof", "signature": "0xproof"},
			want:  ChargeCredentialPayload{Type: CredentialTypeProof, Signature: "0xproof"},
		},
		{
			name:    "missing hash",
			input:   map[string]any{"type": "hash"},
			wantErr: "missing hash",
		},
		{
			name:    "unsupported type",
			input:   map[string]any{"type": "other"},
			wantErr: "unsupported credential payload type",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			payload, err := ParseChargeCredentialPayload(tt.input)
			if tt.wantErr != "" {
				if !assert.Falsef(t, err == nil || !strings.Contains(err.Error(), tt.wantErr),
					"ParseChargeCredentialPayload() error = %v, want substring %q", err, tt.wantErr) {
					return
				}

				return
			}
			if !assert.NoErrorf(t, err,
				"ParseChargeCredentialPayload() error = %v", err) {
				return
			}
			if !assert.Equalf(t, tt.want, payload,
				"ParseChargeCredentialPayload() = %#v, want %#v", payload, tt.want) {
				return
			}
			if !assert.Equalf(t, tt.input, payload.Map(),
				"payload.Map() = %#v, want %#v", payload.Map(), tt.input) {
				return
			}

		})
	}
}