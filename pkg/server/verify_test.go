package server

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
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
	if !assert.NoErrorf(t, err,
		"VerifyOrChallenge() error = %v", err) {
		return
	}
	if !assert.Falsef(t, result.Receipt == nil || result.Receipt.Reference != "0xreceipt",
		"expected successful receipt, got %#v", result) {
		return
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
	if !assert.NoErrorf(t, err,
		"VerifyOrChallenge(issue) error = %v", err) {
		return
	}
	if !assert.Falsef(t, issued.Challenge == nil || issued.Challenge.Opaque == nil,
		"issued challenge opaque = %#v, want empty map", issued.Challenge) {
		return
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
	if !assert.NoErrorf(t, err,
		"VerifyOrChallenge(verify) error = %v", err) {
		return
	}
	if !assert.Falsef(t, result.Receipt == nil || result.Receipt.Reference != "0xreceipt",
		"expected successful receipt, got %#v", result) {
		return
	}

}

func TestVerifyOrChallenge_VerifiesBodyDigest(t *testing.T) {
	request := map[string]any{"amount": "100"}
	body := []byte(`{"query":"paid"}`)
	digest := mpp.BodyDigest.Compute(body)
	challenge := mpp.NewChallenge(
		"secret-key",
		"api.example.com",
		"tempo",
		"charge",
		request,
		mpp.WithDigest(digest),
		mpp.WithExpires(mpp.Expires.Minutes(5)),
	)
	credential := &mpp.Credential{
		Challenge: challenge.ToEcho(),
		Payload:   map[string]any{"type": "hash", "hash": "0xabc123"},
	}

	result, err := VerifyOrChallenge(context.Background(), VerifyParams{
		Authorization: credential.ToAuthorization(),
		Intent:        verifyTestIntent{},
		Request:       request,
		Body:          body,
		Realm:         "api.example.com",
		SecretKey:     "secret-key",
		Method:        "tempo",
		Expires:       challenge.Expires,
	})
	if !assert.NoErrorf(t, err,
		"VerifyOrChallenge() error = %v", err) {
		return
	}
	if !assert.Falsef(t, result.Receipt == nil || result.Receipt.Reference != "0xreceipt",
		"expected successful receipt, got %#v", result) {
		return
	}
}

func TestVerifyOrChallenge_RejectsMismatchedBodyDigest(t *testing.T) {
	request := map[string]any{"amount": "100"}
	challenge := mpp.NewChallenge(
		"secret-key",
		"api.example.com",
		"tempo",
		"charge",
		request,
		mpp.WithDigest(mpp.BodyDigest.Compute([]byte(`{"query":"paid"}`))),
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
		Body:          []byte(`{"query":"tampered"}`),
		Realm:         "api.example.com",
		SecretKey:     "secret-key",
		Method:        "tempo",
		Expires:       challenge.Expires,
	})
	if !assert.Falsef(t, err == nil || !strings.Contains(err.Error(), "body digest mismatch"),
		"VerifyOrChallenge() error = %v, want body digest mismatch", err) {
		return
	}
}

func TestVerifyOrChallenge_RejectsMissingExpires(t *testing.T) {
	request := map[string]any{
		"amount":    "100",
		"currency":  "0xabc",
		"recipient": "0xdef",
	}
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
	})
	if !assert.Falsef(t, err == nil || !strings.Contains(err.Error(), "missing required expires"),
		"VerifyOrChallenge() error = %v, want missing required expires", err) {
		return
	}

}

func TestVerifyOrChallenge_RejectsMissingDefaultExpires(t *testing.T) {
	request := map[string]any{
		"amount":    "100",
		"currency":  "0xabc",
		"recipient": "0xdef",
	}
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
	})
	if err == nil || !strings.Contains(err.Error(), "missing required expires") {
		t.Fatalf("VerifyOrChallenge() error = %v, want missing required expires", err)
	}
}

func TestVerifyOrChallenge_AllowsDynamicExpiresOverrides(t *testing.T) {
	request := map[string]any{
		"amount":    "100",
		"currency":  "0xabc",
		"recipient": "0xdef",
	}
	issuedExpires := mpp.Expires.Minutes(5)
	challenge := mpp.NewChallenge(
		"secret-key",
		"api.example.com",
		"tempo",
		"charge",
		request,
		mpp.WithExpires(issuedExpires),
	)
	credential := &mpp.Credential{
		Challenge: challenge.ToEcho(),
		Payload:   map[string]any{"type": "hash", "hash": "0xabc123"},
	}

	result, err := VerifyOrChallenge(context.Background(), VerifyParams{
		Authorization: credential.ToAuthorization(),
		Intent:        verifyTestIntent{},
		Request:       request,
		Realm:         "api.example.com",
		SecretKey:     "secret-key",
		Method:        "tempo",
		Expires:       mpp.Expires.Minutes(5),
	})
	if !assert.NoErrorf(t, err,
		"VerifyOrChallenge() error = %v", err) {
		return
	}
	if !assert.Falsef(t, result.Receipt == nil || result.Receipt.Reference != "0xreceipt",
		"expected successful receipt, got %#v", result) {
		return
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
	if !assert.Falsef(t, err == nil || !strings.Contains(err.Error(), "opaque metadata does not match"),
		"VerifyOrChallenge() error = %v, want opaque metadata mismatch", err) {
		return
	}

}
