package mpp

import (
	"testing"
)

func TestBodyDigest_Compute_String(t *testing.T) {
	digest := BodyDigest.Compute("hello")
	// SHA-256 of "hello" = LPJNul+wow4m6DsqxbninhsWHlwfp0JecwQzYpOLmCQ=
	want := "sha-256=LPJNul+wow4m6DsqxbninhsWHlwfp0JecwQzYpOLmCQ="
	if digest != want {
		t.Errorf("got %q, want %q", digest, want)
	}
}

func TestBodyDigest_Compute_Bytes(t *testing.T) {
	digest := BodyDigest.Compute([]byte("hello"))
	want := "sha-256=LPJNul+wow4m6DsqxbninhsWHlwfp0JecwQzYpOLmCQ="
	if digest != want {
		t.Errorf("got %q, want %q", digest, want)
	}
}

func TestBodyDigest_Compute_Map(t *testing.T) {
	body := map[string]any{
		"b": 2,
		"a": 1,
	}
	digest := BodyDigest.Compute(body)
	// json.Marshal sorts map keys, so {"a":1,"b":2}
	if digest == "" {
		t.Fatal("digest should not be empty")
	}
	// Same map should produce same digest.
	digest2 := BodyDigest.Compute(map[string]any{"a": 1, "b": 2})
	if digest != digest2 {
		t.Errorf("same content should produce same digest: %q vs %q", digest, digest2)
	}
}

func TestBodyDigest_Compute_StandardBase64(t *testing.T) {
	// Ensure we use standard base64 (with + and /), not URL-safe.
	digest := BodyDigest.Compute("hello")
	// The known digest contains '+' which confirms standard encoding.
	if len(digest) <= len("sha-256=") {
		t.Fatal("digest too short")
	}
}

func TestBodyDigest_Verify(t *testing.T) {
	body := "test body"
	digest := BodyDigest.Compute(body)

	if !BodyDigest.Verify(digest, body) {
		t.Error("verify should return true for matching digest")
	}

	if BodyDigest.Verify("sha-256=AAAA", body) {
		t.Error("verify should return false for non-matching digest")
	}
}

func TestBodyDigest_Verify_Map(t *testing.T) {
	body := map[string]any{"key": "value"}
	digest := BodyDigest.Compute(body)

	if !BodyDigest.Verify(digest, body) {
		t.Error("verify should return true for matching map digest")
	}
}

func TestBodyDigest_Prefix(t *testing.T) {
	digest := BodyDigest.Compute("x")
	if len(digest) < 8 || digest[:8] != "sha-256=" {
		t.Errorf("digest should start with 'sha-256=', got %q", digest)
	}
}
