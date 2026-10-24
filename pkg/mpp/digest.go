package mpp

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
)

// BodyDigest provides helpers for computing and verifying SHA-256 digests.
// Compute returns a digest string in the form "sha-256=<base64>".
// Verify recomputes the digest and compares using constant-time comparison.
//
// A []byte, string or json.RawMessage body, or a value of a named string or
// byte-slice type, is digested as is. Any other value
// is digested as its json.Marshal output, not the raw request bytes, so pass
// the raw bytes to bind exactly what the client sent. Compute and Verify panic
// for a value json.Marshal cannot encode, such as a channel or function, and
// for an io.Reader such as http.Request.Body: read it and pass the bytes.
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
	case io.Reader:
		// A reader's JSON form ({} for *bytes.Buffer, http.NoBody or
		// r.Body) says nothing about its content, so digesting it would
		// bind every request to the same value.
		panic(fmt.Sprintf("mpp: unsupported body type %T: read the body and pass its bytes", body))
	}
	if body != nil {
		// Named string and byte-slice types are raw bytes too, not their
		// quoted or base64 JSON encodings.
		rv := reflect.ValueOf(body)
		switch {
		case rv.Kind() == reflect.String:
			return []byte(rv.String())
		case rv.Kind() == reflect.Slice && rv.Type().Elem().Kind() == reflect.Uint8:
			return rv.Bytes()
		}
	}
	// Any other value, such as a map, slice, struct or scalar, is digested
	// as its json.Marshal output.
	b, err := json.Marshal(body)
	if err != nil {
		panic(fmt.Sprintf("mpp: unsupported body type %T: %v", body, err))
	}
	return b
}
