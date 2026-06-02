package server

import (
	"context"
	"github.com/stretchr/testify/assert"
	"strings"
	"testing"
)

type chargeTestMethod struct {
	intents map[string]Intent
}

func (m chargeTestMethod) Name() string { return "tempo" }

func (m chargeTestMethod) Intents() map[string]Intent { return m.intents }

func TestMppCharge_UsesMetaAsChallengeMeta(t *testing.T) {
	t.Parallel()

	mppServer := New(chargeTestMethod{intents: map[string]Intent{"charge": verifyTestIntent{}}}, "api.example.com", "secret-key")
	result, err := mppServer.Charge(context.Background(), ChargeParams{
		Amount:     "0.50",
		Currency:   "0x20c0000000000000000000000000000000000001",
		Recipient:  "0x70997970c51812dc3a010c7d01b50e0d17dc79c8",
		ExternalID: "ext-123",
		FeePayer:   true,
		ChainID:    42431,
		Memo:       "0x" + strings.Repeat("ab", 32),
		Meta:       map[string]string{"trace": "abc123"},
	})
	if !assert.NoErrorf(t, err,
		"Charge() error = %v", err) {
		return
	}
	if !assert.True(t, result.IsChallenge(),
		"result.IsChallenge() = false, want true") {
		return
	}
	if !assert.Equalf(t, "abc123", result.Challenge.Opaque["trace"],
		"result.Challenge.Opaque[trace] = %q, want %q", result.Challenge.Opaque["trace"], "abc123") {
		return
	}
	if !assert.Equalf(t, "0.50", result.Challenge.Request["amount"],
		"result.Challenge.Request[amount] = %#v, want %q", result.Challenge.Request["amount"], "0.50") {
		return
	}
	if !assert.Equalf(t, "0x70997970c51812dc3a010c7d01b50e0d17dc79c8", result.Challenge.Request["recipient"],
		"result.Challenge.Request[recipient] = %#v", result.Challenge.Request["recipient"]) {
		return
	}
	if !assert.Equalf(t, "ext-123", result.Challenge.Request["externalId"],
		"result.Challenge.Request[externalId] = %#v, want %q", result.Challenge.Request["externalId"], "ext-123") {
		return
	}
	if !assert.Equalf(t, true, result.Challenge.Request["feePayer"],
		"result.Challenge.Request[feePayer] = %#v, want true", result.Challenge.Request["feePayer"]) {
		return
	}
	if !assert.EqualValuesf(t, 42431, result.Challenge.Request["chainId"],
		"result.Challenge.Request[chainId] = %#v, want %d", result.Challenge.Request["chainId"], 42431) {
		return
	}
	if !assert.Equalf(t, "0x"+strings.Repeat("ab", 32), result.Challenge.Request["memo"],
		"result.Challenge.Request[memo] = %#v", result.Challenge.Request["memo"]) {
		return
	}

}

func TestMppCharge_RequiresChargeIntent(t *testing.T) {
	t.Parallel()

	mppServer := New(chargeTestMethod{intents: map[string]Intent{}}, "api.example.com", "secret-key")
	_, err := mppServer.Charge(context.Background(), ChargeParams{Amount: "0.50"})
	if !assert.Falsef(t, err == nil || !strings.Contains(err.Error(), `does not support charge intent`),
		"Charge() error = %v, want missing charge intent error", err) {
		return
	}

}
