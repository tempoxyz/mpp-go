package mpp

import "strings"
import "testing"

func TestParseAuthorization_RoundTripsOpaque(t *testing.T) {
	cred := &Credential{
		Challenge: ChallengeEcho{
			ID:      "challenge-id",
			Realm:   "api.example.com",
			Method:  "tempo",
			Intent:  "charge",
			Request: b64EncodeAny(map[string]any{"amount": "100", "currency": "0xabc"}),
			Opaque:  map[string]string{"trace": "123", "route": "paid"},
		},
		Payload: map[string]any{"type": "hash", "hash": "0xabc123"},
		Source:  "did:pkh:eip155:42431:0x1234",
	}

	parsed, err := ParseAuthorization(cred.ToAuthorization())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(parsed.Challenge.Opaque) != len(cred.Challenge.Opaque) {
		t.Fatalf("opaque map length mismatch: got %d want %d", len(parsed.Challenge.Opaque), len(cred.Challenge.Opaque))
	}
	for key, value := range cred.Challenge.Opaque {
		if parsed.Challenge.Opaque[key] != value {
			t.Fatalf("opaque[%q] = %q, want %q", key, parsed.Challenge.Opaque[key], value)
		}
	}
}

func TestExtractPaymentScheme_CommaSeparatedAuthorization(t *testing.T) {
	header := "Bearer token, Payment abc123, Basic xyz"
	if got := ExtractPaymentScheme(header); got != "Payment abc123" {
		t.Fatalf("ExtractPaymentScheme() = %q, want %q", got, "Payment abc123")
	}
}

func TestSplitWWWAuthenticate_MergedHeader(t *testing.T) {
	tempoChallenge := NewChallenge("secret", "realm", "tempo", "charge", map[string]any{"amount": "100"})
	header := `Bearer realm="example", ` + tempoChallenge.ToWWWAuthenticate("realm")
	parts := SplitWWWAuthenticate(header)
	if len(parts) != 2 {
		t.Fatalf("len(parts) = %d, want 2", len(parts))
	}
	if !strings.HasPrefix(parts[0], "Bearer ") {
		t.Fatalf("parts[0] = %q, want Bearer challenge", parts[0])
	}
	if !strings.HasPrefix(parts[1], "Payment ") {
		t.Fatalf("parts[1] = %q, want Payment challenge", parts[1])
	}
}

func TestParseWWWAuthenticate_RequiresRealmAndRequest(t *testing.T) {
	_, err := ParseWWWAuthenticate(`Payment id="abc", method="tempo", intent="charge"`)
	if err == nil || !strings.Contains(err.Error(), "missing required challenge fields") {
		t.Fatalf("ParseWWWAuthenticate() error = %v, want missing required fields", err)
	}
}

func TestChallengeToWWWAuthenticate_AlwaysIncludesRequest(t *testing.T) {
	challenge := NewChallenge("secret", "realm", "tempo", "charge", nil)
	header := challenge.ToWWWAuthenticate("realm")
	if !strings.Contains(header, `request="`) {
		t.Fatalf("header = %q, want request auth-param", header)
	}
	parsed, err := ParseWWWAuthenticate(header)
	if err != nil {
		t.Fatalf("ParseWWWAuthenticate() error = %v", err)
	}
	if parsed.Request == nil {
		t.Fatal("parsed.Request is nil, want empty request object")
	}
}
