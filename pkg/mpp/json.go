package mpp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// CanonicalJSON marshals a value using Go's stable JSON encoding so nested maps
// can be compared without depending on runtime map iteration order.
func CanonicalJSON(value any) ([]byte, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("mpp: canonical json: %w", err)
	}
	return encoded, nil
}

// CanonicalEqual compares two JSON-like values using their canonical JSON form.
func CanonicalEqual(left, right any) bool {
	leftJSON, err := CanonicalJSON(left)
	if err != nil {
		return false
	}
	rightJSON, err := CanonicalJSON(right)
	if err != nil {
		return false
	}
	return bytes.Equal(leftJSON, rightJSON)
}

// ExtractPaymentScheme returns the Payment credential from an Authorization
// header. It tolerates comma-separated schemes and ignores non-Payment values.
func ExtractPaymentScheme(header string) string {
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
