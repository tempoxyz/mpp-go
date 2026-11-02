package chargeserver

import (
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	temposigner "github.com/tempoxyz/tempo-go/pkg/signer"

	"github.com/tempoxyz/mpp-go/pkg/tempo"
)

type proofKeySigner interface {
	SignEnvelope(hash common.Hash) ([]byte, error)
}

func p256AndWebAuthnSigners(t *testing.T) map[string]struct {
	signer  proofKeySigner
	address common.Address
} {
	t.Helper()
	const key = "0x4444444444444444444444444444444444444444444444444444444444444444"
	p256, err := temposigner.NewP256Signer(key)
	require.NoError(t, err)
	webAuthn, err := temposigner.NewWebAuthnSigner(key, "api.example.com", "https://api.example.com")
	require.NoError(t, err)
	return map[string]struct {
		signer  proofKeySigner
		address common.Address
	}{
		"p256":     {p256, p256.Address()},
		"webauthn": {webAuthn, p256.Address()},
	}
}

// A P256 or WebAuthn key proves for itself, or for its root account inside a
// Keychain V2 envelope, as viem signs it.
func TestP256AndWebAuthnProofsVerify(t *testing.T) {
	root := common.HexToAddress("0x00000000000000000000000000000000000000aa")
	other := common.HexToAddress("0x00000000000000000000000000000000000000bb")
	hash, err := tempo.ProofTypedDataHash(42431, root, "challenge-1", "api.example.com")
	require.NoError(t, err)
	wrapped := crypto.Keccak256Hash([]byte{0x04}, hash.Bytes(), root.Bytes())
	for name, key := range p256AndWebAuthnSigners(t) {
		t.Run(name, func(t *testing.T) {
			direct, err := key.signer.SignEnvelope(hash)
			require.NoError(t, err)
			signer, err := recoverProofSigner(hash, hexutil.Encode(direct), key.address)
			require.NoError(t, err)
			assert.Equal(t, key.address, signer)

			inner, err := key.signer.SignEnvelope(wrapped)
			require.NoError(t, err)
			envelope := append(append([]byte{0x04}, root.Bytes()...), inner...)
			signer, err = recoverProofSigner(hash, hexutil.Encode(envelope), root)
			require.NoError(t, err)
			assert.Equal(t, key.address, signer)

			_, err = recoverProofSigner(hash, hexutil.Encode(envelope), other)
			assert.Error(t, err)
			_, err = recoverProofSigner(crypto.Keccak256Hash([]byte("other")), hexutil.Encode(envelope), root)
			assert.Error(t, err)
		})
	}
}
