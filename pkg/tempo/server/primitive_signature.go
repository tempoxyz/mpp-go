package chargeserver

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

// Tempo primitive signature encodings (tempo-primitives tt_signature.rs):
// a 65-byte secp256k1 signature, or a type byte followed by its data.
const (
	signatureTypeP256Byte     = 0x01
	signatureTypeWebAuthnByte = 0x02

	// p256SignatureLength is r || s || pubKeyX || pubKeyY || preHash.
	p256SignatureLength = 129
	// maxWebAuthnSignatureLength bounds webauthnData || r || s || x || y.
	maxWebAuthnSignatureLength = 2048
	minAuthenticatorDataLength = 37

	webAuthnFlagUP = 0x01
	webAuthnFlagUV = 0x04
	webAuthnFlagAT = 0x40
	webAuthnFlagED = 0x80
)

// p256HalfOrder is n/2 for P-256: Tempo rejects high-s P256 signatures.
var p256HalfOrder = new(big.Int).Rsh(elliptic.P256().Params().N, 1)

// recoverPrimitiveSigner verifies a Tempo primitive signature over hash and
// returns its signer: the secp256k1 recovered address, or the address of the
// P256 public key a P256 or WebAuthn signature carries, as Tempo's
// PrimitiveSignature::recover_signer (and ox's SignatureEnvelope) do.
func recoverPrimitiveSigner(hash common.Hash, raw []byte) (common.Address, error) {
	if len(raw) == 65 {
		return recoverSecp256k1(hash, raw)
	}
	if len(raw) < 2 {
		return common.Address{}, errors.New("signature is too short")
	}
	data := raw[1:]
	switch raw[0] {
	case signatureTypeP256Byte:
		if len(data) != p256SignatureLength {
			return common.Address{}, errors.New("invalid P256 signature length")
		}
		message := hash.Bytes()
		if data[128] != 0 {
			digest := sha256.Sum256(message)
			message = digest[:]
		}
		return verifyP256(message, data[0:32], data[32:64], data[64:96], data[96:128])
	case signatureTypeWebAuthnByte:
		if len(data) < 128 || len(data) > maxWebAuthnSignatureLength {
			return common.Address{}, errors.New("invalid WebAuthn signature length")
		}
		webAuthnData, sig := data[:len(data)-128], data[len(data)-128:]
		message, err := webAuthnMessageHash(webAuthnData, hash)
		if err != nil {
			return common.Address{}, err
		}
		return verifyP256(message, sig[0:32], sig[32:64], sig[64:96], sig[96:128])
	default:
		return common.Address{}, errors.New("unknown signature type")
	}
}

// verifyP256 checks a low-s P256 signature over a 32-byte digest and returns
// the address of the public key: the last 20 bytes of keccak256(x || y).
func verifyP256(digest, r, s, x, y []byte) (common.Address, error) {
	rInt, sInt := new(big.Int).SetBytes(r), new(big.Int).SetBytes(s)
	if sInt.Cmp(p256HalfOrder) > 0 {
		return common.Address{}, errors.New("P256 signature has high s value")
	}
	key, err := ecdsa.ParseUncompressedPublicKey(elliptic.P256(), append(append([]byte{0x04}, x...), y...))
	if err != nil {
		return common.Address{}, errors.New("invalid P256 public key")
	}
	if !ecdsa.Verify(key, digest, rInt, sInt) {
		return common.Address{}, errors.New("P256 signature verification failed")
	}
	return common.BytesToAddress(crypto.Keccak256(x, y)[12:]), nil
}

// webAuthnMessageHash validates WebAuthn assertion data for challenge and
// returns sha256(authenticatorData || sha256(clientDataJSON)), as Tempo does:
// UP or UV set, AT and ED unset (so authenticatorData is exactly 37 bytes),
// clientDataJSON type "webauthn.get" and challenge base64url(challenge).
func webAuthnMessageHash(data []byte, challenge common.Hash) ([]byte, error) {
	if len(data) < minAuthenticatorDataLength+32 {
		return nil, errors.New("WebAuthn data is too short")
	}
	flags := data[32]
	if flags&(webAuthnFlagUP|webAuthnFlagUV) == 0 {
		return nil, errors.New("WebAuthn user presence flag is not set")
	}
	if flags&webAuthnFlagAT != 0 || flags&webAuthnFlagED != 0 {
		return nil, errors.New("WebAuthn assertion must not carry attested or extension data")
	}
	authenticatorData, clientDataJSON := data[:minAuthenticatorDataLength], data[minAuthenticatorDataLength:]
	var clientData struct {
		Type      string `json:"type"`
		Challenge string `json:"challenge"`
	}
	if err := json.Unmarshal(clientDataJSON, &clientData); err != nil {
		return nil, errors.New("WebAuthn clientDataJSON is not valid JSON")
	}
	if clientData.Type != "webauthn.get" {
		return nil, errors.New("WebAuthn clientDataJSON type must be webauthn.get")
	}
	if clientData.Challenge != base64.RawURLEncoding.EncodeToString(challenge.Bytes()) {
		return nil, errors.New("WebAuthn challenge does not match")
	}
	clientDataHash := sha256.Sum256(clientDataJSON)
	message := sha256.Sum256(bytes.Join([][]byte{authenticatorData, clientDataHash[:]}, nil))
	return message[:], nil
}
