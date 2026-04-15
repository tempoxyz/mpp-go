package mpp

import (
	"strings"
	"testing"
)

func TestParseAuthorization_RoundTripsOpaque(t *testing.T) {
	t.Parallel()

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

func TestExtractPaymentAuthorization(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		header string
		want   string
	}{
		{
			name:   "payment only",
			header: "Payment abc123",
			want:   "Payment abc123",
		},
		{
			name:   "comma separated schemes",
			header: "Bearer token, Payment abc123, Basic xyz",
			want:   "Payment abc123",
		},
		{
			name:   "case insensitive scheme",
			header: "Bearer token, payment abc123",
			want:   "payment abc123",
		},
		{
			name:   "missing payment scheme",
			header: "Bearer token, Basic xyz",
			want:   "",
		},
	}

	for _, tt := range tests {
		tc := tt
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := ExtractPaymentAuthorization(tc.header); got != tc.want {
				t.Fatalf("ExtractPaymentAuthorization() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestSplitChallenges(t *testing.T) {
	t.Parallel()

	tempoChallenge := NewChallenge("secret", "realm", "tempo", "charge", map[string]any{"amount": "100"}).ToWWWAuthenticate("realm")
	tests := []struct {
		name   string
		header string
		want   []string
	}{
		{
			name:   "merged bearer and payment",
			header: `Bearer realm="example", ` + tempoChallenge,
			want:   []string{`Bearer realm="example"`, tempoChallenge},
		},
		{
			name:   "quoted commas stay inside one challenge",
			header: `Digest realm="alpha,beta", nonce="123", ` + tempoChallenge,
			want:   []string{`Digest realm="alpha,beta", nonce="123"`, tempoChallenge},
		},
		{
			name:   "single payment challenge",
			header: tempoChallenge,
			want:   []string{tempoChallenge},
		},
	}

	for _, tt := range tests {
		tc := tt
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			parts := SplitChallenges(tc.header)
			if len(parts) != len(tc.want) {
				t.Fatalf("len(parts) = %d, want %d (%#v)", len(parts), len(tc.want), parts)
			}
			for i := range tc.want {
				if parts[i] != tc.want[i] {
					t.Fatalf("parts[%d] = %q, want %q", i, parts[i], tc.want[i])
				}
			}
		})
	}
}

func TestParseWWWAuthenticate_RequiresFields(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		header string
	}{
		{
			name:   "missing realm and request",
			header: `Payment id="abc", method="tempo", intent="charge"`,
		},
		{
			name:   "missing request",
			header: `Payment id="abc", realm="api.example.com", method="tempo", intent="charge"`,
		},
	}

	for _, tt := range tests {
		tc := tt
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, err := ParseWWWAuthenticate(tc.header)
			if err == nil || !strings.Contains(err.Error(), "missing required challenge fields") {
				t.Fatalf("ParseWWWAuthenticate() error = %v, want missing required fields", err)
			}
		})
	}
}

func TestChallengeToWWWAuthenticate_AlwaysIncludesRequest(t *testing.T) {
	t.Parallel()

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
