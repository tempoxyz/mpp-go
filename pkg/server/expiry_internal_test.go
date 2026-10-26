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
