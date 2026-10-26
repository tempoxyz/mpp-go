package mpp

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestComputeBodyDigest_Known(t *testing.T) {
	// SHA-256 of "hello" = LPJNul+wow4m6DsqxbninhsWHlwfp0JecwQzYpOLmCQ=, in
	// standard (not URL-safe) base64.
	assert.Equal(t, "sha-256=LPJNul+wow4m6DsqxbninhsWHlwfp0JecwQzYpOLmCQ=", ComputeBodyDigest([]byte("hello")))
}

func TestVerifyBodyDigest(t *testing.T) {
	body := []byte("test body")
	digest := ComputeBodyDigest(body)
	assert.True(t, VerifyBodyDigest(digest, body),
		"verify should return true for matching digest")
	assert.False(t, VerifyBodyDigest("sha-256=AAAA", body),
		"verify should return false for non-matching digest")
}
