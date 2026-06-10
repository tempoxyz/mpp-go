package tempo

import (
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/assert"
)

// TestProofTypedDataHashCrossSDKVector pins the proof digest to a fixed vector
// so the wire format cannot drift.
func TestProofTypedDataHashCrossSDKVector(t *testing.T) {
	t.Parallel()

	const (
		chainID     = int64(42431)
		challengeID = "kM9xPqWvT2nJrHsY4aDfEb"
		realm       = "api.example.com"
		// account is derived from private key 0x01*32; wantDigest is the
		// corresponding pinned proof digest.
		account    = "0x1a642f0E3c3aF545E7AcBD38b07251B3990914F1"
		wantDigest = "0x3860a700a55e02ad3c2dc047e92489feceecbdb0a801d948e1d9f0b61ea9bc3f"
	)

	got, err := ProofTypedDataHash(chainID, common.HexToAddress(account), challengeID, realm)
	assert.NoError(t, err)
	assert.Equalf(t, wantDigest, got.Hex(), "proof digest must match the pinned vector")
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
