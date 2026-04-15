package mpp

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"sort"
	"strings"
)

// GenerateChallengeIDInput holds the inputs for HMAC-based challenge ID generation.
type GenerateChallengeIDInput struct {
	SecretKey string
	Realm     string
	Method    string
	Intent    string
	Request   map[string]any
	Expires   string
	Digest    string
	Opaque    map[string]string
}

// GenerateChallengeID produces an HMAC-SHA256 challenge ID from the given inputs.
// The HMAC is computed over pipe-delimited fields:
//
//	realm|method|intent|request_b64|expires|digest|opaque_b64
//
// The result is base64url-encoded without padding.
func GenerateChallengeID(opts GenerateChallengeIDInput) string {
	requestB64 := b64EncodeRequest(opts.Request)

	opaqueB64 := ""
	if opts.Opaque != nil {
		opaqueB64 = b64EncodeSortedStringMap(opts.Opaque)
	}

	parts := []string{
		opts.Realm,
		opts.Method,
		opts.Intent,
		requestB64,
		opts.Expires,
		opts.Digest,
		opaqueB64,
	}

	mac := hmac.New(sha256.New, []byte(opts.SecretKey))
	mac.Write([]byte(strings.Join(parts, "|")))

	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// ConstantTimeEqual returns true if a and b are equal, using constant-time
// comparison to avoid timing side-channels.
func ConstantTimeEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// b64EncodeSortedStringMap encodes a map[string]string as compact sorted JSON
// then base64url without padding.
func b64EncodeSortedStringMap(m map[string]string) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	// Build a sorted JSON object manually to guarantee key order.
	buf := strings.Builder{}
	buf.WriteByte('{')
	for i, k := range keys {
		if i > 0 {
			buf.WriteByte(',')
		}
		kb, _ := json.Marshal(k)
		vb, _ := json.Marshal(m[k])
		buf.Write(kb)
		buf.WriteByte(':')
		buf.Write(vb)
	}
	buf.WriteByte('}')

	return base64.RawURLEncoding.EncodeToString([]byte(buf.String()))
}
