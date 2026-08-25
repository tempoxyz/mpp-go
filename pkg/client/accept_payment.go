package client

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/tempoxyz/mpp-go/pkg/mpp"
)

type paymentPreference struct {
	method      string
	intent      string
	quality     float64
	index       int
	specificity int
}

func clonePaymentPreferences(preferences PaymentPreferences) PaymentPreferences {
	if preferences == nil {
		return nil
	}
	clone := make(PaymentPreferences, len(preferences))
	for key, quality := range preferences {
		clone[key] = quality
	}
	return clone
}

func resolvePaymentPreferences(methods []Method, configured PaymentPreferences) ([]paymentPreference, string, error) {
	known := make(map[string]struct{}, len(methods))
	preferences := make([]paymentPreference, 0, len(methods))
	for _, method := range methods {
		key := keyForMethod(method)
		name := paymentPreferenceKey(key.name, key.intent)
		if !validPaymentToken(key.name) || !validPaymentToken(key.intent) {
			return nil, "", fmt.Errorf("mpp: invalid payment method capability %q", name)
		}
		if _, ok := known[name]; ok {
			continue
		}
		known[name] = struct{}{}
		quality := 1.0
		if configuredQuality, ok := configured[name]; ok {
			quality = configuredQuality
		}
		if err := validateQuality(quality); err != nil {
			return nil, "", fmt.Errorf("mpp: invalid payment preference %q: %w", name, err)
		}
		preferences = append(preferences, paymentPreference{
			method:      key.name,
			intent:      key.intent,
			quality:     quality,
			index:       len(preferences),
			specificity: 2,
		})
	}
	for key := range configured {
		if _, ok := known[key]; !ok {
			return nil, "", fmt.Errorf("mpp: unknown payment preference %q", key)
		}
	}
	return preferences, serializeAcceptPayment(preferences), nil
}

func parseAcceptPayment(header string) ([]paymentPreference, error) {
	parts, err := splitHeaderValue(header, ',')
	if err != nil {
		return nil, err
	}
	preferences := make([]paymentPreference, 0, len(parts))
	for _, raw := range parts {
		part := strings.TrimSpace(raw)
		if part == "" {
			return nil, fmt.Errorf("mpp: invalid empty Accept-Payment entry")
		}
		preference, err := parsePaymentPreference(part, len(preferences))
		if err != nil {
			return nil, err
		}
		preferences = append(preferences, preference)
	}
	if len(preferences) == 0 {
		return nil, fmt.Errorf("mpp: empty Accept-Payment header")
	}
	return preferences, nil
}

func parsePaymentPreference(value string, index int) (paymentPreference, error) {
	parts, err := splitHeaderValue(value, ';')
	if err != nil {
		return paymentPreference{}, err
	}
	method, intent, ok := strings.Cut(strings.TrimSpace(parts[0]), "/")
	method = strings.TrimSpace(method)
	intent = strings.TrimSpace(intent)
	if !ok || !validPreferenceToken(method) || !validPreferenceToken(intent) {
		return paymentPreference{}, fmt.Errorf("mpp: invalid Accept-Payment entry %q", value)
	}

	quality := 1.0
	for _, raw := range parts[1:] {
		name, parameter, ok := strings.Cut(strings.TrimSpace(raw), "=")
		name = strings.TrimSpace(name)
		parameter = strings.TrimSpace(parameter)
		if !ok || !validParameterName(name) || parameter == "" {
			return paymentPreference{}, fmt.Errorf("mpp: invalid Accept-Payment parameter %q", raw)
		}
		if !strings.EqualFold(name, "q") {
			continue
		}
		parsed, err := strconv.ParseFloat(parameter, 64)
		if err != nil || !validHeaderQuality(parameter) {
			return paymentPreference{}, fmt.Errorf("mpp: invalid Accept-Payment q-value %q", parameter)
		}
		quality = parsed
	}

	return paymentPreference{
		method:      method,
		intent:      intent,
		quality:     quality,
		index:       index,
		specificity: boolInt(method != "*") + boolInt(intent != "*"),
	}, nil
}

func splitHeaderValue(value string, separator byte) ([]string, error) {
	var parts []string
	inQuote := false
	escaped := false
	start := 0
	for i := range len(value) {
		switch ch := value[i]; {
		case escaped:
			escaped = false
		case inQuote && ch == '\\':
			escaped = true
		case ch == '"':
			inQuote = !inQuote
		case ch == separator && !inQuote:
			parts = append(parts, strings.TrimSpace(value[start:i]))
			start = i + 1
		}
	}
	if inQuote || escaped {
		return nil, fmt.Errorf("mpp: malformed quoted Accept-Payment parameter")
	}
	return append(parts, strings.TrimSpace(value[start:])), nil
}

func bestPaymentPreference(challenge *mpp.Challenge, preferences []paymentPreference) (paymentPreference, bool) {
	var best paymentPreference
	found := false
	for _, preference := range preferences {
		if preference.method != "*" && preference.method != challenge.Method {
			continue
		}
		if preference.intent != "*" && preference.intent != challenge.Intent {
			continue
		}
		if !found || preference.specificity > best.specificity ||
			(preference.specificity == best.specificity && preference.quality > best.quality) ||
			(preference.specificity == best.specificity && preference.quality == best.quality && preference.index < best.index) {
			best = preference
			found = true
		}
	}
	return best, found
}

func serializeAcceptPayment(preferences []paymentPreference) string {
	entries := make([]string, 0, len(preferences))
	for _, preference := range preferences {
		entry := paymentPreferenceKey(preference.method, preference.intent)
		if preference.quality != 1 {
			entry += ";q=" + formatQuality(preference.quality)
		}
		entries = append(entries, entry)
	}
	return strings.Join(entries, ", ")
}

func paymentPreferenceKey(method, intent string) string {
	return method + "/" + intent
}

func validateQuality(quality float64) error {
	if math.IsNaN(quality) || math.IsInf(quality, 0) || quality < 0 || quality > 1 {
		return fmt.Errorf("q-value must be between 0 and 1")
	}
	if math.Abs(quality*1000-math.Round(quality*1000)) > 1e-9 {
		return fmt.Errorf("q-value must have at most three decimal places")
	}
	return nil
}

func validHeaderQuality(value string) bool {
	if value == "0" || value == "1" {
		return true
	}
	whole, fraction, ok := strings.Cut(value, ".")
	if !ok || len(fraction) > 3 {
		return false
	}
	if whole == "0" {
		return allDigits(fraction)
	}
	return whole == "1" && strings.Trim(fraction, "0") == ""
}

func validPreferenceToken(value string) bool {
	return value == "*" || validPaymentToken(value)
}

func validPaymentToken(value string) bool {
	if value == "" {
		return false
	}
	for i := range len(value) {
		ch := value[i]
		if (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9') {
			continue
		}
		if !strings.ContainsRune("!#$%&'*+-.^_`|~", rune(ch)) {
			return false
		}
	}
	return true
}

func validParameterName(value string) bool {
	if value == "" {
		return false
	}
	for i := range len(value) {
		ch := value[i]
		if (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') ||
			(ch >= '0' && ch <= '9') || ch == '_' || ch == '-' {
			continue
		}
		return false
	}
	return true
}

func allDigits(value string) bool {
	for i := range len(value) {
		if value[i] < '0' || value[i] > '9' {
			return false
		}
	}
	return true
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func formatQuality(quality float64) string {
	return strings.TrimRight(strings.TrimRight(strconv.FormatFloat(quality, 'f', 3, 64), "0"), ".")
}
