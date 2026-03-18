package mpp

import (
	"testing"
	"time"
)

func TestExpires_Format(t *testing.T) {
	result := Expires.Seconds(60)

	// Parse the result back.
	parsed, err := time.Parse("2006-01-02T15:04:05.000Z", result)
	if err != nil {
		t.Fatalf("failed to parse result %q: %v", result, err)
	}

	// Should be approximately 60 seconds from now.
	diff := time.Until(parsed)
	if diff < 59*time.Second || diff > 61*time.Second {
		t.Errorf("expected ~60s from now, got %v", diff)
	}
}

func TestExpires_EndsWithZ(t *testing.T) {
	for _, tc := range []struct {
		name string
		fn   func(int) string
	}{
		{"Seconds", Expires.Seconds},
		{"Minutes", Expires.Minutes},
		{"Hours", Expires.Hours},
		{"Days", Expires.Days},
		{"Weeks", Expires.Weeks},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := tc.fn(1)
			if result[len(result)-1] != 'Z' {
				t.Errorf("expected Z suffix, got %q", result)
			}
		})
	}
}

func TestExpires_HasMilliseconds(t *testing.T) {
	result := Expires.Seconds(1)
	// Format: 2006-01-02T15:04:05.000Z — the .000 is 3 digits.
	if len(result) != 24 {
		t.Errorf("expected 24 char ISO string, got %d: %q", len(result), result)
	}
}

func TestExpires_Minutes(t *testing.T) {
	result := Expires.Minutes(5)
	parsed, err := time.Parse("2006-01-02T15:04:05.000Z", result)
	if err != nil {
		t.Fatalf("failed to parse: %v", err)
	}
	diff := time.Until(parsed)
	if diff < 4*time.Minute+59*time.Second || diff > 5*time.Minute+1*time.Second {
		t.Errorf("expected ~5min from now, got %v", diff)
	}
}

func TestExpires_Hours(t *testing.T) {
	result := Expires.Hours(2)
	parsed, err := time.Parse("2006-01-02T15:04:05.000Z", result)
	if err != nil {
		t.Fatalf("failed to parse: %v", err)
	}
	diff := time.Until(parsed)
	if diff < 1*time.Hour+59*time.Minute || diff > 2*time.Hour+1*time.Minute {
		t.Errorf("expected ~2h from now, got %v", diff)
	}
}

func TestExpires_Days(t *testing.T) {
	result := Expires.Days(1)
	parsed, err := time.Parse("2006-01-02T15:04:05.000Z", result)
	if err != nil {
		t.Fatalf("failed to parse: %v", err)
	}
	diff := time.Until(parsed)
	if diff < 23*time.Hour+59*time.Minute || diff > 24*time.Hour+1*time.Minute {
		t.Errorf("expected ~24h from now, got %v", diff)
	}
}

func TestExpires_Weeks(t *testing.T) {
	result := Expires.Weeks(1)
	parsed, err := time.Parse("2006-01-02T15:04:05.000Z", result)
	if err != nil {
		t.Fatalf("failed to parse: %v", err)
	}
	diff := time.Until(parsed)
	expected := 7 * 24 * time.Hour
	if diff < expected-1*time.Minute || diff > expected+1*time.Minute {
		t.Errorf("expected ~1 week from now, got %v", diff)
	}
}
