package mpp

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestChallengeJSONMarshalUsesDecodedRequest(t *testing.T) {
	t.Parallel()

	challenge := Challenge{
		ID:         "challenge-id",
		Realm:      "api.example.com",
		Method:     "tempo",
		Intent:     "charge",
		RequestB64: b64EncodeAny(map[string]any{"amount": "100", "currency": "0xabc"}),
	}

	encoded, err := json.Marshal(challenge)
	if err != nil {
		t.Fatalf("json.Marshal(challenge) error = %v", err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("json.Unmarshal(encoded) error = %v", err)
	}

	if _, ok := decoded["requestB64"]; ok {
		t.Fatal("challenge JSON unexpectedly included requestB64")
	}
	request, ok := decoded["request"].(map[string]any)
	if !ok {
		t.Fatalf("challenge JSON request = %#v, want object", decoded["request"])
	}
	if request["amount"] != "100" {
		t.Fatalf("challenge JSON request[amount] = %#v, want %q", request["amount"], "100")
	}
}

func TestChallengeJSONUnmarshalNormalizesRequest(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		input   string
		wantB64 string
		wantErr string
	}{
		{
			name:    "request object only",
			input:   `{"id":"challenge-id","realm":"api.example.com","method":"tempo","intent":"charge","request":{"amount":"100","currency":"0xabc"}}`,
			wantB64: b64EncodeAny(map[string]any{"amount": "100", "currency": "0xabc"}),
		},
		{
			name:    "requestB64 only",
			input:   `{"id":"challenge-id","realm":"api.example.com","method":"tempo","intent":"charge","requestB64":"` + b64EncodeAny(map[string]any{"amount": "100"}) + `"}`,
			wantB64: b64EncodeAny(map[string]any{"amount": "100"}),
		},
		{
			name:    "matching request and requestB64",
			input:   `{"id":"challenge-id","realm":"api.example.com","method":"tempo","intent":"charge","request":{"amount":"100"},"requestB64":"` + b64EncodeAny(map[string]any{"amount": "100"}) + `"}`,
			wantB64: b64EncodeAny(map[string]any{"amount": "100"}),
		},
		{
			name:    "mismatched request and requestB64",
			input:   `{"id":"challenge-id","realm":"api.example.com","method":"tempo","intent":"charge","request":{"amount":"100"},"requestB64":"` + b64EncodeAny(map[string]any{"amount": "999"}) + `"}`,
			wantErr: "request and requestB64 do not match",
		},
		{
			name:    "invalid requestB64",
			input:   `{"id":"challenge-id","realm":"api.example.com","method":"tempo","intent":"charge","requestB64":"not-base64"}`,
			wantErr: "invalid request encoding",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var challenge Challenge
			err := json.Unmarshal([]byte(tt.input), &challenge)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("json.Unmarshal(challenge) error = %v, want substring %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("json.Unmarshal(challenge) error = %v", err)
			}

			if challenge.RequestB64 != tt.wantB64 {
				t.Fatalf("challenge.RequestB64 = %q, want %q", challenge.RequestB64, tt.wantB64)
			}
			decoded, err := B64Decode(challenge.RequestB64)
			if err != nil {
				t.Fatalf("B64Decode(challenge.RequestB64) error = %v", err)
			}
			if !JSONEqual(challenge.Request, decoded) {
				t.Fatalf("challenge.Request = %#v, want %#v", challenge.Request, decoded)
			}
		})
	}
}

func TestCredentialJSONMarshalUsesDecodedChallengeRequest(t *testing.T) {
	t.Parallel()

	credential := Credential{
		Challenge: ChallengeEcho{
			ID:      "challenge-id",
			Realm:   "api.example.com",
			Method:  "tempo",
			Intent:  "charge",
			Request: b64EncodeAny(map[string]any{"amount": "100", "currency": "0xabc"}),
			Opaque:  map[string]string{"trace": "123"},
		},
		Payload: map[string]any{"type": "hash", "hash": "0xabc123"},
		Source:  "did:pkh:eip155:42431:0x1234",
	}

	encoded, err := json.Marshal(credential)
	if err != nil {
		t.Fatalf("json.Marshal(credential) error = %v", err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("json.Unmarshal(encoded) error = %v", err)
	}

	challenge, ok := decoded["challenge"].(map[string]any)
	if !ok {
		t.Fatalf("credential JSON challenge = %#v, want object", decoded["challenge"])
	}
	request, ok := challenge["request"].(map[string]any)
	if !ok {
		t.Fatalf("credential JSON challenge.request = %#v, want object", challenge["request"])
	}
	if request["amount"] != "100" {
		t.Fatalf("credential JSON challenge.request[amount] = %#v, want %q", request["amount"], "100")
	}
	if decoded["source"] != credential.Source {
		t.Fatalf("credential JSON source = %#v, want %q", decoded["source"], credential.Source)
	}
}

func TestCredentialJSONUnmarshalNormalizesChallengeRequest(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		input   string
		wantB64 string
		wantErr string
	}{
		{
			name:    "request object",
			input:   `{"challenge":{"id":"challenge-id","realm":"api.example.com","method":"tempo","intent":"charge","request":{"amount":"100"}},"payload":{"type":"hash","hash":"0xabc123"}}`,
			wantB64: b64EncodeAny(map[string]any{"amount": "100"}),
		},
		{
			name:    "request base64 string",
			input:   `{"challenge":{"id":"challenge-id","realm":"api.example.com","method":"tempo","intent":"charge","request":"` + b64EncodeAny(map[string]any{"amount": "100"}) + `"},"payload":{"type":"hash","hash":"0xabc123"}}`,
			wantB64: b64EncodeAny(map[string]any{"amount": "100"}),
		},
		{
			name:    "invalid request base64 string",
			input:   `{"challenge":{"id":"challenge-id","realm":"api.example.com","method":"tempo","intent":"charge","request":"not-base64"},"payload":{"type":"hash","hash":"0xabc123"}}`,
			wantErr: "invalid request encoding",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var credential Credential
			err := json.Unmarshal([]byte(tt.input), &credential)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("json.Unmarshal(credential) error = %v, want substring %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("json.Unmarshal(credential) error = %v", err)
			}

			if credential.Challenge.Request != tt.wantB64 {
				t.Fatalf("credential.Challenge.Request = %q, want %q", credential.Challenge.Request, tt.wantB64)
			}
			decoded, err := B64Decode(credential.Challenge.Request)
			if err != nil {
				t.Fatalf("B64Decode(credential.Challenge.Request) error = %v", err)
			}
			if decoded["amount"] != "100" {
				t.Fatalf("decoded request[amount] = %#v, want %q", decoded["amount"], "100")
			}
		})
	}
}
