package mpp

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMCPErrorCode(t *testing.T) {
	tests := []struct {
		err  error
		want int
	}{
		{nil, MCPPaymentRequiredCode},
		{ErrPaymentRequired("realm", ""), MCPPaymentRequiredCode},
		{ErrMalformedCredential("bad"), MCPInvalidParamsCode},
		{ErrInvalidPayload("bad"), MCPInvalidParamsCode},
		{ErrInternalPayment(), MCPInternalErrorCode},
		{errors.New("boom"), MCPInternalErrorCode},
		{ErrVerificationFailed("declined"), MCPPaymentVerificationFailedCode},
		{ErrPaymentExpired("2020-01-01T00:00:00Z"), MCPPaymentVerificationFailedCode},
		{ErrInvalidChallenge("id", "tampered"), MCPPaymentVerificationFailedCode},
		{ErrCredentialReplayed(""), MCPPaymentVerificationFailedCode},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, MCPErrorCode(tt.err), "%v", tt.err)
	}
}

func TestMCPReceiptJSON(t *testing.T) {
	receipt := MCPReceipt{
		Receipt: Receipt{
			Status:     "success",
			Timestamp:  time.Date(2026, 10, 25, 1, 2, 3, 4_000_000, time.UTC),
			Reference:  "0xabc",
			Method:     "tempo",
			ExternalID: "order-1",
			Extensions: map[string]any{"custom": "kept"},
		},
		ChallengeID: "challenge-1",
	}

	encoded, err := json.Marshal(receipt)
	require.NoError(t, err)
	assert.JSONEq(t, `{
		"challengeId": "challenge-1",
		"custom": "kept",
		"externalId": "order-1",
		"method": "tempo",
		"reference": "0xabc",
		"status": "success",
		"timestamp": "2026-10-25T01:02:03.004Z"
	}`, string(encoded))

	var decoded MCPReceipt
	require.NoError(t, json.Unmarshal(encoded, &decoded))
	assert.Equal(t, receipt.ChallengeID, decoded.ChallengeID)
	assert.Equal(t, receipt.Reference, decoded.Reference)
	assert.True(t, receipt.Timestamp.Equal(decoded.Timestamp))
	assert.Equal(t, map[string]any{"custom": "kept"}, decoded.Extensions)

	var missing MCPReceipt
	assert.Error(t, json.Unmarshal([]byte(`{"status":"success","reference":"r","method":"tempo","timestamp":"2026-10-25T01:02:03Z"}`), &missing))
	assert.Error(t, json.Unmarshal([]byte(`{"challengeId":"c","status":"failed","reference":"r","method":"tempo","timestamp":"2026-10-25T01:02:03Z"}`), &missing))
}
