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
}
