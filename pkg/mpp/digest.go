package mpp

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"strings"
)

// ComputeBodyDigest returns the SHA-256 digest of body in the form
// "sha-256=<base64>", the value of a challenge's digest parameter. Pass the
// raw request bytes so the digest binds exactly what the client sent.
func ComputeBodyDigest(body []byte) string {
	h := sha256.Sum256(body)
	return "sha-256=" + base64.StdEncoding.EncodeToString(h[:])
}

// VerifyBodyDigest reports whether digest is the ComputeBodyDigest of body,
// using constant-time comparison. It also accepts the RFC 9530 byte-sequence
// form "sha-256=:<base64>:", which the Payment spec shows.
func VerifyBodyDigest(digest string, body []byte) bool {
	return subtle.ConstantTimeCompare([]byte(ComputeBodyDigest(body)), []byte(normalizeBodyDigest(digest))) == 1
}

// normalizeBodyDigest strips the colons of an RFC 9530 byte sequence and
// restores any padding it omits (RFC 8941 section 4.2.7):
// "sha-256=:<base64>:" becomes "sha-256=<base64>". Other text is unchanged.
func normalizeBodyDigest(digest string) string {
	value, ok := strings.CutPrefix(digest, "sha-256=:")
	if !ok {
		return digest
	}
	inner, ok := strings.CutSuffix(value, ":")
	if !ok || inner == "" || strings.Contains(inner, ":") {
		return digest
	}
	inner = strings.TrimRight(inner, "=")
	if inner == "" {
		return digest
	}
	return "sha-256=" + inner + strings.Repeat("=", (4-len(inner)%4)%4)
}
