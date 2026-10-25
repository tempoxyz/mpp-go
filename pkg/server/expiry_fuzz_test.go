package server

import (
	"testing"
	"time"
)

// FuzzParseChallengeExpiry checks the challenge expires parser: it never
// panics, and every instant it accepts survives formatting in the
// millisecond UTC form mpp.Expires emits.
//
//	go test -run='^$' -fuzz='^FuzzParseChallengeExpiry$' -fuzztime=60s ./pkg/server
func FuzzParseChallengeExpiry(f *testing.F) {
	for _, seed := range []string{
		"2026-01-29T12:00:00Z",
		"2026-01-29T12:00:00.123Z",
		"2026-01-29T12:00:00.123456789+05:30",
		"2026-01-29T12:00:00-00:00",
		"0000-01-01T00:00:00+01:00",
		"9999-12-31T23:59:59-01:00",
		"2026-02-30T12:00:00Z",
		"2026-01-29 12:00:00Z",
		"1769688000",
		"",
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, value string) {
		parsed, err := parseChallengeExpiry(value)
		if err != nil {
			return
		}
		utc := parsed.UTC()
		if y := utc.Year(); y < 0 || y > 9999 {
			t.Skip("instant outside RFC 3339's four-digit years after UTC conversion")
		}
		formatted := utc.Format("2006-01-02T15:04:05.000Z")
		again, err := parseChallengeExpiry(formatted)
		if err != nil {
			t.Fatalf("parseChallengeExpiry(%q) failed after formatting %q: %v", formatted, value, err)
		}
		if want := utc.Truncate(time.Millisecond); !again.Equal(want) {
			t.Fatalf("parseChallengeExpiry(%q) = %v, want %v", formatted, again, want)
		}
		if challengeExpired(parsed, parsed.Add(-time.Nanosecond)) || !challengeExpired(parsed, parsed) {
			t.Fatalf("challengeExpired disagrees with its own expiry instant %v", parsed)
		}
	})
}
