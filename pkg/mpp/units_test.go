package mpp

import (
	"math/big"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestParseAmountUnits(t *testing.T) {
	tests := []struct {
		name     string
		value    string
		decimals int
		want     int64
		wantErr  bool
	}{
		{"integer", "1", 6, 1_000_000, false},
		{"decimal", "1.5", 6, 1_500_000, false},
		{"zero", "0", 6, 0, false},
		{"zero decimal", "0.0", 6, 0, false},
		{"small", "0.000001", 6, 1, false},
		{"large", "1000000", 6, 1_000_000_000_000, false},
		{"no decimals", "42", 0, 42, false},
		{"with spaces", "  1.5  ", 6, 1_500_000, false},

		// Errors.
		{"empty", "", 6, 0, true},
		{"negative", "-1", 6, 0, true},
		{"negative decimals", "1", -1, 0, true},
		{"invalid", "abc", 6, 0, true},
		{"bare separator", ".", 6, 0, true},
		{"fractional base units", "1.0000005", 6, 0, true},
		{"too many decimals", "0.0000001", 6, 0, true},
		{"negative decimals", "1.5", -1, 0, true},
		{"integer with negative decimals", "100", -1, 0, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseAmount(tc.value, tc.decimals)
			if tc.wantErr {
				assert.Errorf(t, err,
					"expected error, got %s", got)

				return
			}
			if !assert.NoErrorf(t, err,
				"unexpected error: %v", err) {
				return
			}
			assert.Equalf(t, big.NewInt(tc.want), got,
				"got %s, want %d", got, tc.want)

		})
	}
}

func TestParseAmountNegativeDecimalsDoNotPanic(t *testing.T) {
	// A negative decimals value must yield an error rather than a
	// slice-bounds panic on fracPart[decimals:].
	assert.NotPanics(t, func() {
		if _, err := ParseAmount("1.5", -1); err == nil {
			t.Error("expected error for negative decimals")
		}
		if _, err := ParseAmount("100", -1); err == nil {
			t.Error("expected error for negative decimals")
		}
	})
}
