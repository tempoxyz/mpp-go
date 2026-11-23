package mpp

import (
	"encoding/base64"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseCredentialRejectsNonStringRequest(t *testing.T) {
	token := base64.RawURLEncoding.EncodeToString([]byte(`{"challenge":{"id":"i","realm":"r","method":"a","intent":"a","request":{"amount":"1"}},"payload":{}}`))
	_, err := ParseCredential("Payment " + token)
	require.Error(t, err)
}

func TestParseCredentialRequiresSingleToken(t *testing.T) {
	token := base64.RawURLEncoding.EncodeToString([]byte(`{"challenge":{"id":"i","realm":"r","method":"a","intent":"a","request":"e30"},"payload":{}}`))

	for _, header := range []string{
		"Payment " + token,
		"payment " + token,
		"Payment   " + token,
		" Payment " + token + " ",
	} {
		_, err := ParseCredential(header)
		require.NoError(t, err, "%q", header)
	}

	for _, header := range []string{
		"Payment " + token + `, Basic realm="x"`,
		"Basic xyz, Payment " + token,
		"Payment " + token + " trailing",
		"Payment " + token + ",",
		",Payment " + token,
		"Payment " + token[:4] + "\t" + token[4:],
		"Payment",
		"Payment ",
		"Payment\t" + token,
	} {
		_, err := ParseCredential(header)
		require.Error(t, err, "%q", header)
		require.NotContains(t, err.Error(), token[:16], "%q", header)
	}
}
