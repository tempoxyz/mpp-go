package mpp

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// nonCanonicalOpaque is valid string-only opaque JSON that is not in RFC 8785
// canonical form: it has extra whitespace and unsorted members.
const nonCanonicalOpaque = `{ "z": "last", "a": "first" }`

func challengeHeaderWithOpaque(t *testing.T, opaqueJSON string) (string, string) {
	t.Helper()
	raw := base64.RawURLEncoding.EncodeToString([]byte(opaqueJSON))
	header := `Payment id="test", realm="merchant.example", method="test", intent="charge", request="e30", opaque="` + raw + `"`
	return header, raw
}

func credentialChallengeOpaque(t *testing.T, header string) any {
	t.Helper()
	encoded, ok := strings.CutPrefix(header, SchemePayment+" ")
	require.True(t, ok)
	decoded, err := base64.RawURLEncoding.DecodeString(encoded)
	require.NoError(t, err)
	var body struct {
		Challenge map[string]any `json:"challenge"`
	}
	require.NoError(t, json.Unmarshal(decoded, &body))
	return body.Challenge["opaque"]
}

func TestParseChallengePreservesOpaqueEncoding(t *testing.T) {
	for _, opaqueJSON := range []string{
		nonCanonicalOpaque,
		`{"z":"last","a":"first"}`,
	} {
		header, raw := challengeHeaderWithOpaque(t, opaqueJSON)

		challenge, err := ParseChallenge(header)
		require.NoError(t, err)
		assert.Equal(t, map[string]string{"a": "first", "z": "last"}, challenge.Opaque)

		formatted := formatAuthenticateUnchecked(challenge, challenge.Realm)
		assert.Contains(t, formatted, `opaque="`+raw+`"`,
			"the issuer's opaque encoding must be echoed unchanged for %s", opaqueJSON)
	}
}

func TestCredentialEchoPreservesOpaqueEncoding(t *testing.T) {
	header, raw := challengeHeaderWithOpaque(t, nonCanonicalOpaque)
	challenge, err := ParseChallenge(header)
	require.NoError(t, err)

	authorization := formatAuthorizationUnchecked(&Credential{
		Challenge: challenge.ToEcho(),
		Payload:   map[string]any{"type": "test"},
	})
	assert.Equal(t, raw, credentialChallengeOpaque(t, authorization),
		"a credential must echo the challenge's opaque value unchanged")

	credential, err := ParseCredential(authorization)
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"a": "first", "z": "last"}, credential.Challenge.Opaque)
	assert.Equal(t, raw, credentialChallengeOpaque(t, formatAuthorizationUnchecked(credential)),
		"re-formatting a parsed credential must keep the echoed opaque value unchanged")
}

func TestFormatAuthenticateReencodesModifiedOpaque(t *testing.T) {
	header, raw := challengeHeaderWithOpaque(t, nonCanonicalOpaque)
	challenge, err := ParseChallenge(header)
	require.NoError(t, err)

	challenge.Opaque["a"] = "changed"

	formatted := formatAuthenticateUnchecked(challenge, challenge.Realm)
	assert.NotContains(t, formatted, `opaque="`+raw+`"`,
		"a stale encoding must not be emitted after the decoded opaque changes")
	assert.Contains(t, formatted, `opaque="`+canonicalOpaque(t, challenge.Opaque)+`"`)
}

func TestIssuedChallengeWithMetaVerifiesAfterWireRoundTrip(t *testing.T) {
	const (
		secret = "0123456789abcdef0123456789abcdef"
		realm  = "api"
	)
	meta := map[string]string{"z": "last", "a": "first"}
	issued := NewChallenge(secret, realm, "tempo", "charge", map[string]any{"amount": "1"}, WithMeta(meta))

	formatted := issued.ToAuthenticate(realm)
	assert.Contains(t, formatted, `opaque="`+canonicalOpaque(t, meta)+`"`)

	parsed, err := ParseChallenge(formatted)
	require.NoError(t, err)
	assert.Equal(t, formatted, parsed.ToAuthenticate(realm))
	assert.True(t, parsed.verify(secret, realm))
}

func TestJSONRoundTripPreservesOpaqueEncoding(t *testing.T) {
	header, raw := challengeHeaderWithOpaque(t, nonCanonicalOpaque)
	parsed, err := ParseChallenge(header)
	require.NoError(t, err)

	data, err := json.Marshal(parsed)
	require.NoError(t, err)
	var challenge Challenge
	require.NoError(t, json.Unmarshal(data, &challenge))
	assert.Equal(t, map[string]string{"a": "first", "z": "last"}, challenge.Opaque)
	assert.Contains(t, formatAuthenticateUnchecked(&challenge, challenge.Realm), `opaque="`+raw+`"`)

	credential := challenge.NewCredential(map[string]any{"type": "test"})
	data, err = json.Marshal(credential)
	require.NoError(t, err)
	var decoded Credential
	require.NoError(t, json.Unmarshal(data, &decoded))
	assert.Equal(t, raw, credentialChallengeOpaque(t, formatAuthorizationUnchecked(&decoded)),
		"a credential must echo the issuer's opaque after a JSON round trip")
}

func TestJSONKeepsCanonicalOpaqueAsObject(t *testing.T) {
	header, _ := challengeHeaderWithOpaque(t, `{"a":"first","z":"last"}`)
	parsed, err := ParseChallenge(header)
	require.NoError(t, err)
	data, err := json.Marshal(parsed)
	require.NoError(t, err)
	assert.Contains(t, string(data), `"opaque":{"a":"first","z":"last"}`)
}

func TestVerifyUsesPreservedOpaqueEncoding(t *testing.T) {
	const (
		secret = "0123456789abcdef0123456789abcdef"
		realm  = "merchant.example"
	)
	raw := base64.RawURLEncoding.EncodeToString([]byte(nonCanonicalOpaque))
	// An mppx issuer binds the challenge ID to the opaque string it sends.
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(realm + "|tempo|charge|e30|||" + raw))
	id := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	header := `Payment id="` + id + `", realm="` + realm + `", method="tempo", intent="charge", request="e30", opaque="` + raw + `"`

	challenge, err := ParseChallenge(header)
	require.NoError(t, err)
	assert.True(t, challenge.verify(secret, realm))

	challenge.Opaque["a"] = "changed"
	assert.False(t, challenge.verify(secret, realm), "a modified opaque must not verify")
}

func TestOpaqueWithRawKeyIsEchoed(t *testing.T) {
	header, raw := challengeHeaderWithOpaque(t, `{"_raw":"abc"}`)
	challenge, err := ParseChallenge(header)
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"_raw": "abc"}, challenge.Opaque)

	authorization := formatAuthorizationUnchecked(challenge.NewCredential(map[string]any{"type": "test"}))
	assert.Equal(t, raw, credentialChallengeOpaque(t, authorization),
		"an issuer's _raw key must not be taken for the undecodable-opaque sentinel")
}

func canonicalOpaque(t *testing.T, m map[string]string) string {
	t.Helper()
	encoded, err := b64EncodeSortedStringMap(m)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}
