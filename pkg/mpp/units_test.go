package mpp

import (
	"github.com/stretchr/testify/assert"
	"testing"
)

func TestParseUnits(t *testing.T) {
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
		{"invalid", "abc", 6, 0, true},
		{"fractional base units", "1.0000005", 6, 0, true},
		{"too many decimals", "0.0000001", 6, 0, true},
		{"negative decimals", "1.5", -1, 0, true},
		{"integer with negative decimals", "100", -1, 0, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseUnits(tc.value, tc.decimals)
			if tc.wantErr {
				assert.Errorf(t, err,
					"expected error, got %d", got)

				return
			}
			if !assert.NoErrorf(t, err,
				"unexpected error: %v", err) {
				return
			}
			assert.Equalf(t, tc.want, got,
				"got %d, want %d", got, tc.want)

		})
	}
}

func TestTransformUnits(t *testing.T) {
	t.Run("with decimals", func(t *testing.T) {
		req := map[string]any{
			"amount":   "1.5",
			"decimals": 6,
			"extra":    "keep",
		}
		out, err := TransformUnits(req)
		if !assert.NoErrorf(t, err,
			"unexpected error: %v", err) {
			return
		}
		assert.Equalf(t, "1500000", out["amount"],
			"amount = %v, want 1500000", out["amount"])
		{

			_, ok := out["decimals"]
			assert.False(t, ok,
				"decimals key should be removed")
		}
		assert.Equal(t, "keep", out["extra"],
			"extra key should be preserved")

	})

	t.Run("with suggestedDeposit", func(t *testing.T) {
		req := map[string]any{
			"amount":           "0.01",
			"suggestedDeposit": "1.0",
			"decimals":         6,
		}
		out, err := TransformUnits(req)
		if !assert.NoErrorf(t, err,
			"unexpected error: %v", err) {
			return
		}
		assert.Equalf(t, "10000", out["amount"],
			"amount = %v, want 10000", out["amount"])
		assert.Equalf(t, "1000000", out["suggestedDeposit"],
			"suggestedDeposit = %v, want 1000000", out["suggestedDeposit"])

	})

	t.Run("without decimals", func(t *testing.T) {
		req := map[string]any{
			"amount": "100",
		}
		out, err := TransformUnits(req)
		if !assert.NoErrorf(t, err,
			"unexpected error: %v", err) {
			return
		}
		assert.Equalf(t, "100", out["amount"],
			"amount should be unchanged, got %v", out["amount"])

	})

	t.Run("does not mutate original", func(t *testing.T) {
		req := map[string]any{
			"amount":   "1.0",
			"decimals": 6,
		}
		_, err := TransformUnits(req)
		if !assert.NoErrorf(t, err,
			"unexpected error: %v", err) {
			return
		}
		assert.Equal(t, "1.0", req["amount"],
			"original map was mutated")
		{

			_, ok := req["decimals"]
			assert.True(t, ok,
				"original map decimals was removed")
		}

	})

	t.Run("float64 decimals from JSON", func(t *testing.T) {
		req := map[string]any{
			"amount":   "1.0",
			"decimals": float64(6),
		}
		out, err := TransformUnits(req)
		if !assert.NoErrorf(t, err,
			"unexpected error: %v", err) {
			return
		}
		assert.Equalf(t, "1000000", out["amount"],
			"amount = %v, want 1000000", out["amount"])

	})

	t.Run("negative decimals returns error without panicking", func(t *testing.T) {
		req := map[string]any{
			"amount":   "1.5",
			"decimals": -1,
		}
		assert.NotPanics(t, func() {
			_, err := TransformUnits(req)
			assert.Error(t, err, "negative decimals must return an error")
		})
	})
}

func TestParseUnitsNegativeDecimalsDoNotPanic(t *testing.T) {
	// A negative decimals value must yield an error rather than a
	// slice-bounds panic on fracPart[decimals:].
	assert.NotPanics(t, func() {
		if _, err := ParseUnits("1.5", -1); err == nil {
			t.Error("expected error for negative decimals")
		}
		if _, err := ParseUnits("100", -1); err == nil {
			t.Error("expected error for negative decimals")
		}
	})
}
