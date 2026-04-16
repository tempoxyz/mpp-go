package server

import (
	"context"
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
	if err != nil {
		t.Fatalf("Charge() error = %v", err)
	}
	if !result.IsChallenge() {
		t.Fatal("result.IsChallenge() = false, want true")
	}
	if result.Challenge.Opaque["trace"] != "abc123" {
		t.Fatalf("result.Challenge.Opaque[trace] = %q, want %q", result.Challenge.Opaque["trace"], "abc123")
	}
	if result.Challenge.Request["amount"] != "0.50" {
		t.Fatalf("result.Challenge.Request[amount] = %#v, want %q", result.Challenge.Request["amount"], "0.50")
	}
	if result.Challenge.Request["recipient"] != "0x70997970c51812dc3a010c7d01b50e0d17dc79c8" {
		t.Fatalf("result.Challenge.Request[recipient] = %#v", result.Challenge.Request["recipient"])
	}
	if result.Challenge.Request["externalId"] != "ext-123" {
		t.Fatalf("result.Challenge.Request[externalId] = %#v, want %q", result.Challenge.Request["externalId"], "ext-123")
	}
	if result.Challenge.Request["feePayer"] != true {
		t.Fatalf("result.Challenge.Request[feePayer] = %#v, want true", result.Challenge.Request["feePayer"])
	}
	if result.Challenge.Request["chainId"] != 42431 {
		t.Fatalf("result.Challenge.Request[chainId] = %#v, want %d", result.Challenge.Request["chainId"], 42431)
	}
	if result.Challenge.Request["memo"] != "0x"+strings.Repeat("ab", 32) {
		t.Fatalf("result.Challenge.Request[memo] = %#v", result.Challenge.Request["memo"])
	}
}

func TestMppCharge_RequiresChargeIntent(t *testing.T) {
	t.Parallel()

	mppServer := New(chargeTestMethod{intents: map[string]Intent{}}, "api.example.com", "secret-key")
	_, err := mppServer.Charge(context.Background(), ChargeParams{Amount: "0.50"})
	if err == nil || !strings.Contains(err.Error(), `does not support charge intent`) {
		t.Fatalf("Charge() error = %v, want missing charge intent error", err)
	}
}
