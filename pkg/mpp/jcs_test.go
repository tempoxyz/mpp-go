package mpp

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestChallengeBoundValuesUseJCS(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		data map[string]any
		want string
	}{
		{
			name: "unicode strings",
			data: map[string]any{
				"description": "Payment for café ☕",
				"amount":      "1000000",
			},
			want: "eyJhbW91bnQiOiIxMDAwMDAwIiwiZGVzY3JpcHRpb24iOiJQYXltZW50IGZvciBjYWbDqSDimJUifQ",
		},
		{
			name: "UTF-16 key ordering",
			data: map[string]any{
				"\U00010000": "supplementary",
				"\ue000":     "private",
			},
			want: "eyLwkICAIjoic3VwcGxlbWVudGFyeSIsIu6AgCI6InByaXZhdGUifQ",
		},
		{
			name: "ECMAScript number formatting",
			data: map[string]any{
				"integer":      333333333.33333329,
				"large":        1e21,
				"negativeZero": -0.0,
				"scientific":   1e-7,
				"small":        1e-6,
			},
			want: "eyJpbnRlZ2VyIjozMzMzMzMzMzMuMzMzMzMzMywibGFyZ2UiOjFlKzIxLCJuZWdhdGl2ZVplcm8iOjAsInNjaWVudGlmaWMiOjFlLTcsInNtYWxsIjowLjAwMDAwMX0",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, b64EncodeRequest(tt.data))
		})
	}
}

func TestChallengeBoundJSONEqualUsesJCSNumberSemantics(t *testing.T) {
	t.Parallel()

	assert.True(t,
		ChallengeBoundJSONEqual(
			map[string]any{"amount": int64(1234567890123456789)},
			map[string]any{"amount": json.Number("1234567890123456800")},
		),
	)
	assert.False(t,
		ChallengeBoundJSONEqual(
			map[string]any{"amount": "100"},
			map[string]any{"amount": "101"},
		),
	)
}

func TestChallengeWireAndIDUseSameJCSValues(t *testing.T) {
	t.Parallel()

	const (
		requestB64 = "eyJhbW91bnQiOiIxMDAwMDAwIiwiZGVzY3JpcHRpb24iOiJQYXltZW50IGZvciBjYWbDqSDimJUifQ"
		opaqueB64  = "eyLwkICAIjoic3VwcGxlbWVudGFyeSIsIu6AgCI6InByaXZhdGUifQ"
	)
	request := map[string]any{
		"description": "Payment for café ☕",
		"amount":      "1000000",
	}
	opaque := map[string]string{
		"\U00010000": "supplementary",
		"\ue000":     "private",
	}
	challenge := NewChallenge(
		"test-secret",
		"api.example.com",
		"tempo",
		"charge",
		request,
		WithMeta(opaque),
		WithHeader("Payment-Authorization"),
	)

	assert.Equal(t,
		"5ICqnT2yXt8jNyNF013hXjilUbs6zxFDO4jjYfsRi3w",
		challenge.ID,
	)
	header := challenge.ToAuthenticate(challenge.Realm)
	assert.Contains(t, header, `request="`+requestB64+`"`)
	assert.Contains(t, header, `opaque="`+opaqueB64+`"`)
	assert.Equal(t, requestB64, challenge.ToEcho().Request)

	parsed, err := ParseChallenge(header)
	if !assert.NoError(t, err) {
		return
	}
	assert.True(t, parsed.Verify("test-secret", "api.example.com"))
	assert.Equal(t, header, parsed.ToAuthenticate(parsed.Realm))

	credential := &Credential{
		Challenge: challenge.ToEcho(),
		Payload:   map[string]any{"type": "hash"},
	}
	parsedCredential, err := ParseCredential(credential.ToAuthorization())
	if !assert.NoError(t, err) {
		return
	}
	echoedRequest, err := B64Decode(parsedCredential.Challenge.Request)
	if !assert.NoError(t, err) {
		return
	}
	reconstructed := NewChallenge(
		"test-secret",
		parsedCredential.Challenge.Realm,
		parsedCredential.Challenge.Method,
		parsedCredential.Challenge.Intent,
		echoedRequest,
		WithMeta(parsedCredential.Challenge.Opaque),
		WithHeader(parsedCredential.Challenge.Header),
	)
	assert.Equal(t, challenge.ID, reconstructed.ID)
}
