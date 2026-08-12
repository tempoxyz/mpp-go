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
	case json.RawMessage:
		return v
	default:
		// Any other value that came out of ordinary JSON decoding — a
		// map, a slice, a typed struct, a scalar — is JSON-marshallable.
		// Route it through the same canonical JSON path used for
		// map[string]any rather than rejecting it outright; this is
		// what BodyDigest's `body any` signature already promises.
		b, err := json.Marshal(v)
		if err != nil {
			// Only genuinely non-JSON-serializable Go values reach
			// here (chan, func, unsupported cyclic structures) —
			// values no ordinary decoded HTTP body can produce. This
			// remains a programmer error, not an operational input a
			// server should ever receive from request handling.
			panic(fmt.Sprintf("mpp: unsupported body type %T: %v", body, err))
		}
		return b
	}
}
