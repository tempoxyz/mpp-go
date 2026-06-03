package mpp

import (
	"encoding/json"
	"github.com/stretchr/testify/assert"
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
	if !assert.NoErrorf(t, err,
		"json.Marshal(challenge) error = %v", err) {
		return
	}

	var decoded map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		assert.Failf(t, "", "json.Unmarshal(encoded) error = %v", err)
		return
	}

	if _, ok := decoded["requestB64"]; ok {
		assert.Fail(t, "challenge JSON unexpectedly included requestB64")
		return
	}
	request, ok := decoded["request"].(map[string]any)
	if !assert.Truef(t, ok,
		"challenge JSON request = %#v, want object", decoded["request"]) {
		return
	}
	if !assert.Equalf(t, "100", request["amount"],
		"challenge JSON request[amount] = %#v, want %q", request["amount"], "100") {
		return
	}

}

func TestChallengeJSONOpaqueRoundTrip(t *testing.T) {
	t.Parallel()

	rawOpaque := b64EncodeAny(map[string]any{"trace": "123"})
	challenge := Challenge{
		ID:         "challenge-id",
		Realm:      "api.example.com",
		Method:     "tempo",
		Intent:     "charge",
		RequestB64: b64EncodeAny(map[string]any{"amount": "100"}),
		Opaque:     map[string]string{"_raw": rawOpaque},
	}

	encoded, err := json.Marshal(challenge)
	if !assert.NoErrorf(t, err,
		"json.Marshal(challenge) error = %v", err) {
		return
	}

	var decoded map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		assert.Failf(t, "", "json.Unmarshal(encoded) error = %v", err)
		return
	}

	opaque, ok := decoded["opaque"].(map[string]any)
	if !assert.Truef(t, ok,
		"challenge JSON opaque = %#v, want object", decoded["opaque"]) {
		return
	}
	if !assert.Equalf(t, "123", opaque["trace"],
		"challenge JSON opaque[trace] = %#v, want %q", opaque["trace"], "123") {
		return
	}

	var roundTripped Challenge
	if err := json.Unmarshal([]byte(`{"id":"challenge-id","realm":"api.example.com","method":"tempo","intent":"charge","request":{"amount":"100"},"opaque":"`+rawOpaque+`"}`), &roundTripped); err != nil {
		assert.Failf(t, "", "json.Unmarshal(challenge) error = %v", err)
		return
	}
	if !assert.Equalf(t, "123", roundTripped.Opaque["trace"],
		"challenge.Opaque[trace] = %q, want %q", roundTripped.Opaque["trace"], "123") {
		return
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
				if !assert.Falsef(t, err == nil || !strings.Contains(err.Error(), tt.wantErr),
					"json.Unmarshal(challenge) error = %v, want substring %q", err, tt.wantErr) {
					return
				}

				return
			}
			if !assert.NoErrorf(t, err,
				"json.Unmarshal(challenge) error = %v", err) {
				return
			}
			if !assert.Equalf(t, tt.wantB64, challenge.RequestB64,
				"challenge.RequestB64 = %q, want %q", challenge.RequestB64, tt.wantB64) {
				return
			}

			decoded, err := B64Decode(challenge.RequestB64)
			if !assert.NoErrorf(t, err,
				"B64Decode(challenge.RequestB64) error = %v", err) {
				return
			}
			if !assert.Truef(t, JSONEqual(challenge.Request, decoded),
				"challenge.Request = %#v, want %#v", challenge.Request, decoded) {
				return
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
	if !assert.NoErrorf(t, err,
		"json.Marshal(credential) error = %v", err) {
		return
	}

	var decoded map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		assert.Failf(t, "", "json.Unmarshal(encoded) error = %v", err)
		return
	}

	challenge, ok := decoded["challenge"].(map[string]any)
	if !assert.Truef(t, ok,
		"credential JSON challenge = %#v, want object", decoded["challenge"]) {
		return
	}

	if _, ok := challenge["requestB64"]; ok {
		assert.Fail(t, "credential JSON challenge unexpectedly included requestB64")
		return
	}
	if _, ok := challenge["description"]; ok {
		assert.Fail(t, "credential JSON challenge unexpectedly included description")
		return
	}
	request, ok := challenge["request"].(map[string]any)
	if !assert.Truef(t, ok,
		"credential JSON challenge.request = %#v, want object", challenge["request"]) {
		return
	}
	if !assert.Equalf(t, "100", request["amount"],
		"credential JSON challenge.request[amount] = %#v, want %q", request["amount"], "100") {
		return
	}
	if !assert.Equalf(t, credential.Source, decoded["source"],
		"credential JSON source = %#v, want %q", decoded["source"], credential.Source) {
		return
	}

}

func TestCredentialJSONUnmarshalNormalizesChallengeOpaque(t *testing.T) {
	t.Parallel()

	rawOpaque := b64EncodeAny(map[string]any{"trace": "123"})
	input := `{"challenge":{"id":"challenge-id","realm":"api.example.com","method":"tempo","intent":"charge","request":{"amount":"100"},"opaque":"` + rawOpaque + `"},"payload":{"type":"hash","hash":"0xabc123"}}`

	var credential Credential
	if err := json.Unmarshal([]byte(input), &credential); err != nil {
		assert.Failf(t, "", "json.Unmarshal(credential) error = %v", err)
		return
	}
	if !assert.Equalf(t, "123", credential.Challenge.Opaque["trace"],
		"credential.Challenge.Opaque[trace] = %q, want %q", credential.Challenge.Opaque["trace"], "123") {
		return
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
				if !assert.Falsef(t, err == nil || !strings.Contains(err.Error(), tt.wantErr),
					"json.Unmarshal(credential) error = %v, want substring %q", err, tt.wantErr) {
					return
				}

				return
			}
			if !assert.NoErrorf(t, err,
				"json.Unmarshal(credential) error = %v", err) {
				return
			}
			if !assert.Equalf(t, tt.wantB64, credential.Challenge.Request,
				"credential.Challenge.Request = %q, want %q", credential.Challenge.Request, tt.wantB64) {
				return
			}

			decoded, err := B64Decode(credential.Challenge.Request)
			if !assert.NoErrorf(t, err,
				"B64Decode(credential.Challenge.Request) error = %v", err) {
				return
			}
			if !assert.Equalf(t, "100", decoded["amount"],
				"decoded request[amount] = %#v, want %q", decoded["amount"], "100") {
				return
			}

		})
	}
}
