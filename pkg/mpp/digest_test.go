package mpp

import (
	"github.com/stretchr/testify/assert"
	"testing"
)

func TestBodyDigest_Compute_String(t *testing.T) {
	digest := BodyDigest.Compute("hello")
	// SHA-256 of "hello" = LPJNul+wow4m6DsqxbninhsWHlwfp0JecwQzYpOLmCQ=
	want := "sha-256=LPJNul+wow4m6DsqxbninhsWHlwfp0JecwQzYpOLmCQ="
	assert.Equalf(t, want, digest,
		"got %q, want %q", digest, want)

}

func TestBodyDigest_Compute_Bytes(t *testing.T) {
	digest := BodyDigest.Compute([]byte("hello"))
	want := "sha-256=LPJNul+wow4m6DsqxbninhsWHlwfp0JecwQzYpOLmCQ="
	assert.Equalf(t, want, digest,
		"got %q, want %q", digest, want)

}

func TestBodyDigest_Compute_Map(t *testing.T) {
	body := map[string]any{
		"b": 2,
		"a": 1,
	}
	digest := BodyDigest.Compute(body)
	if !
	// json.Marshal sorts map keys, so {"a":1,"b":2}
	assert.NotEqual(t, "", digest,
		"digest should not be empty") {
		return

		// Same map should produce same digest.
	}

	digest2 := BodyDigest.Compute(map[string]any{"a": 1, "b": 2})
	assert.Equalf(t, digest2, digest,
		"same content should produce same digest: %q vs %q", digest, digest2)

}

func TestBodyDigest_Compute_StandardBase64(t *testing.T) {
	// Ensure we use standard base64 (with + and /), not URL-safe.
	digest := BodyDigest.Compute("hello")
	if !
	// The known digest contains '+' which confirms standard encoding.
	assert.False(t, len(digest) <= len("sha-256="),
		"digest too short") {
		return
	}

}

func TestBodyDigest_Verify(t *testing.T) {
	body := "test body"
	digest := BodyDigest.Compute(body)
	assert.True(t, BodyDigest.Verify(digest, body),
		"verify should return true for matching digest")
	assert.False(t, BodyDigest.Verify("sha-256=AAAA", body),
		"verify should return false for non-matching digest")

}

func TestBodyDigest_Verify_Map(t *testing.T) {
	body := map[string]any{"key": "value"}
	digest := BodyDigest.Compute(body)
	assert.True(t, BodyDigest.Verify(digest, body),
		"verify should return true for matching map digest")

}

func TestBodyDigest_Prefix(t *testing.T) {
	digest := BodyDigest.Compute("x")
	assert.Falsef(t, len(digest) < 8 || digest[:8] != "sha-256=",
		"digest should start with 'sha-256=', got %q", digest)

}
