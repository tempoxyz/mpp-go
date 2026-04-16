package server

import (
	"context"
	"strings"
	"testing"

	"github.com/tempoxyz/mpp-go/pkg/mpp"
)

type verifyTestIntent struct{}

func (verifyTestIntent) Name() string { return "charge" }

func (verifyTestIntent) Verify(_ context.Context, _ *mpp.Credential, _ map[string]any) (*mpp.Receipt, error) {
	return mpp.Success("0xreceipt", mpp.WithReceiptMethod("tempo")), nil
}

func TestVerifyOrChallenge_UsesCanonicalRequestMatching(t *testing.T) {
	request := map[string]any{
		"amount":    "100",
		"currency":  "0xabc",
		"recipient": "0xdef",
		"methodDetails": map[string]any{
			"chainId":        42431,
			"supportedModes": []string{"pull", "push"},
		},
	}
	challenge := mpp.NewChallenge(
		"secret-key",
		"api.example.com",
		"tempo",
		"charge",
		request,
		mpp.WithExpires(mpp.Expires.Minutes(5)),
	)
	credential := &mpp.Credential{
		Challenge: challenge.ToEcho(),
		Payload:   map[string]any{"type": "hash", "hash": "0xabc123"},
	}

	result, err := VerifyOrChallenge(context.Background(), VerifyParams{
		Authorization: credential.ToAuthorization(),
		Intent:        verifyTestIntent{},
		Request: map[string]any{
			"amount":    "100",
			"currency":  "0xabc",
			"recipient": "0xdef",
			"methodDetails": map[string]any{
				"chainId":        float64(42431),
				"supportedModes": []any{"pull", "push"},
			},
		},
		Realm:     "api.example.com",
		SecretKey: "secret-key",
		Method:    "tempo",
		Expires:   challenge.Expires,
	})
	if err != nil {
		t.Fatalf("VerifyOrChallenge() error = %v", err)
	}
	if result.Receipt == nil || result.Receipt.Reference != "0xreceipt" {
		t.Fatalf("expected successful receipt, got %#v", result)
	}
}

func TestVerifyOrChallenge_PreservesEmptyOpaqueMaps(t *testing.T) {
	request := map[string]any{
		"amount":    "100",
		"currency":  "0xabc",
		"recipient": "0xdef",
	}
	challenge := mpp.NewChallenge(
		"secret-key",
		"api.example.com",
		"tempo",
		"charge",
		request,
		mpp.WithMeta(map[string]string{}),
		mpp.WithExpires(mpp.Expires.Minutes(5)),
	)
	credential := &mpp.Credential{
		Challenge: challenge.ToEcho(),
		Payload:   map[string]any{"type": "hash", "hash": "0xabc123"},
	}

	issued, err := VerifyOrChallenge(context.Background(), VerifyParams{
		Authorization: "",
		Intent:        verifyTestIntent{},
		Request:       request,
		Realm:         "api.example.com",
		SecretKey:     "secret-key",
		Method:        "tempo",
		Meta:          map[string]string{},
		Expires:       challenge.Expires,
	})
	if err != nil {
		t.Fatalf("VerifyOrChallenge(issue) error = %v", err)
	}
	if issued.Challenge == nil || issued.Challenge.Opaque == nil {
		t.Fatalf("issued challenge opaque = %#v, want empty map", issued.Challenge)
	}

	result, err := VerifyOrChallenge(context.Background(), VerifyParams{
		Authorization: credential.ToAuthorization(),
		Intent:        verifyTestIntent{},
		Request:       request,
		Realm:         "api.example.com",
		SecretKey:     "secret-key",
		Method:        "tempo",
		Meta:          map[string]string{},
		Expires:       challenge.Expires,
	})
	if err != nil {
		t.Fatalf("VerifyOrChallenge(verify) error = %v", err)
	}
	if result.Receipt == nil || result.Receipt.Reference != "0xreceipt" {
		t.Fatalf("expected successful receipt, got %#v", result)
	}
}

func TestVerifyOrChallenge_RejectsMissingExpires(t *testing.T) {
	request := map[string]any{
		"amount":    "100",
		"currency":  "0xabc",
		"recipient": "0xdef",
	}
	issued := mpp.NewChallenge(
		"secret-key",
		"api.example.com",
		"tempo",
		"charge",
		request,
		mpp.WithExpires(mpp.Expires.Minutes(5)),
	)
	tampered := mpp.NewChallenge(
		"secret-key",
		"api.example.com",
		"tempo",
		"charge",
		request,
	)
	credential := &mpp.Credential{
		Challenge: tampered.ToEcho(),
		Payload:   map[string]any{"type": "hash", "hash": "0xabc123"},
	}

	_, err := VerifyOrChallenge(context.Background(), VerifyParams{
		Authorization: credential.ToAuthorization(),
		Intent:        verifyTestIntent{},
		Request:       request,
		Realm:         "api.example.com",
		SecretKey:     "secret-key",
		Method:        "tempo",
		Expires:       issued.Expires,
	})
	if err == nil || !strings.Contains(err.Error(), "missing required expires") {
		t.Fatalf("VerifyOrChallenge() error = %v, want missing required expires", err)
	}
}

func TestVerifyOrChallenge_RejectsOpaqueMismatch(t *testing.T) {
	request := map[string]any{
		"amount":    "100",
		"currency":  "0xabc",
		"recipient": "0xdef",
	}
	challenge := mpp.NewChallenge(
		"secret-key",
		"api.example.com",
		"tempo",
		"charge",
		request,
		mpp.WithMeta(map[string]string{"trace": "issued"}),
		mpp.WithExpires(mpp.Expires.Minutes(5)),
	)
	credential := &mpp.Credential{
		Challenge: challenge.ToEcho(),
		Payload:   map[string]any{"type": "hash", "hash": "0xabc123"},
	}

	_, err := VerifyOrChallenge(context.Background(), VerifyParams{
		Authorization: credential.ToAuthorization(),
		Intent:        verifyTestIntent{},
		Request:       request,
		Realm:         "api.example.com",
		SecretKey:     "secret-key",
		Method:        "tempo",
		Meta:          map[string]string{"trace": "current"},
		Expires:       challenge.Expires,
	})
	if err == nil || !strings.Contains(err.Error(), "opaque metadata does not match") {
		t.Fatalf("VerifyOrChallenge() error = %v, want opaque metadata mismatch", err)
	}
}
