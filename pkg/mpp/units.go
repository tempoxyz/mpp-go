package mpp

import (
	"encoding/json"
	"fmt"
	"math/big"
	"strconv"
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

// ParseUnits converts a human-readable decimal string to base units.
// For example, ParseUnits("1.5", 6) returns 1500000.
// Returns an error if value is not a valid decimal, is negative,
// would produce fractional base units, decimals is negative, or the result
// overflows int64.
//
// Deprecated: Use ParseAmount instead, which does not overflow for
// 18-decimal tokens.
func ParseUnits(value string, decimals int) (int64, error) {
	result, err := ParseAmount(value, decimals)
	if err != nil {
		return 0, err
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
//
// Deprecated: Convert amounts with ParseAmount when building the request.
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
		out["amount"] = strconv.FormatInt(units, 10)
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
		out["suggestedDeposit"] = strconv.FormatInt(units, 10)
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
	case json.Number:
		parsed, err := n.Int64()
		if err != nil {
			return 0, fmt.Errorf("non-integer json.Number: %v", n)
		}
		return int(parsed), nil
	default:
		return 0, fmt.Errorf("unsupported type %T", v)
	}
}
