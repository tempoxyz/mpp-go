package tempo

import (
	"encoding/hex"
	"slices"
	"strings"

	"github.com/ethereum/go-ethereum/crypto"
)

var attributionTag = crypto.Keccak256([]byte("mpp"))[:4]

const attributionVersion = byte(0x01)

// Attribution memos match the first-pass Tempo charge implementations in mppx
// and pympp so Challenge binding stays consistent across SDKs.

// EncodeAttribution builds the 32-byte Tempo attribution memo for a Challenge.
func EncodeAttribution(serverID, clientID, challengeID string) string {
	buf := make([]byte, 32)
	copy(buf[0:4], attributionTag)
	buf[4] = attributionVersion
	copy(buf[5:15], attributionFingerprint(serverID))
	if clientID != "" {
		copy(buf[15:25], attributionFingerprint(clientID))
	}
	copy(buf[25:32], attributionChallengeNonce(challengeID))
	return "0x" + hex.EncodeToString(buf)
}

// IsAttributionMemo reports whether a memo uses the Tempo attribution layout.
func IsAttributionMemo(memo string) bool {
	memo = strings.TrimPrefix(strings.ToLower(memo), "0x")
	if len(memo) != 64 {
		return false
	}
	raw, err := hex.DecodeString(memo)
	if err != nil {
		return false
	}
	return slices.Equal(raw[0:4], attributionTag) && raw[4] == attributionVersion
}

// VerifyAttributionServer reports whether a memo matches the Challenge realm fingerprint.
func VerifyAttributionServer(memo, serverID string) bool {
	memo = strings.TrimPrefix(strings.ToLower(memo), "0x")
	if !IsAttributionMemo(memo) {
		return false
	}
	raw, err := hex.DecodeString(memo)
	if err != nil {
		return false
	}
	return slices.Equal(raw[5:15], attributionFingerprint(serverID))
}

// VerifyAttributionChallenge reports whether a memo matches a specific Challenge ID.
func VerifyAttributionChallenge(memo, challengeID string) bool {
	memo = strings.TrimPrefix(strings.ToLower(memo), "0x")
	if !IsAttributionMemo(memo) || challengeID == "" {
		return false
	}
	raw, err := hex.DecodeString(memo)
	if err != nil {
		return false
	}
	return slices.Equal(raw[25:32], attributionChallengeNonce(challengeID))
}

func attributionFingerprint(value string) []byte {
	if value == "" {
		return make([]byte, 10)
	}
	return crypto.Keccak256([]byte(value))[:10]
}

func attributionChallengeNonce(challengeID string) []byte {
	if challengeID == "" {
		return make([]byte, 7)
	}
	return crypto.Keccak256([]byte(challengeID))[:7]
}
