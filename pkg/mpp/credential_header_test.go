package mpp

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const credentialHeaderTestChallenge = `Payment id="abc", realm="api.example.com", method="tempo", intent="charge", request="e30"`

func TestParseChallengeValidatesRawCredentialHeader(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		param      string
		wantHeader string
		wantErr    bool
	}{
		{name: "absent", param: "", wantHeader: ""},
		{name: "Payment-Authorization", param: `, header="Payment-Authorization"`, wantHeader: HeaderPaymentAuthorization},
		{name: "lowercase payment-authorization", param: `, header="payment-authorization"`, wantHeader: HeaderPaymentAuthorization},
		{name: "token form", param: `, header=Payment-Authorization`, wantHeader: HeaderPaymentAuthorization},
		{name: "explicit Authorization", param: `, header="Authorization"`, wantErr: true},
		{name: "empty", param: `, header=""`, wantErr: true},
		{name: "X-Payment", param: `, header="X-Payment"`, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			challenge, err := ParseChallenge(credentialHeaderTestChallenge + tt.param)
			if tt.wantErr {
				require.ErrorIs(t, err, ErrUnsupportedCredentialHeader)
				assert.Nil(t, challenge)
				assert.NotContains(t, err.Error(), "X-Payment", "the raw value is not echoed")
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantHeader, challenge.Header)
			want := HeaderAuthorization
			if tt.wantHeader != "" {
				want = HeaderPaymentAuthorization
			}
			assert.Equal(t, want, challenge.CredentialHeader())
			if tt.wantHeader != "" {
				// The credential echoes the canonical header.
				credential := challenge.NewCredential(map[string]any{})
				assert.Equal(t, HeaderPaymentAuthorization, credential.Challenge.Header)
			}
		})
	}
}

func TestChallengeUnmarshalJSONValidatesRawCredentialHeader(t *testing.T) {
	t.Parallel()

	base := `"id":"abc","realm":"api.example.com","method":"tempo","intent":"charge","request":{}`
	tests := []struct {
		name       string
		member     string
		wantHeader string
		wantErr    bool
	}{
		{name: "absent", member: ""},
		{name: "Payment-Authorization", member: `,"header":"Payment-Authorization"`, wantHeader: HeaderPaymentAuthorization},
		{name: "lowercase", member: `,"header":"payment-authorization"`, wantHeader: HeaderPaymentAuthorization},
		{name: "Authorization", member: `,"header":"Authorization"`, wantErr: true},
		{name: "empty", member: `,"header":""`, wantErr: true},
		{name: "X-Payment", member: `,"header":"X-Payment"`, wantErr: true},
		{name: "not a string", member: `,"header":1`, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var challenge Challenge
			err := json.Unmarshal([]byte("{"+base+tt.member+"}"), &challenge)
			if tt.wantErr {
				require.ErrorIs(t, err, ErrUnsupportedCredentialHeader)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantHeader, challenge.Header)
		})
	}
}

func TestParseCredentialValidatesEchoedCredentialHeader(t *testing.T) {
	t.Parallel()

	credential := func(header any) string {
		echo := map[string]any{
			"id":      "abc",
			"realm":   "api.example.com",
			"method":  "tempo",
			"intent":  "charge",
			"request": "e30",
		}
		if header != nil {
			echo["header"] = header
		}
		raw, err := json.Marshal(map[string]any{"challenge": echo, "payload": map[string]any{}})
		require.NoError(t, err)
		return SchemePayment + " " + base64.RawURLEncoding.EncodeToString(raw)
	}
	tests := []struct {
		name       string
		header     any
		wantHeader string
		wantErr    bool
	}{
		{name: "absent", header: nil},
		{name: "Payment-Authorization", header: "Payment-Authorization", wantHeader: HeaderPaymentAuthorization},
		{name: "lowercase", header: "payment-authorization", wantHeader: HeaderPaymentAuthorization},
		{name: "Authorization", header: "Authorization", wantErr: true},
		{name: "empty", header: "", wantErr: true},
		{name: "X-Payment", header: "X-Payment", wantErr: true},
		{name: "not a string", header: 1, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			parsed, err := ParseCredential(credential(tt.header))
			if tt.wantErr {
				require.ErrorIs(t, err, ErrUnsupportedCredentialHeader)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantHeader, parsed.Challenge.Header)
		})
	}
}

func TestChallengeConstructionCredentialHeader(t *testing.T) {
	t.Parallel()

	params := map[string]any{"amount": "1000000"}
	for _, header := range []string{"", "Authorization", "authorization", "AUTHORIZATION"} {
		challenge, err := NewChallengeWithError("secret", "api.example.com", "tempo", "charge", params, WithHeader(header))
		require.NoError(t, err, header)
		assert.Empty(t, challenge.Header, header)
		formatted, err := FormatChallenge(challenge)
		require.NoError(t, err, header)
		assert.NotContains(t, formatted, "header=", header)
	}

	for _, header := range []string{"Payment-Authorization", "payment-authorization"} {
		challenge, err := NewChallengeWithError("secret", "api.example.com", "tempo", "charge", params, WithHeader(header))
		require.NoError(t, err, header)
		assert.Equal(t, HeaderPaymentAuthorization, challenge.Header, header)
		formatted, err := FormatChallenge(challenge)
		require.NoError(t, err, header)
		assert.Contains(t, formatted, `header="Payment-Authorization"`, header)
		parsed, err := ParseChallenge(formatted)
		require.NoError(t, err, header)
		assert.True(t, parsed.verify("secret", "api.example.com"), header)
	}

	for _, header := range []string{"X-Payment", "Proxy-Authorization", "Payment Authorization"} {
		_, err := NewChallengeWithError("secret", "api.example.com", "tempo", "charge", params, WithHeader(header))
		require.ErrorIs(t, err, ErrUnsupportedCredentialHeader, header)

		_, err = FormatChallenge(&Challenge{
			ID:      "abc",
			Realm:   "api.example.com",
			Method:  "tempo",
			Intent:  "charge",
			Request: params,
			Header:  header,
		})
		require.ErrorIs(t, err, ErrUnsupportedCredentialHeader, header)
	}
}

func TestChallengesFromResponseSkipsUnsupportedCredentialHeader(t *testing.T) {
	t.Parallel()

	valid := newChallenge("secret", "api.example.com", "tempo", "charge", map[string]any{"amount": "1"})
	validHeader, err := FormatChallenge(valid)
	require.NoError(t, err)
	invalidHeader := `Payment id="bad", realm="api.example.com", method="tempo", intent="charge", request="e30", header="Authorization"`

	response := func(values ...string) *http.Response {
		resp := &http.Response{StatusCode: http.StatusPaymentRequired, Header: http.Header{}}
		for _, value := range values {
			resp.Header.Add(HeaderWWWAuthenticate, value)
		}
		return resp
	}

	challenges, err := ChallengesFromResponse(response(invalidHeader, validHeader))
	require.NoError(t, err)
	require.Len(t, challenges, 1)
	assert.Equal(t, valid.ID, challenges[0].ID)

	challenges, err = ChallengesFromResponse(response(invalidHeader + ", " + validHeader))
	require.NoError(t, err)
	require.Len(t, challenges, 1)
	assert.Equal(t, valid.ID, challenges[0].ID)

	_, err = ChallengesFromResponse(response(invalidHeader))
	require.ErrorIs(t, err, ErrUnsupportedCredentialHeader)

	// Other parse errors still fail the response.
	_, err = ChallengesFromResponse(response(`Payment id="bad", realm="api.example.com", method="tempo", intent="charge"`, validHeader))
	require.Error(t, err)
	assert.False(t, strings.Contains(err.Error(), "unsupported credential header"))
}

func TestMCPPaymentRequiredDataSkipsUnsupportedCredentialHeader(t *testing.T) {
	t.Parallel()

	valid := newChallenge("secret", "api.example.com", "tempo", "charge", map[string]any{"amount": "1"})
	validJSON, err := json.Marshal(valid)
	require.NoError(t, err)
	invalidJSON := `{"id":"bad","realm":"api.example.com","method":"tempo","intent":"charge","request":{},"header":"X-Payment"}`

	var data MCPPaymentRequiredData
	require.NoError(t, json.Unmarshal([]byte(`{"httpStatus":402,"challenges":[`+invalidJSON+`,`+string(validJSON)+`]}`), &data))
	assert.Equal(t, 402, data.HTTPStatus)
	require.Len(t, data.Challenges, 1)
	assert.Equal(t, valid.ID, data.Challenges[0].ID)

	data = MCPPaymentRequiredData{}
	require.NoError(t, json.Unmarshal([]byte(`{"httpStatus":402,"challenges":[`+invalidJSON+`]}`), &data))
	assert.Empty(t, data.Challenges)

	// Other invalid challenges still fail the decode.
	err = json.Unmarshal([]byte(`{"httpStatus":402,"challenges":[{"id":1,"realm":"api.example.com","method":"tempo","intent":"charge","request":{}}]}`), &data)
	assert.Error(t, err)
}
