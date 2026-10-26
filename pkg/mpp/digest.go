package mpp

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
)

// ComputeBodyDigest returns the SHA-256 digest of body in the form
// "sha-256=<base64>", the value of a challenge's digest parameter. Pass the
// raw request bytes so the digest binds exactly what the client sent.
func ComputeBodyDigest(body []byte) string {
	h := sha256.Sum256(body)
	return "sha-256=" + base64.StdEncoding.EncodeToString(h[:])
}

// VerifyBodyDigest reports whether digest is the ComputeBodyDigest of body,
// using constant-time comparison.
func VerifyBodyDigest(digest string, body []byte) bool {
	return subtle.ConstantTimeCompare([]byte(ComputeBodyDigest(body)), []byte(digest)) == 1
}
