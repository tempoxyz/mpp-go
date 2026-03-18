package mpp

import (
	"fmt"
	"math/big"
	"strings"
)

// ParseUnits converts a human-readable decimal string to base units.
// For example, ParseUnits("1.5", 6) returns 1500000.
// Returns an error if value is not a valid decimal, is negative,
// or would produce fractional base units.
func ParseUnits(value string, decimals int) (int64, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, fmt.Errorf("mpp: empty value")
	}

	if strings.HasPrefix(value, "-") {
		return 0, fmt.Errorf("mpp: negative value: %q", value)
	}

	// Split into integer and fractional parts via string manipulation
	// to avoid floating-point precision issues.
	intPart, fracPart := value, ""
	if dot := strings.IndexByte(value, '.'); dot >= 0 {
		intPart = value[:dot]
		fracPart = value[dot+1:]
	}

	if intPart == "" {
		intPart = "0"
	}

	// Validate that both parts are pure digits.
	for _, c := range intPart {
		if c < '0' || c > '9' {
			return 0, fmt.Errorf("mpp: invalid decimal value: %q", value)
		}
	}
	for _, c := range fracPart {
		if c < '0' || c > '9' {
			return 0, fmt.Errorf("mpp: invalid decimal value: %q", value)
		}
	}

	// If fractional digits exceed decimals, the extras must all be zero.
	if len(fracPart) > decimals {
		for _, c := range fracPart[decimals:] {
			if c != '0' {
				return 0, fmt.Errorf("mpp: value %q with %d decimals produces fractional base units", value, decimals)
			}
		}
		fracPart = fracPart[:decimals]
	}

	// Pad fractional part with trailing zeros to fill decimals places.
	for len(fracPart) < decimals {
		fracPart += "0"
	}

	// Concatenate integer + padded fraction and parse as big.Int.
	combined := intPart + fracPart
	result, ok := new(big.Int).SetString(combined, 10)
	if !ok {
		return 0, fmt.Errorf("mpp: invalid decimal value: %q", value)
	}

	if !result.IsInt64() {
		return 0, fmt.Errorf("mpp: value %q with %d decimals overflows int64", value, decimals)
	}

	return result.Int64(), nil
}

// TransformUnits transforms request amounts from human-readable to base units.
// If a "decimals" key is present in the request map, it converts the "amount"
// field (and optionally "suggestedDeposit") from human-readable decimal strings
// to base unit strings, then removes the "decimals" key.
// If no "decimals" key is present, the request is returned unchanged.
func TransformUnits(request map[string]any) (map[string]any, error) {
	decRaw, ok := request["decimals"]
	if !ok {
		return request, nil
	}

	decimals, err := toInt(decRaw)
	if err != nil {
		return nil, fmt.Errorf("mpp: invalid decimals: %w", err)
	}

	out := make(map[string]any, len(request))
	for k, v := range request {
		out[k] = v
	}

	if amountRaw, ok := out["amount"]; ok {
		amountStr, ok := amountRaw.(string)
		if !ok {
			return nil, fmt.Errorf("mpp: amount must be a string")
		}
		units, err := ParseUnits(amountStr, decimals)
		if err != nil {
			return nil, fmt.Errorf("mpp: converting amount: %w", err)
		}
		out["amount"] = fmt.Sprintf("%d", units)
	}

	if depositRaw, ok := out["suggestedDeposit"]; ok {
		depositStr, ok := depositRaw.(string)
		if !ok {
			return nil, fmt.Errorf("mpp: suggestedDeposit must be a string")
		}
		units, err := ParseUnits(depositStr, decimals)
		if err != nil {
			return nil, fmt.Errorf("mpp: converting suggestedDeposit: %w", err)
		}
		out["suggestedDeposit"] = fmt.Sprintf("%d", units)
	}

	delete(out, "decimals")
	return out, nil
}

func toInt(v any) (int, error) {
	switch n := v.(type) {
	case int:
		return n, nil
	case int64:
		return int(n), nil
	case float64:
		if n != float64(int(n)) {
			return 0, fmt.Errorf("non-integer float64: %v", n)
		}
		return int(n), nil
	default:
		return 0, fmt.Errorf("unsupported type %T", v)
	}
}
