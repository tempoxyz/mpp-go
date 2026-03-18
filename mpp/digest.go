package mpp

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
)

// BodyDigest provides helpers for computing and verifying SHA-256 digests.
// Compute returns a digest string in the form "sha-256=<base64>".
// Verify recomputes the digest and compares using constant-time comparison.
var BodyDigest = struct {
	Compute func(body any) string
	Verify  func(digest string, body any) bool
}{
	Compute: computeDigest,
	Verify:  verifyDigest,
}

func computeDigest(body any) string {
	data := toBytes(body)
	h := sha256.Sum256(data)
	return "sha-256=" + base64.StdEncoding.EncodeToString(h[:])
}

func verifyDigest(digest string, body any) bool {
	expected := computeDigest(body)
	return subtle.ConstantTimeCompare([]byte(expected), []byte(digest)) == 1
}

func toBytes(body any) []byte {
	switch v := body.(type) {
	case []byte:
		return v
	case string:
		return []byte(v)
	case map[string]any:
		b, err := json.Marshal(v)
		if err != nil {
			panic(fmt.Sprintf("mpp: failed to marshal body: %v", err))
		}
		return b
	default:
		panic(fmt.Sprintf("mpp: unsupported body type %T", body))
	}
}
