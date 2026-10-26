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
// A default port (443 for https, 80 for http) is dropped, so https://h and
// https://h:443 have the same origin.
func Origin(u *url.URL) string {
	if u == nil {
		return ""
	}
	scheme := strings.ToLower(u.Scheme)
	host := strings.ToLower(u.Host)
	switch {
	case scheme == "https" && strings.HasSuffix(host, ":443"):
		host = strings.TrimSuffix(host, ":443")
	case scheme == "http" && strings.HasSuffix(host, ":80"):
		host = strings.TrimSuffix(host, ":80")
	}
	return scheme + "://" + host
}

// NormalizeOrigin lowercases a configured origin, drops a trailing slash and
// a default port, so it compares equal to Origin of a URL at that origin.
func NormalizeOrigin(origin string) string {
	normalized := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(origin)), "/")
	if u, err := url.Parse(normalized); err == nil && u.Scheme != "" && u.Host != "" && u.Path == "" {
		return Origin(u)
	}
	return normalized
}

// RealmError is the error for a proof challenge whose realm the sender does
// not own. origin is "" when the transport has none, such as MCP.
func RealmError(realm, origin, hint string) error {
	if origin == "" {
		return fmt.Errorf("mpp: refusing to sign Tempo proof for untrusted realm %q; %s", realm, hint)
	}
	return fmt.Errorf("mpp: refusing to sign Tempo proof for realm %q from %q", realm, origin)
}
