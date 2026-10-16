package mpp

import (
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

		formatted := FormatAuthenticate(challenge, challenge.Realm)
		assert.Contains(t, formatted, `opaque="`+raw+`"`,
			"the issuer's opaque encoding must be echoed unchanged for %s", opaqueJSON)
	}
}

func TestCredentialEchoPreservesOpaqueEncoding(t *testing.T) {
	header, raw := challengeHeaderWithOpaque(t, nonCanonicalOpaque)
	challenge, err := ParseChallenge(header)
	require.NoError(t, err)

	authorization := FormatAuthorization(&Credential{
		Challenge: challenge.ToEcho(),
		Payload:   map[string]any{"type": "test"},
	})
	assert.Equal(t, raw, credentialChallengeOpaque(t, authorization),
		"a credential must echo the challenge's opaque value unchanged")

	credential, err := ParseCredential(authorization)
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"a": "first", "z": "last"}, credential.Challenge.Opaque)
	assert.Equal(t, raw, credentialChallengeOpaque(t, FormatAuthorization(credential)),
		"re-formatting a parsed credential must keep the echoed opaque value unchanged")
}

func TestFormatAuthenticateReencodesModifiedOpaque(t *testing.T) {
	header, raw := challengeHeaderWithOpaque(t, nonCanonicalOpaque)
	challenge, err := ParseChallenge(header)
	require.NoError(t, err)

	challenge.Opaque["a"] = "changed"

	formatted := FormatAuthenticate(challenge, challenge.Realm)
	assert.NotContains(t, formatted, `opaque="`+raw+`"`,
		"a stale encoding must not be emitted after the decoded opaque changes")
	assert.Contains(t, formatted, `opaque="`+b64EncodeSortedStringMap(challenge.Opaque)+`"`)
}

func TestIssuedChallengeWithMetaVerifiesAfterWireRoundTrip(t *testing.T) {
	const (
		secret = "0123456789abcdef0123456789abcdef"
		realm  = "api"
	)
	meta := map[string]string{"z": "last", "a": "first"}
	issued := NewChallenge(secret, realm, "tempo", "charge", map[string]any{"amount": "1"}, WithMeta(meta))

	formatted := issued.ToAuthenticate(realm)
	assert.Contains(t, formatted, `opaque="`+b64EncodeSortedStringMap(meta)+`"`)

	parsed, err := ParseChallenge(formatted)
	require.NoError(t, err)
	assert.Equal(t, formatted, parsed.ToAuthenticate(realm))
	assert.True(t, parsed.Verify(secret, realm))
}
