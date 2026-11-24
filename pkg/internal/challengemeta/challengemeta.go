// Package challengemeta names the opaque metadata keys that Challenge
// issuance reserves, as in mppx's ChallengeMeta, so the server that sets
// them and the clients that compare Challenges strip the same keys.
package challengemeta

// NonceKey is the key of the random issuance nonce, the key every MPP SDK
// uses.
const NonceKey = "_mpp_nonce"

// LegacyNonceKey is the issuance nonce key mppx and mpp-go releases 0.7.0 to
// 0.9.0 set, still accepted on verification.
const LegacyNonceKey = "_mppx_nonce"

// ExpiryGraceKey marks a Challenge whose intent accepts some of its
// Credentials for a while after it expires, as mppx's
// ChallengeMeta.withExpiryGrace. The Challenge HMAC binds it.
const ExpiryGraceKey = "_mppx_expiry_grace"

// ExpiryGraceValue is the value of ExpiryGraceKey on a marked Challenge.
const ExpiryGraceValue = "1"

// ReservedKeys are the keys issuance sets on its own. They are not part of
// the terms a Challenge asks to pay: two Challenges that differ only in them
// accept each other's Credentials.
var ReservedKeys = [...]string{NonceKey, LegacyNonceKey, ExpiryGraceKey}

// IsReserved reports whether key is one of ReservedKeys.
func IsReserved(key string) bool {
	for _, reserved := range ReservedKeys {
		if key == reserved {
			return true
		}
	}
	return false
}
