package mpp

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
			if !assert.Equalf(t, tt.want, got,
				"SplitAuthenticate() = %#v, want %#v", got, tt.want) {
				return
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
			if !assert.Equalf(t, tt.want, got,
				"FindPaymentAuthorization() = %q, want %q", got, tt.want) {
				return
			}

		})
	}
}

func TestIsMethodName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		value string
		want  bool
	}{
		{name: "lowercase", value: "tempo", want: true},
		{name: "empty", value: "", want: false},
		{name: "uppercase", value: "Tempo", want: false},
		{name: "digit", value: "tempo2", want: false},
		{name: "hyphen", value: "tempo-pay", want: false},
		{name: "underscore", value: "tempo_pay", want: false},
		{name: "dot", value: "tempo.pay", want: false},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			{

				got := isMethodName(tt.value)
				if !assert.Equalf(t, tt.want, got,
					"isMethodName(%q) = %t, want %t", tt.value, got, tt.want) {
					return
				}
			}

		})
	}
}

func TestFormatAuthenticateStrict(t *testing.T) {
	t.Parallel()

	challenge := NewChallenge(
		"secret",
		"api.example.com",
		"tempo",
		"charge",
		map[string]any{"amount": "100"},
		WithDescription(`Pay "premium" path C:\tempo\api`),
	)

	got, err := challenge.ToAuthenticateStrict("api.example.com")
	require.NoError(t, err)
	assert.Equal(t, challenge.ToAuthenticate("api.example.com"), got)
	assert.Contains(t, got, `description="Pay \"premium\" path C:\\tempo\\api"`)
}

func TestFormatAuthenticateStrictRejectsCRLF(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		realm   string
		mutate  func(*Challenge)
		wantErr string
	}{
		{
			name:    "realm line feed",
			realm:   "api.example.com\nnext",
			wantErr: "realm auth-param",
		},
		{
			name: "id carriage return",
			mutate: func(c *Challenge) {
				c.ID = "challenge\rid"
			},
			wantErr: "id auth-param",
		},
		{
			name: "description crlf",
			mutate: func(c *Challenge) {
				c.Description = "Line one\r\nLine two"
			},
			wantErr: "description auth-param",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			challenge := NewChallenge("secret", "api.example.com", "tempo", "charge", map[string]any{"amount": "100"})
			if tt.mutate != nil {
				tt.mutate(challenge)
			}
			realm := "api.example.com"
			if tt.realm != "" {
				realm = tt.realm
			}

			got, err := challenge.ToAuthenticateStrict(realm)
			require.Error(t, err, "ToAuthenticateStrict() = %q", got)
			assert.Contains(t, err.Error(), tt.wantErr)
			assert.Contains(t, err.Error(), "CR or LF")
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
			name:    "invalid uppercase method",
			header:  `Payment id="abc", realm="api.example.com", method="Tempo", intent="charge", request="e30"`,
			wantErr: `invalid challenge method`,
		},
		{
			name:    "invalid method with digit",
			header:  `Payment id="abc", realm="api.example.com", method="tempo2", intent="charge", request="e30"`,
			wantErr: `invalid challenge method`,
		},
		{
			name:    "invalid punctuated method",
			header:  `Payment id="abc", realm="api.example.com", method="tempo-pay", intent="charge", request="e30"`,
			wantErr: `invalid challenge method`,
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
			} {
				got, err := parse.fn(tt.header)
				if tt.wantErr != "" {
					if !assert.Falsef(t, err == nil || !strings.Contains(err.Error(), tt.wantErr),
						"%s() error = %v, want substring %q", parse.name, err, tt.wantErr) {
						return
					}

					continue
				}
				if !assert.NoErrorf(t, err,
					"%s() unexpected error: %v", parse.name, err) {
					return
				}

				assertChallengeEqual(t, got, tt.want)
				if !assert.NotNilf(t, got.Request,
					"%s() returned nil Request, want empty object", parse.name) {
					return
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
			name:    "invalid uppercase method",
			header:  "Payment " + b64EncodeAny(map[string]any{"challenge": map[string]any{"id": "abc", "method": "Tempo", "intent": "charge", "request": "e30"}, "payload": map[string]any{}}),
			wantErr: `invalid credential challenge method`,
		},
		{
			name:    "invalid method with digit",
			header:  "Payment " + b64EncodeAny(map[string]any{"challenge": map[string]any{"id": "abc", "method": "tempo2", "intent": "charge", "request": "e30"}, "payload": map[string]any{}}),
			wantErr: `invalid credential challenge method`,
		},
		{
			name:    "invalid punctuated method",
			header:  "Payment " + b64EncodeAny(map[string]any{"challenge": map[string]any{"id": "abc", "method": "tempo-pay", "intent": "charge", "request": "e30"}, "payload": map[string]any{}}),
			wantErr: `invalid credential challenge method`,
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
					if !assert.Falsef(t, err == nil || !strings.Contains(err.Error(), tt.wantErr),
						"%s() error = %v, want substring %q", parse.name, err, tt.wantErr) {
						return
					}

					continue
				}
				if !assert.NoErrorf(t, err,
					"%s() unexpected error: %v", parse.name, err) {
					return
				}

				assertCredentialEqual(t, got, tt.want)
			}
		})
	}
}

func assertChallengeEqual(t *testing.T, got, want *Challenge) {
	t.Helper()

	if got == nil || want == nil {
		if !assert.Equalf(t, want, got,
			"challenge mismatch: got %#v want %#v", got, want) {
			return
		}

		return
	}
	if !assert.Falsef(t, got.ID != want.ID || got.Realm != want.Realm || got.Method != want.Method || got.Intent != want.Intent,
		"challenge identity mismatch: got %#v want %#v", got, want) {
		return
	}
	if !assert.Falsef(t, got.RequestB64 != want.RequestB64 || got.Digest != want.Digest || got.Expires != want.Expires || got.Description != want.Description,
		"challenge metadata mismatch: got %#v want %#v", got, want) {
		return
	}
	if !assert.Truef(t, JSONEqual(got.Request, want.Request),
		"challenge request mismatch: got %#v want %#v", got.Request, want.Request) {
		return
	}
	if !assert.Equalf(t, want.Opaque, got.Opaque,
		"challenge opaque mismatch: got %#v want %#v", got.Opaque, want.Opaque) {
		return
	}

}

func assertCredentialEqual(t *testing.T, got, want *Credential) {
	t.Helper()

	if got == nil || want == nil {
		if !assert.Equalf(t, want, got,
			"credential mismatch: got %#v want %#v", got, want) {
			return
		}

		return
	}
	if !assert.Equalf(t, want.Source, got.Source,
		"credential source mismatch: got %q want %q", got.Source, want.Source) {
		return
	}
	if !assert.Equalf(t, want.Challenge, got.Challenge,
		"credential challenge mismatch: got %#v want %#v", got.Challenge, want.Challenge) {
		return
	}
	if !assert.Truef(t, JSONEqual(got.Payload, want.Payload),
		"credential payload mismatch: got %#v want %#v", got.Payload, want.Payload) {
		return
	}

}
