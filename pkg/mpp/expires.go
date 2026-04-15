package mpp

import "time"

// Expires provides helpers for generating ISO 8601 expiration timestamps
// with millisecond precision and Z suffix.
var Expires = struct {
	Seconds func(n int) string
	Minutes func(n int) string
	Hours   func(n int) string
	Days    func(n int) string
	Weeks   func(n int) string
}{
	Seconds: func(n int) string { return formatISO(time.Now().UTC().Add(time.Duration(n) * time.Second)) },
	Minutes: func(n int) string { return formatISO(time.Now().UTC().Add(time.Duration(n) * time.Minute)) },
	Hours:   func(n int) string { return formatISO(time.Now().UTC().Add(time.Duration(n) * time.Hour)) },
	Days:    func(n int) string { return formatISO(time.Now().UTC().Add(time.Duration(n) * 24 * time.Hour)) },
	Weeks:   func(n int) string { return formatISO(time.Now().UTC().Add(time.Duration(n) * 7 * 24 * time.Hour)) },
}

func formatISO(t time.Time) string {
	return t.Format("2006-01-02T15:04:05.000Z")
}
