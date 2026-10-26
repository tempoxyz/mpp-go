package tempo

import (
	"encoding/json"
	"math/big"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/assert"
	"github.com/tempoxyz/mpp-go/pkg/mpp"
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
	assert.NotContains(t, request.Map()["methodDetails"], "memo")
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

func TestParseChargeRequest_KeepsChainIDFromWireChallenge(t *testing.T) {
	t.Parallel()

	request, err := NormalizeChargeRequest(ChargeRequestParams{
		Amount:    "0.50",
		Currency:  "0x20c0000000000000000000000000000000000001",
		Recipient: "0x70997970c51812dc3a010c7d01b50e0d17dc79c8",
		Decimals:  6,
		ChainID:   42431,
	})
	if !assert.NoErrorf(t, err,
		"NormalizeChargeRequest() error = %v", err) {
		return
	}

	// Challenge JSON is decoded with json.Number, so the chainId a client or
	// relay sees after parsing the header is not the int64 Map() produced.
	issued := mpp.NewChallenge("secret", "api.example.com", MethodName, IntentCharge, request.Map())
	received, err := mpp.ParseChallenge(issued.ToAuthenticate("api.example.com"))
	if !assert.NoErrorf(t, err,
		"ParseChallenge() error = %v", err) {
		return
	}
	if _, ok := received.Request["methodDetails"].(map[string]any)["chainId"].(json.Number); !assert.True(t, ok,
		"wire chainId should decode as json.Number") {
		return
	}

	parsed, err := ParseChargeRequest(received.Request)
	if !assert.NoErrorf(t, err,
		"ParseChargeRequest() error = %v", err) {
		return
	}
	if !assert.NotNil(t, parsed.MethodDetails.ChainID,
		"ParseChargeRequest() dropped methodDetails.chainId from the wire challenge") {
		return
	}
	assert.Equal(t, int64(42431), *parsed.MethodDetails.ChainID)
}

func TestParseChargeRequest_RejectsNonIntegerJSONNumberChainID(t *testing.T) {
	t.Parallel()

	_, err := ParseChargeRequest(map[string]any{
		"amount":    "500000",
		"currency":  "0x20c0000000000000000000000000000000000001",
		"recipient": "0x70997970c51812dc3a010c7d01b50e0d17dc79c8",
		"methodDetails": map[string]any{
			"chainId": json.Number("1.5"),
		},
	})
	assert.ErrorContains(t, err, "invalid chainId")
}

func TestParseChargeRequest_AcceptsIntegralJSONNumberChainID(t *testing.T) {
	t.Parallel()

	for _, raw := range []string{"42431.0", "4.2431e4"} {
		request, err := ParseChargeRequest(map[string]any{
			"amount":    "500000",
			"currency":  "0x20c0000000000000000000000000000000000001",
			"recipient": "0x70997970c51812dc3a010c7d01b50e0d17dc79c8",
			"methodDetails": map[string]any{
				"chainId": json.Number(raw),
			},
		})
		if !assert.NoError(t, err, raw) || !assert.NotNil(t, request.MethodDetails.ChainID, raw) {
			continue
		}
		assert.Equal(t, int64(42431), *request.MethodDetails.ChainID, raw)
	}
}

func TestNormalizeChargeRequest_RejectsInvalidSplitMemo(t *testing.T) {
	t.Parallel()

	_, err := NormalizeChargeRequest(ChargeRequestParams{
		Amount:    "1",
		Currency:  "0x20c0000000000000000000000000000000000001",
		Recipient: "0x70997970c51812dc3a010c7d01b50e0d17dc79c8",
		Splits: []SplitParams{{
			Amount:    "0.10",
			Recipient: "0x3c44cdddb6a900fa2b585dd299e03d12fa4293bc",
			Memo:      "0x1234",
		}},
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

func TestNormalizeChargeRequest_RejectsBareDecimalSeparator(t *testing.T) {
	t.Parallel()

	// "." has no digits on either side of the separator. It used to normalize
	// to "0", which downgrades the charge to a zero-amount one.
	_, err := NormalizeChargeRequest(ChargeRequestParams{
		Amount:    ".",
		Currency:  "0x20c0000000000000000000000000000000000001",
		Recipient: "0x70997970c51812dc3a010c7d01b50e0d17dc79c8",
		Decimals:  6,
	})
	if !assert.Falsef(t, err == nil || !strings.Contains(err.Error(), `invalid amount "."`),
		"NormalizeChargeRequest() error = %v, want invalid amount error", err) {
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

func TestEncodeTransferWithMemo_RejectsInvalidHex(t *testing.T) {
	t.Parallel()

	_, err := EncodeTransferWithMemo(
		"0x70997970c51812dc3a010c7d01b50e0d17dc79c8",
		big.NewInt(500000),
		"0x"+strings.Repeat("zz", 32),
	)
	assert.EqualError(t, err, "tempo: memo must be hex encoded")
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

func TestMatchTransferCalldata_Attribution(t *testing.T) {
	t.Parallel()

	amount := big.NewInt(500000)
	recipient := "0x70997970c51812dc3a010c7d01b50e0d17dc79c8"
	implicitRequest := ChargeRequest{
		Amount:    amount.String(),
		Currency:  "0x20c0000000000000000000000000000000000001",
		Recipient: common.HexToAddress(recipient).Hex(),
	}
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

func TestParseChargeCredentialPayload_CanonicalizesHash(t *testing.T) {
	t.Parallel()

	canonical := "0x2120c5a4e1f6b7d9c3a8e2f4b6d8a0c2e4f6a8b0c2d4e6f8a0b2c4d6e8f0a2b4"
	for _, spelling := range []string{
		canonical,
		strings.TrimPrefix(canonical, "0x"),
		"0X" + strings.ToUpper(strings.TrimPrefix(canonical, "0x")),
		"  " + canonical + "  ",
	} {
		payload, err := ParseChargeCredentialPayload(map[string]any{"type": "hash", "hash": spelling})
		if !assert.NoErrorf(t, err,
			"ParseChargeCredentialPayload(%q) error = %v", spelling, err) {
			continue
		}
		assert.Equalf(t, canonical, payload.Hash,
			"ParseChargeCredentialPayload(%q).Hash", spelling)
		assert.Equalf(t, ChargeStoreKey(canonical), ChargeStoreKey(payload.Hash),
			"store key for %q", spelling)
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
			input: map[string]any{"type": "hash", "hash": "0x2120c5a4e1f6b7d9c3a8e2f4b6d8a0c2e4f6a8b0c2d4e6f8a0b2c4d6e8f0a2b4"},
			want:  ChargeCredentialPayload{Type: CredentialTypeHash, Hash: "0x2120c5a4e1f6b7d9c3a8e2f4b6d8a0c2e4f6a8b0c2d4e6f8a0b2c4d6e8f0a2b4"},
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
			name:    "hash too short",
			input:   map[string]any{"type": "hash", "hash": "0xabc123"},
			wantErr: "invalid transaction hash",
		},
		{
			name:    "hash not hex",
			input:   map[string]any{"type": "hash", "hash": "0x" + strings.Repeat("zz", 32)},
			wantErr: "invalid transaction hash",
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

func TestParseChargeRequest_CanonicalizesSplitAmounts(t *testing.T) {
	t.Parallel()

	// The total amount is already canonicalized; split amounts must be too,
	// because verifiers compare them as strings with decoded transfer amounts.
	request, err := ParseChargeRequest(map[string]any{
		"amount":    "0500",
		"currency":  "0x20c0000000000000000000000000000000000001",
		"recipient": "0x70997970c51812dc3a010c7d01b50e0d17dc79c8",
		"methodDetails": map[string]any{
			"splits": []any{
				map[string]any{
					"amount":    "0100",
					"recipient": "0x3c44cdddb6a900fa2b585dd299e03d12fa4293bc",
				},
				map[string]any{
					"amount":    " 50 ",
					"recipient": "0x90f79bf6eb2c4f870365e785982e1f101e93b906",
				},
			},
		},
	})
	if !assert.NoErrorf(t, err,
		"ParseChargeRequest() error = %v", err) {
		return
	}
	assert.Equal(t, "500", request.Amount)
	if !assert.Len(t, request.MethodDetails.Splits, 2) {
		return
	}
	assert.Equal(t, "100", request.MethodDetails.Splits[0].Amount)
	assert.Equal(t, "50", request.MethodDetails.Splits[1].Amount)
}
