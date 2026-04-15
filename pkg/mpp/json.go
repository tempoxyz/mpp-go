package mpp

import (
	"bytes"
	"encoding/json"
	"strings"
)

// CanonicalEqual compares two JSON-like values using their canonical JSON form.
func CanonicalEqual(left, right any) bool {
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

// ExtractPaymentAuthorization returns the Payment credential from an
// Authorization header. It tolerates comma-separated schemes and ignores
// non-Payment values.
func ExtractPaymentAuthorization(header string) string {
	for _, scheme := range strings.Split(header, ",") {
		scheme = strings.TrimSpace(scheme)
		if len(scheme) >= len("Payment ") && strings.EqualFold(scheme[:len("Payment")], "Payment") {
			if len(scheme) > len("Payment") && scheme[len("Payment")] == ' ' {
				return scheme
			}
		}
	}
	return ""
}

// Deprecated: use ExtractPaymentAuthorization.
func ExtractPaymentScheme(header string) string {
	return ExtractPaymentAuthorization(header)
}
