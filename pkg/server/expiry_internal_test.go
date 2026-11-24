package server

import (
	"testing"
	"time"

	"github.com/tempoxyz/mpp-go/pkg/mpp"

	"github.com/stretchr/testify/assert"
)

func TestChallengeExpiredAtExpiryInstant(t *testing.T) {
	t.Parallel()

	expires, err := mpp.ParseExpires("2026-10-21T00:00:00.500Z")
	assert.NoError(t, err)
	for _, tc := range []struct {
		name string
		now  time.Time
		want bool
	}{
		{"before", expires.Add(-time.Nanosecond), false},
		{"at expiry", expires, true},
		{"after", expires.Add(time.Nanosecond), true},
	} {
		assert.Equal(t, tc.want, challengeExpired(expires, tc.now), tc.name)
	}
}

// graceTestIntent grants every credential a minute of grace.
type graceTestIntent struct{ Intent }

func (graceTestIntent) ExpiryGrace(*mpp.Credential) time.Duration { return time.Minute }

// An ExpiryGraceIntent's grace applies only to a challenge carrying the
// HMAC-bound expiry grace marker.
func TestCredentialExpiredGraceRequiresMarker(t *testing.T) {
	t.Parallel()

	expires := time.Now().Add(-time.Second)
	marked := &mpp.Credential{Challenge: mpp.ChallengeEcho{Opaque: map[string]string{expiryGraceKey: expiryGraceValue}}}
	unmarked := &mpp.Credential{Challenge: mpp.ChallengeEcho{Opaque: map[string]string{"plan": "pro"}}}
	intent := graceTestIntent{}
	assert.False(t, credentialExpired(intent, marked, expires, time.Now()))
	assert.True(t, credentialExpired(intent, unmarked, expires, time.Now()))
	assert.True(t, credentialExpired(intent, marked, expires.Add(-time.Minute), time.Now()))
}

// Server metadata can't set the expiry grace marker, and application
// metadata comparisons ignore it.
func TestExpiryGraceKeyReserved(t *testing.T) {
	t.Parallel()

	_, err := VerifyOrChallenge(t.Context(), VerifyParams{SecretKey: "verify-test-secret-key-of-at-least-32-bytes", Meta: map[string]string{expiryGraceKey: "1"}})
	assert.ErrorContains(t, err, "_mppx_expiry_grace is reserved")
	assert.Equal(t, map[string]string{"plan": "pro"}, applicationMeta(map[string]string{"plan": "pro", expiryGraceKey: "1"}))
}
