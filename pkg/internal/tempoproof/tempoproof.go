// Package tempoproof holds the rule shared by the HTTP and MCP clients and
// the Tempo charge client for zero-amount Tempo proofs: a proof
// authenticates the payer at the realm the challenge names, so a client signs
// one only for a realm the server that sent the challenge owns.
package tempoproof

import (
	"fmt"
	"math/big"
	"net/url"
	"strings"

	"github.com/tempoxyz/mpp-go/pkg/mpp"
)

// IsProofChallenge reports whether challenge is a zero-amount tempo/charge
// challenge, which a client pays with a proof signed for its realm.
func IsProofChallenge(challenge *mpp.Challenge) bool {
	return challenge != nil && challenge.Method == "tempo" && challenge.Intent == mpp.IntentCharge &&
		IsZeroAmount(challenge.Request["amount"])
}

// IsZeroAmount reports whether value is a base-10 integer amount equal to zero.
func IsZeroAmount(value any) bool {
	amount := strings.TrimSpace(fmt.Sprint(value))
	if amount == "" {
		return false
	}
	for _, digit := range amount {
		if digit < '0' || digit > '9' {
			return false
		}
	}
	parsed, ok := new(big.Int).SetString(amount, 10)
	return ok && parsed.Sign() == 0
}

// OriginOwnsRealm reports whether u is an HTTPS URL on the default port
// whose hostname is realm, compared case-insensitively.
func OriginOwnsRealm(u *url.URL, realm string) bool {
	return u != nil && u.Scheme == "https" && (u.Port() == "" || u.Port() == "443") &&
		strings.EqualFold(realm, u.Hostname())
}

// Origin returns the lowercase scheme://host[:port] origin of u, or "" for nil.
func Origin(u *url.URL) string {
	if u == nil {
		return ""
	}
	return strings.ToLower(u.Scheme) + "://" + strings.ToLower(u.Host)
}

// NormalizeOrigin lowercases a configured origin and drops a trailing slash,
// so it compares equal to Origin of a URL at that origin.
func NormalizeOrigin(origin string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(origin)), "/")
}

// RealmError is the error for a proof challenge whose realm the sender does
// not own. origin is "" when the transport has none, such as MCP.
func RealmError(realm, origin, hint string) error {
	if origin == "" {
		return fmt.Errorf("mpp: refusing to sign Tempo proof for untrusted realm %q; %s", realm, hint)
	}
	return fmt.Errorf("mpp: refusing to sign Tempo proof for realm %q from %q", realm, origin)
}
