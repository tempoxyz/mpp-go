package tempo

import (
	"math"
	"math/big"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/assert"
)

// TestProofTypedDataHashPinnedVector pins the proof digest to fixed values so
// the wire format cannot drift. The digests were produced by go-ethereum
// v1.17.5's signer/core/apitypes.TypedDataAndHash, which ProofTypedDataHash
// used before it was inlined; the inline implementation must match it byte for
// byte.
func TestProofTypedDataHashPinnedVector(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		chainID     int64
		account     string
		challengeID string
		realm       string
		wantDigest  string
	}{
		{
			// account is derived from private key 0x01*32.
			name:        "moderato",
			chainID:     42431,
			account:     "0x1a642f0E3c3aF545E7AcBD38b07251B3990914F1",
			challengeID: "kM9xPqWvT2nJrHsY4aDfEb",
			realm:       "api.example.com",
			wantDigest:  "0x3860a700a55e02ad3c2dc047e92489feceecbdb0a801d948e1d9f0b61ea9bc3f",
		},
		{
			name:        "mainnet",
			chainID:     4217,
			account:     "0x000000000000000000000000000000000000bEEF",
			challengeID: "zero-amount-challenge",
			realm:       "api.example.com",
			wantDigest:  "0xa8e491271cabc98080ff97e4ac9587b8aa0dda2ed60d692e5f63adb1af14e84a",
		},
		{
			name:       "zero values",
			chainID:    0,
			account:    "0x0000000000000000000000000000000000000000",
			wantDigest: "0x68f5aef595811ca9d545f593a3876c0401566f446d345b730b48456f67bfabc0",
		},
		{
			name:        "unicode",
			chainID:     1,
			account:     "0xffffffffffffffffffffffffffffffffffffffff",
			challengeID: "ünïcødé ✓ 🚀",
			realm:       "realm with spaces/and?query=1",
			wantDigest:  "0x48ac8e81ce659e0cbf02fb29d9599f16e33bdd798d4e6c8ac8d1862785cd2674",
		},
		{
			name:        "max chain ID and invalid UTF-8",
			chainID:     math.MaxInt64,
			account:     "0x1a642f0E3c3aF545E7AcBD38b07251B3990914F1",
			challengeID: "x",
			realm:       "\x00\xff",
			wantDigest:  "0x939366571717751f5542aedb839cbb3c8836cdef68ba267fcd3603d442329493",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := ProofTypedDataHash(tt.chainID, common.HexToAddress(tt.account), tt.challengeID, tt.realm)
			assert.NoError(t, err)
			assert.Equalf(t, tt.wantDigest, got.Hex(), "proof digest must match the pinned apitypes value")
		})
	}
}

// TestProofTypedDataHashNegativeChainID checks that a chain ID uint256 cannot
// encode is rejected, as apitypes did.
func TestProofTypedDataHashNegativeChainID(t *testing.T) {
	t.Parallel()

	_, err := ProofTypedDataHash(-1, common.Address{}, "challenge", "realm")
	assert.Error(t, err)
}

// TestProofTypedDataMatchesHash checks that the typed data handed to signers
// describes the same struct types and domain that ProofTypedDataHash hashes.
func TestProofTypedDataMatchesHash(t *testing.T) {
	t.Parallel()

	encodeType := func(td TypedData, name string) string {
		fields := make([]string, 0, len(td.Types[name]))
		for _, f := range td.Types[name] {
			fields = append(fields, f.Type+" "+f.Name)
		}
		return name + "(" + strings.Join(fields, ",") + ")"
	}

	account := common.HexToAddress("0x1a642f0E3c3aF545E7AcBD38b07251B3990914F1")
	td := ProofTypedData(42431, account, "challenge", "api.example.com")
	assert.Equal(t, proofDomainTypeString, encodeType(td, "EIP712Domain"))
	assert.Equal(t, proofTypeString, encodeType(td, td.PrimaryType))
	assert.Equal(t, proofDomainName, td.Domain.Name)
	assert.Equal(t, proofDomainVersion, td.Domain.Version)
	assert.Equal(t, "42431", (*big.Int)(td.Domain.ChainId).String())
	assert.Equal(t, map[string]any{
		"account":     account.Hex(),
		"challengeId": "challenge",
		"realm":       "api.example.com",
	}, td.Message)
}

// TestProofTypedDataHashWalletBinding checks that the digest is bound to the
// payer account: swapping the account yields a different digest.
func TestProofTypedDataHashWalletBinding(t *testing.T) {
	t.Parallel()

	const (
		chainID     = int64(42431)
		challengeID = "kM9xPqWvT2nJrHsY4aDfEb"
		realm       = "api.example.com"
	)
	accountA := common.HexToAddress("0x1a642f0E3c3aF545E7AcBD38b07251B3990914F1")
	accountB := common.HexToAddress("0x000000000000000000000000000000000000bEEF")

	hashA, err := ProofTypedDataHash(chainID, accountA, challengeID, realm)
	assert.NoError(t, err)
	hashB, err := ProofTypedDataHash(chainID, accountB, challengeID, realm)
	assert.NoError(t, err)

	assert.NotEqual(t, hashA.Hex(), hashB.Hex(),
		"different payer accounts must produce different proof digests")
}
