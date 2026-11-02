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

func TestVerifyBodyDigestRFC9530ByteSequence(t *testing.T) {
	const hello = "LPJNul+wow4m6DsqxbninhsWHlwfp0JecwQzYpOLmCQ="
	if !VerifyBodyDigest("sha-256=:"+hello+":", []byte("hello")) {
		t.Fatal("byte sequence for hello rejected")
	}
	if !VerifyBodyDigest("sha-256=:47DEQpj8HBSa+/TImW+5JCeuQeRkm5NMpJWZG3hSuFU=:", nil) {
		t.Fatal("byte sequence for empty body rejected")
	}
	if !VerifyBodyDigest("sha-256=:LPJNul+wow4m6DsqxbninhsWHlwfp0JecwQzYpOLmCQ:", []byte("hello")) {
		t.Fatal("unpadded byte sequence for hello rejected")
	}
	if VerifyBodyDigest("sha-256=:"+hello+":", []byte("changed body")) {
		t.Fatal("byte sequence accepted a changed body")
	}
	for _, digest := range []string{
		"sha-256=:" + hello,
		"sha-256=" + hello + ":",
		"sha-256=::" + hello + "::",
		"sha-256=:" + hello + ": ",
		"sha-256=:" + hello + ":;param=1",
		"SHA-256=:" + hello + ":",
		"sha-256=::",
	} {
		if VerifyBodyDigest(digest, []byte("hello")) {
			t.Errorf("accepted malformed digest %q", digest)
		}
	}
}
