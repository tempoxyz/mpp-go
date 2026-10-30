package chargeserver

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tempoxyz/mpp-go/pkg/tempo"
)

func newP256Key(t *testing.T) (*ecdsa.PrivateKey, common.Address) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	public, err := key.PublicKey.Bytes()
	require.NoError(t, err)
	return key, common.BytesToAddress(crypto.Keccak256(public[1:])[12:])
}

// signP256 signs digest with a low-s signature and returns r || s || x || y.
func signP256(t *testing.T, key *ecdsa.PrivateKey, digest []byte) []byte {
	t.Helper()
	r, s, err := ecdsa.Sign(rand.Reader, key, digest)
	require.NoError(t, err)
	if s.Cmp(p256HalfOrder) > 0 {
		s = new(big.Int).Sub(elliptic.P256().Params().N, s)
	}
	out := make([]byte, 0, 128)
	out = append(out, r.FillBytes(make([]byte, 32))...)
	out = append(out, s.FillBytes(make([]byte, 32))...)
	public, err := key.PublicKey.Bytes()
	require.NoError(t, err)
	return append(out, public[1:]...)
}

func signedP256Envelope(t *testing.T, key *ecdsa.PrivateKey, hash common.Hash, preHash bool) []byte {
	digest := hash.Bytes()
	flag := byte(0)
	if preHash {
		d := sha256.Sum256(digest)
		digest, flag = d[:], 1
	}
	return append(append([]byte{signatureTypeP256Byte}, signP256(t, key, digest)...), flag)
}

func signedWebAuthnEnvelope(t *testing.T, key *ecdsa.PrivateKey, hash common.Hash, challengeType string) []byte {
	authenticatorData := append(make([]byte, 32), webAuthnFlagUP|webAuthnFlagUV, 0, 0, 0, 1)
	clientDataJSON, err := json.Marshal(map[string]any{
		"type":      challengeType,
		"challenge": base64.RawURLEncoding.EncodeToString(hash.Bytes()),
		"origin":    "https://wallet.example",
	})
	require.NoError(t, err)
	clientDataHash := sha256.Sum256(clientDataJSON)
	message := sha256.Sum256(append(append([]byte{}, authenticatorData...), clientDataHash[:]...))
	out := append([]byte{signatureTypeWebAuthnByte}, authenticatorData...)
	out = append(out, clientDataJSON...)
	return append(out, signP256(t, key, message[:])...)
}

func TestRecoverPrimitiveSignerP256AndWebAuthn(t *testing.T) {
	key, address := newP256Key(t)
	hash := crypto.Keccak256Hash([]byte("proof"))
	for name, envelope := range map[string][]byte{
		"p256":         signedP256Envelope(t, key, hash, false),
		"p256 prehash": signedP256Envelope(t, key, hash, true),
		"webauthn":     signedWebAuthnEnvelope(t, key, hash, "webauthn.get"),
	} {
		t.Run(name, func(t *testing.T) {
			signer, err := recoverPrimitiveSigner(hash, envelope)
			require.NoError(t, err)
			assert.Equal(t, address, signer)
			_, err = recoverPrimitiveSigner(crypto.Keccak256Hash([]byte("other")), envelope)
			assert.Error(t, err)
		})
	}
	_, err := recoverPrimitiveSigner(hash, signedWebAuthnEnvelope(t, key, hash, "webauthn.create"))
	assert.ErrorContains(t, err, "webauthn.get")
}

func TestRecoverPrimitiveSignerRejectsHighS(t *testing.T) {
	key, _ := newP256Key(t)
	hash := crypto.Keccak256Hash([]byte("proof"))
	envelope := signedP256Envelope(t, key, hash, false)
	s := new(big.Int).SetBytes(envelope[33:65])
	new(big.Int).Sub(elliptic.P256().Params().N, s).FillBytes(envelope[33:65])
	_, err := recoverPrimitiveSigner(hash, envelope)
	assert.ErrorContains(t, err, "high s")
}

// A P256 or WebAuthn access key proves for its root account inside a
// Keychain V2 envelope, as viem signs it.
func TestP256AccessKeyProofVerifies(t *testing.T) {
	key, keyAddress := newP256Key(t)
	root := common.HexToAddress("0x00000000000000000000000000000000000000aa")
	hash, err := tempo.ProofTypedDataHash(42431, root, "challenge-1", "api.example.com")
	require.NoError(t, err)
	wrapped := crypto.Keccak256Hash([]byte{0x04}, hash.Bytes(), root.Bytes())
	for name, inner := range map[string][]byte{
		"p256":     signedP256Envelope(t, key, wrapped, false),
		"webauthn": signedWebAuthnEnvelope(t, key, wrapped, "webauthn.get"),
	} {
		t.Run(name, func(t *testing.T) {
			envelope := append(append([]byte{0x04}, root.Bytes()...), inner...)
			signer, err := recoverProofSigner(hash, hexutil.Encode(envelope), root)
			require.NoError(t, err)
			assert.Equal(t, keyAddress, signer)
			_, err = recoverProofSigner(hash, hexutil.Encode(envelope), common.HexToAddress("0x00000000000000000000000000000000000000bb"))
			assert.Error(t, err)
		})
	}
}
