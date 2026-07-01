package mpp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// JSONEqual compares two JSON-like values using Go's stable JSON encoding.
func JSONEqual(left, right any) bool {
	leftJSON, err := encodeStableJSON(left)
	if err != nil {
		return false
	}
	rightJSON, err := encodeStableJSON(right)
	if err != nil {
		return false
	}
	return bytes.Equal(leftJSON, rightJSON)
}

func encodeStableJSON(value any) ([]byte, error) {
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

// ExtractAuthorizationScheme returns the first authorization value that matches
// the requested scheme from a possibly merged Authorization header.
func ExtractAuthorizationScheme(header, scheme string) string {
	for _, value := range SplitAuthenticate(header) {
		name, _, ok := strings.Cut(strings.TrimSpace(value), " ")
		if ok && strings.EqualFold(name, scheme) {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

// ExtractAuthorizationSchemeStrict returns a single authorization value that
// matches scheme, or an error if the header includes multiple matching values.
func ExtractAuthorizationSchemeStrict(header, scheme string) (string, error) {
	var found string
	for _, value := range SplitAuthenticate(header) {
		value = strings.TrimSpace(value)
		name, _, ok := strings.Cut(value, " ")
		if !ok || !strings.EqualFold(name, scheme) {
			continue
		}
		if found != "" {
			return "", fmt.Errorf("mpp: multiple %s credentials", scheme)
		}
		found = value
	}
	return found, nil
}

// FindPaymentAuthorization returns the Payment credential from an Authorization
// header. It tolerates comma-separated schemes and ignores non-Payment values.
func FindPaymentAuthorization(header string) string {
	return ExtractAuthorizationScheme(header, "Payment")
}

// FindPaymentAuthorizationStrict returns the Payment credential from an
// Authorization header, or an error if more than one Payment credential exists.
func FindPaymentAuthorizationStrict(header string) (string, error) {
	return ExtractAuthorizationSchemeStrict(header, "Payment")
}
