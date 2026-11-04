package tempoproof

import (
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/tempoxyz/mpp-go/pkg/mpp"
)

func TestIsProofChallenge(t *testing.T) {
	t.Parallel()
	for amount, want := range map[any]bool{"0": true, "000": true, " 0 ": true, "1": false, "-0": false, "0.0": false, "": false, nil: false} {
		challenge := &mpp.Challenge{Method: "tempo", Intent: mpp.IntentCharge, Request: map[string]any{"amount": amount}}
		if amount == nil {
			challenge.Request = map[string]any{}
		}
		assert.Equalf(t, want, IsProofChallenge(challenge), "amount %#v", amount)
	}
	assert.False(t, IsProofChallenge(&mpp.Challenge{Method: "stripe", Intent: mpp.IntentCharge, Request: map[string]any{"amount": "0"}}))
	assert.False(t, IsProofChallenge(nil))
}

func TestOriginOwnsRealm(t *testing.T) {
	t.Parallel()
	for raw, want := range map[string]bool{
		"https://API.example.com/x":    true,
		"https://api.example.com:443":  true,
		"https://api.example.com:8443": false,
		"http://api.example.com":       false,
		"https://other.example.com":    false,
	} {
		u, err := url.Parse(raw)
		assert.NoError(t, err)
		assert.Equalf(t, want, OriginOwnsRealm(u, "api.example.com"), raw)
	}
	assert.False(t, OriginOwnsRealm(nil, "api.example.com"))
	assert.Equal(t, "https://api.example.com:8443", NormalizeOrigin(" HTTPS://API.example.com:8443/ "))
	assert.Equal(t, "https://api.example.com", NormalizeOrigin("https://api.example.com:443/"))
	assert.Equal(t, "http://api.example.com", NormalizeOrigin("HTTP://api.example.com:80"))
	assert.Equal(t, "https://api.example.com", Origin(&url.URL{Scheme: "HTTPS", Host: "API.example.com:443"}))
	assert.Equal(t, "https://[::1]", Origin(&url.URL{Scheme: "https", Host: "[::1]:443"}))
}

func TestOriginOwnsRealmNonASCII(t *testing.T) {
	for _, tc := range []struct {
		host, realm string
		want        bool
	}{
		{"σ.example", "ς.example", false},
		{"ς.example", "σ.example", false},
		// Non-ASCII hostnames and realms never own a realm; only the xn--
		// form does, as with mppx's WHATWG URL hostname.
		{"bücher.example", "xn--bcher-kva.example", false},
		{"xn--bcher-kva.example", "bücher.example", false},
		{"bücher.example", "bücher.example", false},
		{"xn--bcher-kva.example", "XN--BCHER-KVA.example", true},
		{"Key.example", "key.example", false}, // Kelvin sign
		{"key.example", "Key.example", false},
		{"API.example", "api.example", true},
		{"bad_host.example", "bad_host.example", true},
		{"r3---sn-abc.example.com", "r3---sn-abc.example.com", true},
		{"[::1]", "::1", true},
		{"[::1]", "::2", false},
		{"", "", false},
	} {
		t.Run(tc.host+"/"+tc.realm, func(t *testing.T) {
			assert.Equal(t, tc.want, OriginOwnsRealm(&url.URL{Scheme: "https", Host: tc.host}, tc.realm))
		})
	}
}
