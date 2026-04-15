package mpp

import (
	"bytes"
	"encoding/json"
	"strings"
)

// JSONEqual compares two JSON-like values using Go's stable JSON encoding.
func JSONEqual(left, right any) bool {
	leftJSON, err := json.Marshal(left)
	if err != nil {
		return false
	}
	rightJSON, err := json.Marshal(right)
	if err != nil {
		return false
	}
	return bytes.Equal(leftJSON, rightJSON)
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

// FindPaymentAuthorization returns the Payment credential from an Authorization
// header. It tolerates comma-separated schemes and ignores non-Payment values.
func FindPaymentAuthorization(header string) string {
	return ExtractAuthorizationScheme(header, "Payment")
}
