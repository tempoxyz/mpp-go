package mpp

import (
	"fmt"
	"math/big"
	"strings"
)

// ParseAmount converts a human-readable decimal string to base units. For
// example, ParseAmount("1.5", 6) returns 1500000. The result is unbounded,
// so 18-decimal token amounts do not overflow. It returns an error if value
// is empty, negative or not a plain decimal (digits with an optional single
// "."), if it would produce fractional base units, or if decimals is
// negative.
func ParseAmount(value string, decimals int) (*big.Int, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, fmt.Errorf("mpp: empty value")
	}
	// Guard decimals before it is used to slice the fractional part: a negative
	// value would panic on fracPart[decimals:].
	if decimals < 0 {
		return nil, fmt.Errorf("mpp: negative decimals: %d", decimals)
	}
	if strings.HasPrefix(value, "-") {
		return nil, fmt.Errorf("mpp: negative value: %q", value)
	}

	// Split into integer and fractional parts via string manipulation
	// to avoid floating-point precision issues.
	intPart, fracPart := value, ""
	if dot := strings.IndexByte(value, '.'); dot >= 0 {
		intPart = value[:dot]
		fracPart = value[dot+1:]
	}
	// A bare "." carries no digits on either side of the separator. Without
	// this guard both parts pass the digit checks below and the value parses
	// as 0 instead of being rejected.
	if intPart == "" && fracPart == "" {
		return nil, fmt.Errorf("mpp: invalid decimal value: %q", value)
	}
	if intPart == "" {
		intPart = "0"
	}
	for _, c := range intPart + fracPart {
		if c < '0' || c > '9' {
			return nil, fmt.Errorf("mpp: invalid decimal value: %q", value)
		}
	}

	// If fractional digits exceed decimals, the extras must all be zero.
	if len(fracPart) > decimals {
		for _, c := range fracPart[decimals:] {
			if c != '0' {
				return nil, fmt.Errorf("mpp: value %q with %d decimals produces fractional base units", value, decimals)
			}
		}
		fracPart = fracPart[:decimals]
	}
	fracPart += strings.Repeat("0", decimals-len(fracPart))

	result, ok := new(big.Int).SetString(intPart+fracPart, 10)
	if !ok {
		return nil, fmt.Errorf("mpp: invalid decimal value: %q", value)
	}
	return result, nil
}
