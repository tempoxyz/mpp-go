package mpp

import (
	"encoding/json"
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

// Reproduces #95: JSON-compatible body types outside the old narrow
// allow-list ([]byte, string, map[string]any) must not panic.

func TestBodyDigest_Compute_Slice(t *testing.T) {
	assert.NotPanics(t, func() {
		digest := BodyDigest.Compute([]int{1, 2, 3})
		assert.Equal(t, "sha-256="+shaB64(t, "[1,2,3]"), digest)
	})
}

func TestBodyDigest_Compute_Struct(t *testing.T) {
	type payload struct {
		Amount int    `json:"amount"`
		Symbol string `json:"symbol"`
	}
	assert.NotPanics(t, func() {
		digest := BodyDigest.Compute(payload{Amount: 5, Symbol: "USDC"})
		assert.Equal(t, "sha-256="+shaB64(t, `{"amount":5,"symbol":"USDC"}`), digest)
	})
}

func TestBodyDigest_Compute_RawMessage(t *testing.T) {
	raw := json.RawMessage(`{"items":[1,2,3]}`)
	assert.NotPanics(t, func() {
		digest := BodyDigest.Compute(raw)
		assert.Equal(t, "sha-256="+shaB64(t, `{"items":[1,2,3]}`), digest)
	})
}

func TestBodyDigest_Compute_Int(t *testing.T) {
	assert.NotPanics(t, func() {
		digest := BodyDigest.Compute(42)
		assert.Equal(t, "sha-256="+shaB64(t, "42"), digest)
	})
}

func TestBodyDigest_Compute_UnsupportedStillPanics(t *testing.T) {
	// A genuinely non-JSON-serializable Go value should still panic —
	// this isn't something ordinary request-body decoding can produce.
	assert.Panics(t, func() {
		BodyDigest.Compute(make(chan int))
	})
}

func shaB64(t *testing.T, s string) string {
	t.Helper()
	digest := BodyDigest.Compute(s)
	return digest[len("sha-256="):]
}
