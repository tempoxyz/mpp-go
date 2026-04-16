package mpp

import (
	"strings"
	"testing"
)

func TestGenerateChallengeIDCrossSDKCompatibilityVectors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   GenerateChallengeIDInput
		want string
	}{
		{
			name: "basic charge",
			in: GenerateChallengeIDInput{
				SecretKey: "test-secret-key-12345",
				Realm:     "api.example.com",
				Method:    "tempo",
				Intent:    "charge",
				Request: map[string]any{
					"amount":    "1000000",
					"currency":  "0x20c0000000000000000000000000000000000000",
					"recipient": "0x1234567890abcdef1234567890abcdef12345678",
				},
			},
			want: "XmJ98SdsAdzwP9Oa-8In322Uh6yweMO6rywdomWk_V4",
		},
		{
			name: "with expires",
			in: GenerateChallengeIDInput{
				SecretKey: "test-secret-key-12345",
				Realm:     "api.example.com",
				Method:    "tempo",
				Intent:    "charge",
				Expires:   "2026-01-29T12:00:00Z",
				Request: map[string]any{
					"amount":    "5000000",
					"currency":  "0x20c0000000000000000000000000000000000000",
					"recipient": "0xabcdef1234567890abcdef1234567890abcdef12",
				},
			},
			want: "EvqUWMPJjqhoVJVG3mhTYVqCa3Mk7bUVd_OjeJGek1A",
		},
		{
			name: "with digest",
			in: GenerateChallengeIDInput{
				SecretKey: "my-server-secret",
				Realm:     "payments.example.org",
				Method:    "tempo",
				Intent:    "charge",
				Digest:    "sha-256=X48E9qOokqqrvdts8nOJRJN3OWDUoyWxBf7kbu9DBPE=",
				Request: map[string]any{
					"amount":    "250000",
					"currency":  "USD",
					"recipient": "0x9999999999999999999999999999999999999999",
				},
			},
			want: "qcJUPoapy4bFLznQjQUutwPLyXW7FvALrWA_sMENgAY",
		},
		{
			name: "full challenge",
			in: GenerateChallengeIDInput{
				SecretKey: "production-secret-abc123",
				Realm:     "api.tempo.xyz",
				Method:    "tempo",
				Intent:    "charge",
				Expires:   "2026-02-01T00:00:00Z",
				Digest:    "sha-256=abc123def456",
				Request: map[string]any{
					"amount":      "10000000",
					"currency":    "0x20c0000000000000000000000000000000000000",
					"recipient":   "0x742d35Cc6634C0532925a3b844Bc9e7595f1B0F2",
					"description": "API access fee",
					"externalId":  "order-12345",
				},
			},
			want: "J6w7zq6nHLnchss3AYbLxNirdpuaV8_Msn37DQSz6Bw",
		},
		{
			name: "different secret different id",
			in: GenerateChallengeIDInput{
				SecretKey: "different-secret-key",
				Realm:     "api.example.com",
				Method:    "tempo",
				Intent:    "charge",
				Request: map[string]any{
					"amount":    "1000000",
					"currency":  "0x20c0000000000000000000000000000000000000",
					"recipient": "0x1234567890abcdef1234567890abcdef12345678",
				},
			},
			want: "_o55RP0duNvJYtw9PXnf44mGyY5ajV_wwGzoGdTFuNs",
		},
		{
			name: "empty request alternate fixture",
			in: GenerateChallengeIDInput{
				SecretKey: "test-key",
				Realm:     "test.example.com",
				Method:    "tempo",
				Intent:    "authorize",
				Request:   map[string]any{},
			},
			want: "MYEC2oq3_B3cHa_My1Lx3NQKn_iUiMfsns6361N0SX0",
		},
		{
			name: "unicode request data",
			in: GenerateChallengeIDInput{
				SecretKey: "unicode-test-key",
				Realm:     "api.example.com",
				Method:    "tempo",
				Intent:    "charge",
				Request: map[string]any{
					"amount":      "100",
					"currency":    "EUR",
					"recipient":   "0x1111111111111111111111111111111111111111",
					"description": "Payment for caf\u00e9 \u2615",
				},
			},
			want: "1_GKJqATKvVnIUY3f8MFq48bMs18JHz_3CBK8pu52yA",
		},
		{
			name: "nested methodDetails with fee payer",
			in: GenerateChallengeIDInput{
				SecretKey: "nested-test-key",
				Realm:     "api.tempo.xyz",
				Method:    "tempo",
				Intent:    "charge",
				Request: map[string]any{
					"amount":    "5000000",
					"currency":  "0x20c0000000000000000000000000000000000000",
					"recipient": "0x2222222222222222222222222222222222222222",
					"methodDetails": map[string]any{
						"chainId":  42431,
						"feePayer": true,
					},
				},
			},
			want: "VkSq83C7vQFvdX3MqHM7s-N1QOo2nae4F1iHmbV5pgg",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := GenerateChallengeID(tt.in); got != tt.want {
				t.Fatalf("GenerateChallengeID() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestGenerateChallengeIDGoldenVectors(t *testing.T) {
	t.Parallel()

	secret := "test-vector-secret"
	tests := []struct {
		name string
		in   GenerateChallengeIDInput
		want string
	}{
		{
			name: "required fields only",
			in:   GenerateChallengeIDInput{SecretKey: secret, Realm: "api.example.com", Method: "tempo", Intent: "charge", Request: map[string]any{"amount": "1000000"}},
			want: "X6v1eo7fJ76gAxqY0xN9Jd__4lUyDDYmriryOM-5FO4",
		},
		{
			name: "with expires",
			in:   GenerateChallengeIDInput{SecretKey: secret, Realm: "api.example.com", Method: "tempo", Intent: "charge", Request: map[string]any{"amount": "1000000"}, Expires: "2025-01-06T12:00:00Z"},
			want: "ChPX33RkKSZoSUyZcu8ai4hhkvjZJFkZVnvWs5s0iXI",
		},
		{
			name: "with digest",
			in:   GenerateChallengeIDInput{SecretKey: secret, Realm: "api.example.com", Method: "tempo", Intent: "charge", Request: map[string]any{"amount": "1000000"}, Digest: "sha-256=X48E9qOokqqrvdts8nOJRJN3OWDUoyWxBf7kbu9DBPE"},
			want: "JHB7EFsPVb-xsYCo8LHcOzeX1gfXWVoUSzQsZhKAfKM",
		},
		{
			name: "with expires and digest",
			in:   GenerateChallengeIDInput{SecretKey: secret, Realm: "api.example.com", Method: "tempo", Intent: "charge", Request: map[string]any{"amount": "1000000"}, Expires: "2025-01-06T12:00:00Z", Digest: "sha-256=X48E9qOokqqrvdts8nOJRJN3OWDUoyWxBf7kbu9DBPE"},
			want: "m39jbWWCIfmfJZSwCfvKFFtBl0Qwf9X4nOmDb21peLA",
		},
		{
			name: "multi field request",
			in:   GenerateChallengeIDInput{SecretKey: secret, Realm: "api.example.com", Method: "tempo", Intent: "charge", Request: map[string]any{"amount": "1000000", "currency": "0x1234", "recipient": "0xabcd"}},
			want: "_H5TOnnlW0zduQ5OhQ3EyLVze_TqxLDPda2CGZPZxOc",
		},
		{
			name: "nested methodDetails",
			in:   GenerateChallengeIDInput{SecretKey: secret, Realm: "api.example.com", Method: "tempo", Intent: "charge", Request: map[string]any{"amount": "1000000", "currency": "0x1234", "methodDetails": map[string]any{"chainId": 42431}}},
			want: "TqujwpuDDg_zsWGINAd5XObO2rRe6uYufpqvtDmr6N8",
		},
		{
			name: "empty request",
			in:   GenerateChallengeIDInput{SecretKey: secret, Realm: "api.example.com", Method: "tempo", Intent: "charge", Request: map[string]any{}},
			want: "yLN7yChAejW9WNmb54HpJIWpdb1WWXeA3_aCx4dxmkU",
		},
		{
			name: "different realm",
			in:   GenerateChallengeIDInput{SecretKey: secret, Realm: "payments.other.com", Method: "tempo", Intent: "charge", Request: map[string]any{"amount": "1000000"}},
			want: "3F5bOo2a9RUihdwKk4hGRvBvzQmVPBMDvW0YM-8GD00",
		},
		{
			name: "different method",
			in:   GenerateChallengeIDInput{SecretKey: secret, Realm: "api.example.com", Method: "stripe", Intent: "charge", Request: map[string]any{"amount": "1000000"}},
			want: "o0ra2sd7HcB4Ph0Vns69gRDUhSj5WNOnUopcDqKPLz4",
		},
		{
			name: "different intent",
			in:   GenerateChallengeIDInput{SecretKey: secret, Realm: "api.example.com", Method: "tempo", Intent: "session", Request: map[string]any{"amount": "1000000"}},
			want: "aAY7_IEDzsznNYplhOSE8cERQxvjFcT4Lcn-7FHjLVE",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := GenerateChallengeID(tt.in); got != tt.want {
				t.Fatalf("GenerateChallengeID() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestGenerateChallengeIDOpaqueGoldenVectors(t *testing.T) {
	t.Parallel()

	secret := "test-vector-secret"
	request := map[string]any{"amount": "1000000"}
	tests := []struct {
		name string
		in   GenerateChallengeIDInput
		want string
	}{
		{
			name: "with opaque",
			in:   GenerateChallengeIDInput{SecretKey: secret, Realm: "api.example.com", Method: "tempo", Intent: "charge", Request: request, Opaque: map[string]string{"pi": "pi_3abc123XYZ"}},
			want: "rxzKZ2qjXvinqCH96RORTZEPs1KXsA-0AUjrCAPFOWc",
		},
		{
			name: "with opaque and expires",
			in:   GenerateChallengeIDInput{SecretKey: secret, Realm: "api.example.com", Method: "tempo", Intent: "charge", Request: request, Expires: "2025-01-06T12:00:00Z", Opaque: map[string]string{"pi": "pi_3abc123XYZ"}},
			want: "KAfoMrA4fnzS1DPWN_cUv_b3_yHxCizdp6OhH7gluMY",
		},
		{
			name: "with empty opaque object",
			in:   GenerateChallengeIDInput{SecretKey: secret, Realm: "api.example.com", Method: "tempo", Intent: "charge", Request: request, Opaque: map[string]string{}},
			want: "vb4IyH-0LdJ3s7L0QAw8jIzcZkyxksPhIvEfmHmzA9k",
		},
		{
			name: "with multi key opaque",
			in: GenerateChallengeIDInput{SecretKey: secret, Realm: "api.example.com", Method: "tempo", Intent: "charge", Request: request, Opaque: map[string]string{
				"deposit": "dep_456",
				"pi":      "pi_3abc123XYZ",
			}},
			want: "aKskU8sadR5ZuFbUCsIwhO-ENxuVpTw17FdwHEXsJDk",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := GenerateChallengeID(tt.in); got != tt.want {
				t.Fatalf("GenerateChallengeID() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestEmptyOpaquePreservedOnWire(t *testing.T) {
	t.Parallel()

	challenge := NewChallenge(
		"test-vector-secret",
		"api.example.com",
		"tempo",
		"charge",
		map[string]any{"amount": "1000000"},
		WithMeta(map[string]string{}),
	)

	header := challenge.ToAuthenticate("api.example.com")
	if !strings.Contains(header, `opaque="`) {
		t.Fatalf("challenge header = %q, want opaque field", header)
	}

	parsed, err := ParseChallenge(header)
	if err != nil {
		t.Fatalf("ParseChallenge() error = %v", err)
	}
	if parsed.Opaque == nil {
		t.Fatal("parsed.Opaque = nil, want empty map")
	}
	if got := GenerateChallengeID(GenerateChallengeIDInput{
		SecretKey: "test-vector-secret",
		Realm:     "api.example.com",
		Method:    "tempo",
		Intent:    "charge",
		Request:   map[string]any{"amount": "1000000"},
		Opaque:    parsed.Opaque,
	}); got != challenge.ID {
		t.Fatalf("GenerateChallengeID(parsed opaque) = %q, want %q", got, challenge.ID)
	}

	credential := &Credential{Challenge: challenge.ToEcho(), Payload: map[string]any{"type": "hash", "hash": "0xabc123"}}
	authorization := credential.ToAuthorization()
	if !strings.Contains(authorization, "ey") {
		t.Fatalf("credential authorization = %q, want base64 payload", authorization)
	}
	parsedCredential, err := ParseCredential(authorization)
	if err != nil {
		t.Fatalf("ParseCredential() error = %v", err)
	}
	if parsedCredential.Challenge.Opaque == nil {
		t.Fatal("parsedCredential.Challenge.Opaque = nil, want empty map")
	}
}
