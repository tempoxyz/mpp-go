package mpp

import (
	"reflect"
	"strings"
	"testing"
)

func TestSplitAuthenticate(t *testing.T) {
	t.Parallel()

	payment := NewChallenge("secret", "realm", "tempo", "charge", map[string]any{"amount": "100"}).ToAuthenticate("realm")

	tests := []struct {
		name   string
		header string
		want   []string
	}{
		{
			name:   "empty header",
			header: "",
			want:   nil,
		},
		{
			name:   "single payment challenge",
			header: payment,
			want:   []string{payment},
		},
		{
			name:   "merged schemes",
			header: `Bearer realm="example", ` + payment,
			want:   []string{`Bearer realm="example"`, payment},
		},
		{
			name:   "quoted commas stay inside one scheme",
			header: `Digest realm="alpha,beta", nonce="123", ` + payment,
			want:   []string{`Digest realm="alpha,beta", nonce="123"`, payment},
		},
		{
			name:   "escaped quotes do not break splitting",
			header: `Digest realm="alpha\"beta,beta", nonce="123", ` + payment,
			want:   []string{`Digest realm="alpha\"beta,beta", nonce="123"`, payment},
		},
		{
			name: "multiple payment challenges stay separate",
			header: payment + `, ` + NewChallenge(
				"secret",
				"realm",
				"stripe",
				"charge",
				map[string]any{"amount": "200"},
			).ToAuthenticate("realm"),
			want: []string{
				payment,
				NewChallenge("secret", "realm", "stripe", "charge", map[string]any{"amount": "200"}).ToAuthenticate("realm"),
			},
		},
		{
			name:   "auth params that look like schemes do not split",
			header: `Digest Bearer="alpha", realm="example", ` + payment,
			want:   []string{`Digest Bearer="alpha", realm="example"`, payment},
		},
		{
			name:   "payment challenge before quoted comma scheme",
			header: payment + `, Digest realm="alpha,beta", nonce="123"`,
			want:   []string{payment, `Digest realm="alpha,beta", nonce="123"`},
		},
		{
			name:   "whitespace around merged header is ignored",
			header: `  ` + payment + `  ,   Bearer token   `,
			want:   []string{payment, `Bearer token`},
		},
		{
			name:   "auth param assignment does not start a new scheme",
			header: `Digest realm="example", token68=abc123, ` + payment,
			want:   []string{`Digest realm="example", token68=abc123`, payment},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := SplitAuthenticate(tt.header)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("SplitAuthenticate() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestFindPaymentAuthorization(t *testing.T) {
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
			name:   "merged schemes",
			header: "Bearer token, Payment abc123, Basic xyz",
			want:   "Payment abc123",
		},
		{
			name:   "case insensitive scheme",
			header: "Bearer token, payment abc123",
			want:   "payment abc123",
		},
		{
			name:   "quoted comma before payment",
			header: `Digest realm="alpha,beta", nonce="123", Payment abc123`,
			want:   "Payment abc123",
		},
		{
			name:   "missing payment scheme",
			header: "Bearer token, Basic xyz",
			want:   "",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := FindPaymentAuthorization(tt.header)
			if got != tt.want {
				t.Fatalf("FindPaymentAuthorization() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestParseChallenge(t *testing.T) {
	t.Parallel()

	minimal := NewChallenge("secret", "api.example.com", "tempo", "charge", nil)
	minimal.Request = map[string]any{}
	optional := NewChallenge(
		"secret",
		"api.example.com",
		"tempo",
		"charge",
		map[string]any{"amount": "100", "currency": "0xabc"},
		WithExpires("2026-01-01T00:00:00.000Z"),
		WithDigest("sha-256=:abc123:"),
		WithDescription("Pay for API access"),
		WithMeta(map[string]string{"trace": "123", "route": "paid"}),
	)

	tests := []struct {
		name    string
		header  string
		want    *Challenge
		wantErr string
	}{
		{
			name:   "roundtrip with empty request",
			header: minimal.ToAuthenticate("api.example.com"),
			want:   minimal,
		},
		{
			name:   "roundtrip with optional fields",
			header: optional.ToAuthenticate("api.example.com"),
			want:   optional,
		},
		{
			name:    "invalid scheme",
			header:  `Bearer realm="api.example.com"`,
			wantErr: `expected Payment scheme`,
		},
		{
			name:    "missing required fields",
			header:  `Payment id="abc", method="tempo", intent="charge"`,
			wantErr: `missing required challenge fields`,
		},
		{
			name:    "duplicate auth params",
			header:  `Payment id="abc", realm="api.example.com", method="tempo", intent="charge", intent="session", request="e30"`,
			wantErr: `duplicate auth-param`,
		},
		{
			name:    "unterminated quoted auth param",
			header:  `Payment id="abc", realm="api.example.com", method="tempo", intent="charge", request="e30", opaque="unterminated`,
			wantErr: `unterminated quoted auth-param`,
		},
		{
			name:    "malformed auth param key",
			header:  `Payment id="abc", realm="api.example.com", method="tempo", intent="charge", request="e30", =="bad"`,
			wantErr: `malformed auth-param`,
		},
		{
			name:    "invalid request encoding",
			header:  `Payment id="abc", realm="api.example.com", method="tempo", intent="charge", request="not-base64"`,
			wantErr: `invalid request field`,
		},
		{
			name:    "header too large",
			header:  "Payment " + strings.Repeat("a", maxHeaderPayload),
			wantErr: `WWW-Authenticate header exceeds maximum size`,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			for _, parse := range []struct {
				name string
				fn   func(string) (*Challenge, error)
			}{
				{name: "ParseChallenge", fn: ParseChallenge},
				{name: "FromAuthenticate", fn: FromAuthenticate},
				{name: "FromWWWAuthenticate", fn: FromWWWAuthenticate},
			} {
				got, err := parse.fn(tt.header)
				if tt.wantErr != "" {
					if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
						t.Fatalf("%s() error = %v, want substring %q", parse.name, err, tt.wantErr)
					}
					continue
				}
				if err != nil {
					t.Fatalf("%s() unexpected error: %v", parse.name, err)
				}
				assertChallengeEqual(t, got, tt.want)
				if got.Request == nil {
					t.Fatalf("%s() returned nil Request, want empty object", parse.name)
				}
			}
		})
	}
}

func TestParseCredential(t *testing.T) {
	t.Parallel()

	credential := &Credential{
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

	tests := []struct {
		name    string
		header  string
		want    *Credential
		wantErr string
	}{
		{
			name:   "roundtrip payment credential",
			header: credential.ToAuthorization(),
			want:   credential,
		},
		{
			name:   "merged authorization header",
			header: `Digest realm="alpha,beta", nonce="123", ` + credential.ToAuthorization(),
			want:   credential,
		},
		{
			name:    "missing payment scheme",
			header:  "Bearer token",
			wantErr: `expected Payment scheme`,
		},
		{
			name:    "missing challenge object",
			header:  "Payment " + b64EncodeAny(map[string]any{"payload": map[string]any{}}),
			wantErr: `credential missing required field: challenge`,
		},
		{
			name:    "challenge must be object",
			header:  "Payment " + b64EncodeAny(map[string]any{"challenge": "not-an-object", "payload": map[string]any{}}),
			wantErr: `credential challenge must be an object`,
		},
		{
			name:    "missing payload",
			header:  "Payment " + b64EncodeAny(map[string]any{"challenge": map[string]any{"id": "abc", "method": "tempo", "intent": "charge", "request": "e30"}}),
			wantErr: `credential missing required field: payload`,
		},
		{
			name:    "missing echoed challenge id",
			header:  "Payment " + b64EncodeAny(map[string]any{"challenge": map[string]any{"method": "tempo", "intent": "charge", "request": "e30"}, "payload": map[string]any{}}),
			wantErr: `credential challenge missing required field: id`,
		},
		{
			name:    "invalid opaque encoding",
			header:  "Payment " + b64EncodeAny(map[string]any{"challenge": map[string]any{"id": "abc", "method": "tempo", "intent": "charge", "request": "e30", "opaque": "not-base64"}, "payload": map[string]any{}}),
			wantErr: `invalid credential opaque field`,
		},
		{
			name:    "authorization header too large",
			header:  "Payment " + strings.Repeat("a", maxHeaderPayload),
			wantErr: `Authorization header exceeds maximum size`,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			for _, parse := range []struct {
				name string
				fn   func(string) (*Credential, error)
			}{
				{name: "ParseCredential", fn: ParseCredential},
				{name: "FromAuthorization", fn: FromAuthorization},
			} {
				got, err := parse.fn(tt.header)
				if tt.wantErr != "" {
					if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
						t.Fatalf("%s() error = %v, want substring %q", parse.name, err, tt.wantErr)
					}
					continue
				}
				if err != nil {
					t.Fatalf("%s() unexpected error: %v", parse.name, err)
				}
				assertCredentialEqual(t, got, tt.want)
			}
		})
	}
}

func assertChallengeEqual(t *testing.T, got, want *Challenge) {
	t.Helper()

	if got == nil || want == nil {
		if got != want {
			t.Fatalf("challenge mismatch: got %#v want %#v", got, want)
		}
		return
	}
	if got.ID != want.ID || got.Realm != want.Realm || got.Method != want.Method || got.Intent != want.Intent {
		t.Fatalf("challenge identity mismatch: got %#v want %#v", got, want)
	}
	if got.RequestB64 != want.RequestB64 || got.Digest != want.Digest || got.Expires != want.Expires || got.Description != want.Description {
		t.Fatalf("challenge metadata mismatch: got %#v want %#v", got, want)
	}
	if !JSONEqual(got.Request, want.Request) {
		t.Fatalf("challenge request mismatch: got %#v want %#v", got.Request, want.Request)
	}
	if !reflect.DeepEqual(got.Opaque, want.Opaque) {
		t.Fatalf("challenge opaque mismatch: got %#v want %#v", got.Opaque, want.Opaque)
	}
}

func assertCredentialEqual(t *testing.T, got, want *Credential) {
	t.Helper()

	if got == nil || want == nil {
		if got != want {
			t.Fatalf("credential mismatch: got %#v want %#v", got, want)
		}
		return
	}
	if got.Source != want.Source {
		t.Fatalf("credential source mismatch: got %q want %q", got.Source, want.Source)
	}
	if !reflect.DeepEqual(got.Challenge, want.Challenge) {
		t.Fatalf("credential challenge mismatch: got %#v want %#v", got.Challenge, want.Challenge)
	}
	if !JSONEqual(got.Payload, want.Payload) {
		t.Fatalf("credential payload mismatch: got %#v want %#v", got.Payload, want.Payload)
	}
}
