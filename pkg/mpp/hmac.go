package mpp

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
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
	Header    string
}

// GenerateChallengeIDWithError produces an HMAC-SHA256 challenge ID from the
// given inputs and rejects ambiguous fields, invalid advertised credential
// headers and request values that JCS cannot represent exactly. The HMAC is
// computed over pipe-delimited fields:
//
//	realm|method|intent|request_b64|expires|digest|opaque_b64
//
// Challenges that advertise a credential header insert it immediately before
// the final opaque slot. Authorization is the implicit default and is never
// included, preserving the legacy binding. The result is base64url-encoded
// without padding.
func GenerateChallengeIDWithError(opts GenerateChallengeIDInput) (string, error) {
	return generateChallengeID(opts, "")
}

// generateChallengeID binds encodedOpaque as the opaque slot when it is set,
// and the canonical encoding of opts.Opaque otherwise.
func generateChallengeID(opts GenerateChallengeIDInput, encodedOpaque string) (string, error) {
	header, err := parseAdvertisedCredentialHeader(opts.Header)
	if err != nil {
		return "", err
	}
	fields := []struct {
		name  string
		value string
	}{
		{"realm", opts.Realm},
		{"method", opts.Method},
		{"intent", opts.Intent},
		{"expires", opts.Expires},
		{"digest", opts.Digest},
		{"header", header},
	}
	for _, field := range fields {
		if strings.Contains(field.value, "|") {
			return "", fmt.Errorf("mpp: challenge %s contains reserved delimiter", field.name)
		}
	}

	requestB64, err := b64EncodeRequestWithError(opts.Request)
	if err != nil {
		return "", err
	}

	opaqueB64 := encodedOpaque
	if opaqueB64 == "" && opts.Opaque != nil {
		opaqueB64, err = b64EncodeSortedStringMap(opts.Opaque)
		if err != nil {
			return "", err
		}
	}

	parts := []string{
		opts.Realm,
		opts.Method,
		opts.Intent,
		requestB64,
		opts.Expires,
		opts.Digest,
	}
	if header != "" {
		parts = append(parts, header)
	}
	parts = append(parts, opaqueB64)

	mac := hmac.New(sha256.New, []byte(opts.SecretKey))
	mac.Write([]byte(strings.Join(parts, "|")))

	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

// ErrUnsupportedCredentialHeader reports a challenge header auth-param other
// than Payment-Authorization. A challenge may only omit header (selecting
// Authorization) or select Payment-Authorization: ParseChallenge rejects a
// received challenge with any other value, including an explicit
// "Authorization" or an empty value, so clients send no credential for it,
// and challenge construction and formatting reject a configured header other
// than Authorization or Payment-Authorization. Match it with errors.Is.
var ErrUnsupportedCredentialHeader = errors.New("mpp: unsupported credential header: challenges may only select Payment-Authorization")

// AdvertisedCredentialHeader returns the challenge header parameter value
// for a configured credential header: empty for the implicit Authorization
// default (empty, or Authorization in any case), HeaderPaymentAuthorization
// for Payment-Authorization in any case, and header unchanged otherwise,
// which challenge construction and formatting reject with
// ErrUnsupportedCredentialHeader.
func AdvertisedCredentialHeader(header string) string {
	switch {
	case isDefaultCredentialHeader(header):
		return ""
	case strings.EqualFold(header, HeaderPaymentAuthorization):
		return HeaderPaymentAuthorization
	}
	return header
}

func isDefaultCredentialHeader(header string) bool {
	return header == "" || strings.EqualFold(header, HeaderAuthorization)
}

// parseAdvertisedCredentialHeader validates a locally configured credential
// header. Authorization is shorthand for the default and is never advertised;
// Payment-Authorization is advertised canonically; anything else is
// rejected, since servers must not emit it.
func parseAdvertisedCredentialHeader(header string) (string, error) {
	header = AdvertisedCredentialHeader(header)
	if header != "" && header != HeaderPaymentAuthorization {
		return "", ErrUnsupportedCredentialHeader
	}
	return header, nil
}

// parseReceivedCredentialHeader validates the raw header auth-param of a
// received challenge, or of the challenge a received credential echoes,
// before any normalization: an absent header selects Authorization, and only
// Payment-Authorization (compared case-insensitively) may be present. Any
// other value, including Authorization and the empty string, makes the
// challenge unrecognized.
func parseReceivedCredentialHeader(value string, present bool) (string, error) {
	if !present {
		return "", nil
	}
	if strings.EqualFold(value, HeaderPaymentAuthorization) {
		return HeaderPaymentAuthorization, nil
	}
	return "", ErrUnsupportedCredentialHeader
}

// ConstantTimeEqual returns true if a and b are equal, using constant-time
// comparison to avoid timing side-channels.
func ConstantTimeEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// b64EncodeSortedStringMap encodes a map[string]string as RFC 8785 canonical
// JSON, then base64url without padding.
func b64EncodeSortedStringMap(m map[string]string) (string, error) {
	encoded, err := encodeCanonicalJSON(m)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(encoded), nil
}
