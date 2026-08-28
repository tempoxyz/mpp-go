package mpp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"strconv"
	"strings"

	"github.com/gowebpki/jcs"
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

// encodeCanonicalJSON serializes a value using RFC 8785 JSON Canonicalization
// Scheme (JCS). Challenge-bound request and opaque values use this encoding so
// their HMAC inputs are reproducible across SDKs.
func encodeCanonicalJSON(value any) ([]byte, error) {
	encoded, err := encodeStableJSON(value)
	if err != nil {
		return nil, err
	}
	canonical, err := jcs.Transform(encoded)
	if err != nil {
		return nil, err
	}
	if err := validateJCSNumericRoundTrip(encoded, canonical); err != nil {
		return nil, err
	}
	return canonical, nil
}

// ChallengeBoundJSONEqual compares values using the RFC 8785 representation
// used to bind challenge requests and opaque values into their HMAC IDs.
func ChallengeBoundJSONEqual(left, right any) bool {
	leftJSON, err := encodeCanonicalJSON(left)
	if err != nil {
		return false
	}
	rightJSON, err := encodeCanonicalJSON(right)
	if err != nil {
		return false
	}
	return bytes.Equal(leftJSON, rightJSON)
}

// validateJCSNumericRoundTrip rejects numbers that JCS would round or
// underflow. Without this check, distinct Go integers could produce the same
// challenge ID after conversion through JCS's IEEE-754 number representation.
func validateJCSNumericRoundTrip(original, canonical []byte) error {
	originalValue, err := decodeJSONValue(original)
	if err != nil {
		return err
	}
	canonicalValue, err := decodeJSONValue(canonical)
	if err != nil {
		return err
	}
	return compareJSONNumbers(originalValue, canonicalValue)
}

func decodeJSONValue(data []byte) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return nil, fmt.Errorf("unexpected trailing data")
	}
	return value, nil
}

func compareJSONNumbers(original, canonical any) error {
	switch original := original.(type) {
	case json.Number:
		canonicalNumber, ok := canonical.(json.Number)
		if !ok {
			return fmt.Errorf("mpp: JCS changed a JSON number's type")
		}
		originalValue, err := exactJSONNumber(original.String())
		if err != nil {
			return err
		}
		canonicalValue, err := exactJSONNumber(canonicalNumber.String())
		if err != nil {
			return err
		}
		if originalValue.Cmp(canonicalValue) != 0 {
			return fmt.Errorf("mpp: JSON number is not exactly representable by JCS")
		}
	case []any:
		canonicalArray, ok := canonical.([]any)
		if !ok || len(original) != len(canonicalArray) {
			return fmt.Errorf("mpp: JCS changed a JSON array")
		}
		for index := range original {
			if err := compareJSONNumbers(original[index], canonicalArray[index]); err != nil {
				return err
			}
		}
	case map[string]any:
		canonicalObject, ok := canonical.(map[string]any)
		if !ok || len(original) != len(canonicalObject) {
			return fmt.Errorf("mpp: JCS changed a JSON object")
		}
		for key, originalValue := range original {
			canonicalValue, ok := canonicalObject[key]
			if !ok {
				return fmt.Errorf("mpp: JCS changed a JSON object")
			}
			if err := compareJSONNumbers(originalValue, canonicalValue); err != nil {
				return err
			}
		}
	}
	return nil
}

func exactJSONNumber(value string) (*big.Rat, error) {
	mantissa, exponent, hasExponent := strings.Cut(strings.ToLower(value), "e")
	result, ok := new(big.Rat).SetString(mantissa)
	if !ok {
		return nil, fmt.Errorf("mpp: invalid JSON number %q", value)
	}
	if !hasExponent {
		return result, nil
	}
	power, err := strconv.Atoi(exponent)
	if err != nil || power < -324 || power > 308 {
		return nil, fmt.Errorf("mpp: JSON number %q is outside JCS range", value)
	}
	factor := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(abs(power))), nil)
	if power < 0 {
		return result.Quo(result, new(big.Rat).SetInt(factor)), nil
	}
	return result.Mul(result, new(big.Rat).SetInt(factor)), nil
}

func abs(value int) int {
	if value < 0 {
		return -value
	}
	return value
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
	return ExtractAuthorizationScheme(header, SchemePayment)
}

// FindPaymentAuthorizationStrict returns the Payment credential from an
// Authorization header, or an error if more than one Payment credential exists.
func FindPaymentAuthorizationStrict(header string) (string, error) {
	return ExtractAuthorizationSchemeStrict(header, SchemePayment)
}
