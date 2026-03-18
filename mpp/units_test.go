package mpp

import (
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
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseUnits(tc.value, tc.decimals)
			if tc.wantErr {
				if err == nil {
					t.Errorf("expected error, got %d", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %d, want %d", got, tc.want)
			}
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
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if out["amount"] != "1500000" {
			t.Errorf("amount = %v, want 1500000", out["amount"])
		}
		if _, ok := out["decimals"]; ok {
			t.Error("decimals key should be removed")
		}
		if out["extra"] != "keep" {
			t.Error("extra key should be preserved")
		}
	})

	t.Run("with suggestedDeposit", func(t *testing.T) {
		req := map[string]any{
			"amount":           "0.01",
			"suggestedDeposit": "1.0",
			"decimals":         6,
		}
		out, err := TransformUnits(req)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if out["amount"] != "10000" {
			t.Errorf("amount = %v, want 10000", out["amount"])
		}
		if out["suggestedDeposit"] != "1000000" {
			t.Errorf("suggestedDeposit = %v, want 1000000", out["suggestedDeposit"])
		}
	})

	t.Run("without decimals", func(t *testing.T) {
		req := map[string]any{
			"amount": "100",
		}
		out, err := TransformUnits(req)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if out["amount"] != "100" {
			t.Errorf("amount should be unchanged, got %v", out["amount"])
		}
	})

	t.Run("does not mutate original", func(t *testing.T) {
		req := map[string]any{
			"amount":   "1.0",
			"decimals": 6,
		}
		_, err := TransformUnits(req)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if req["amount"] != "1.0" {
			t.Error("original map was mutated")
		}
		if _, ok := req["decimals"]; !ok {
			t.Error("original map decimals was removed")
		}
	})

	t.Run("float64 decimals from JSON", func(t *testing.T) {
		req := map[string]any{
			"amount":   "1.0",
			"decimals": float64(6),
		}
		out, err := TransformUnits(req)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if out["amount"] != "1000000" {
			t.Errorf("amount = %v, want 1000000", out["amount"])
		}
	})
}
