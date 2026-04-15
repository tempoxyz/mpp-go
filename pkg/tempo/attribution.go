package tempo

import (
	"encoding/hex"
	"slices"
	"strings"

	"github.com/ethereum/go-ethereum/crypto"
)

const (
	attributionTagSize         = 4
	attributionVersionOffset   = attributionTagSize
	attributionServerOffset    = 5
	attributionServerSize      = 10
	attributionClientOffset    = 15
	attributionClientSize      = 10
	attributionChallengeOffset = 25
	attributionChallengeSize   = 7
	attributionMemoSize        = 32
	attributionMemoHexLength   = attributionMemoSize * 2
)

var attributionTag = crypto.Keccak256([]byte("mpp"))[:4]

const attributionVersion = byte(0x01)

// TODO(tempo-go): move attribution memo encoding and verification into
// tempo-go so Go, Python, and TypeScript SDKs share the same Tempo memo codec.

// Attribution memos match the Tempo charge reference implementations in mppx
// and pympp so challenge binding stays consistent across SDKs.

// EncodeAttribution builds the 32-byte Tempo attribution memo for a Challenge.
func EncodeAttribution(serverID, clientID, challengeID string) string {
	buf := make([]byte, attributionMemoSize)
	copy(buf[:attributionTagSize], attributionTag)
	buf[attributionVersionOffset] = attributionVersion
	copy(buf[attributionServerOffset:attributionServerOffset+attributionServerSize], attributionFingerprint(serverID))
	if clientID != "" {
		copy(buf[attributionClientOffset:attributionClientOffset+attributionClientSize], attributionFingerprint(clientID))
	}
	copy(buf[attributionChallengeOffset:attributionChallengeOffset+attributionChallengeSize], attributionChallengeNonce(challengeID))
	return "0x" + hex.EncodeToString(buf)
}

// IsAttributionMemo reports whether a memo uses the Tempo attribution layout.
func IsAttributionMemo(memo string) bool {
	memo = strings.TrimPrefix(strings.ToLower(memo), "0x")
	if len(memo) != attributionMemoHexLength {
		return false
	}
	raw, err := hex.DecodeString(memo)
	if err != nil {
		return false
	}
	return slices.Equal(raw[:attributionTagSize], attributionTag) && raw[attributionVersionOffset] == attributionVersion
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
	return slices.Equal(raw[attributionServerOffset:attributionServerOffset+attributionServerSize], attributionFingerprint(serverID))
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
	return slices.Equal(raw[attributionChallengeOffset:attributionChallengeOffset+attributionChallengeSize], attributionChallengeNonce(challengeID))
}

func attributionFingerprint(value string) []byte {
	if value == "" {
		return make([]byte, attributionServerSize)
	}
	return crypto.Keccak256([]byte(value))[:attributionServerSize]
}

func attributionChallengeNonce(challengeID string) []byte {
	if challengeID == "" {
		return make([]byte, attributionChallengeSize)
	}
	return crypto.Keccak256([]byte(challengeID))[:attributionChallengeSize]
}
