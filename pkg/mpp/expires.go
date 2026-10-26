package mpp

import (
	"fmt"
	"time"
)

// ExpiresIn returns the RFC 3339 timestamp d from now, in UTC with
// millisecond precision and a Z suffix, as mppx's Expires helpers emit. Use it
// for a challenge's expires parameter:
//
//	mpp.WithExpires(mpp.ExpiresIn(5 * time.Minute))
func ExpiresIn(d time.Duration) string {
	return formatISO(time.Now().UTC().Add(d))
}

// ParseExpires parses an expires timestamp in the RFC 3339 format challenges
// use, with or without fractional seconds.
func ParseExpires(s string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("mpp: invalid expires timestamp: %w", err)
	}
	return t, nil
}

// CheckExpires checks a challenge's expires parameter at now, as mppx's
// Expires.assert does. It returns an invalid-challenge PaymentError when
// expires is empty or malformed, and ErrPaymentExpired when now is at or after
// it: a challenge is no longer valid at its exact expiry instant.
func CheckExpires(expires string, now time.Time) error {
	if expires == "" {
		return newPaymentError(ErrorTypeInvalidChallenge, "missing required expires field")
	}
	t, err := ParseExpires(expires)
	if err != nil {
		return newPaymentError(ErrorTypeInvalidChallenge, "malformed expires timestamp").WithCause(err)
	}
	if !now.Before(t) {
		return ErrPaymentExpired(expires)
	}
	return nil
}

func formatISO(t time.Time) string {
	return t.Format("2006-01-02T15:04:05.000Z")
}
