package chargeserver

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/tempoxyz/tempo-go/pkg/keychain"
	temposigner "github.com/tempoxyz/tempo-go/pkg/signer"
	tempotx "github.com/tempoxyz/tempo-go/pkg/transaction"
)

// Tempo signature envelope type bytes (ox SignatureEnvelope).
const (
	envelopeTypeP256       = 0x01
	envelopeTypeWebAuthn   = 0x02
	envelopeTypeKeychainV1 = keychainV1SignatureType
	envelopeTypeKeychainV2 = keychain.KeychainSignatureType
	envelopeTypeMultisig   = 0x05

	signatureTypeKeychain = "keychain"
	signatureTypeMultisig = "multisig"

	p256EnvelopeDataLength = 129
	webAuthnMinDataLength  = 128
	webAuthnMaxDataLength  = 2048
)

// feePayerTrailerMarker ends the "<sender> feefeefeefee" trailer tempo.ts and
// this SDK's client append to a sponsored 0x76 transaction.
var feePayerTrailerMarker = []byte{0xfe, 0xef, 0xee, 0xfe, 0xef, 0xee}

// stripFeePayerTrailer removes the sponsored-transaction trailer when the
// bytes before it form one complete RLP list, as tempo-go does.
func stripFeePayerTrailer(body []byte) []byte {
	trailer := common.AddressLength + len(feePayerTrailerMarker)
	if len(body) < trailer || !bytes.HasSuffix(body, feePayerTrailerMarker) {
		return body
	}
	candidate := body[:len(body)-trailer]
	if _, rest, err := rlp.SplitList(candidate); err != nil || len(rest) != 0 {
		return body
	}
	return candidate
}

// deserializeTransactionCredential accepts both the broadcast transaction
// envelope (0x76) and the fee-payer signing form (0x78), as mppx does.
//
// Like mppx's prepareSponsoredTransaction it keeps every sender signature
// envelope Tempo defines (secp256k1, P256, WebAuthn, Keychain V1 and V2 with
// any inner signature, and multisig) and a keyAuthorization, and refuses a
// non-empty authorizationList. tempo-go v0.5 decodes only secp256k1, P256,
// WebAuthn and Keychain V2 envelopes with a secp256k1 inner signature, and
// neither the 0x78 form nor field 12, so the body is checked and normalized
// here before delegating the remaining decoding; any other envelope is carried
// through as raw bytes.
func deserializeTransactionCredential(serialized string) (*tempotx.Tx, bool, error) {
	hexValue := serialized
	if strings.HasPrefix(hexValue, "0x") || strings.HasPrefix(hexValue, "0X") {
		hexValue = hexValue[2:]
	}
	encoded, err := hex.DecodeString(hexValue)
	if err != nil {
		return nil, false, fmt.Errorf("decode transaction: %w", err)
	}
	if len(encoded) < 2 {
		return nil, false, fmt.Errorf("transaction is too short")
	}
	prefix := encoded[0]
	if prefix != 0x76 && prefix != 0x78 {
		return nil, false, fmt.Errorf("unsupported transaction prefix 0x%02x", prefix)
	}
	feePayerForm := prefix == 0x78
	body := encoded[1:]
	if !feePayerForm {
		body = stripFeePayerTrailer(body)
	}
	var fields []rlp.RawValue
	if err := rlp.DecodeBytes(body, &fields); err != nil {
		return nil, false, fmt.Errorf("decode transaction body: %w", err)
	}
	if len(fields) != 13 && len(fields) != 14 && len(fields) != 15 {
		return nil, false, fmt.Errorf("invalid transaction field count %d", len(fields))
	}
	if kind, content, _, err := rlp.Split(fields[12]); err != nil || kind != rlp.List || len(content) != 0 {
		return nil, false, fmt.Errorf("transaction authorizationList is not supported")
	}

	var sender common.Address
	if feePayerForm {
		var senderBytes []byte
		if err := rlp.DecodeBytes(fields[11], &senderBytes); err != nil {
			return nil, false, fmt.Errorf("decode fee-payer transaction sender: %w", err)
		}
		if len(senderBytes) != common.AddressLength {
			return nil, false, fmt.Errorf("invalid fee-payer transaction sender length %d", len(senderBytes))
		}
		sender = common.BytesToAddress(senderBytes)
		if sender == (common.Address{}) {
			return nil, false, fmt.Errorf("fee-payer transaction sender is required")
		}
		marker, err := rlp.EncodeToBytes([]byte{0})
		if err != nil {
			return nil, false, fmt.Errorf("encode fee-payer marker: %w", err)
		}
		fields[11] = marker
	}

	var rawEnvelope []byte
	if len(fields) >= 14 {
		last := len(fields) - 1
		if kind, content, _, err := rlp.Split(fields[last]); err == nil && kind == rlp.String && carriedAsRaw(content) {
			if _, err := envelopeSigner(content); err != nil {
				return nil, false, err
			}
			rawEnvelope = append([]byte(nil), content...)
			// A well-formed secp256k1 placeholder lets tempo-go decode the
			// other fields; the envelope is restored below.
			placeholder := make([]byte, 65)
			placeholder[31], placeholder[63] = 1, 1
			if fields[last], err = rlp.EncodeToBytes(placeholder); err != nil {
				return nil, false, err
			}
		}
	}
	normalized, err := rlp.EncodeToBytes(fields)
	if err != nil {
		return nil, false, fmt.Errorf("encode transaction body: %w", err)
	}
	tx, err := tempotx.Deserialize("0x76" + hex.EncodeToString(normalized))
	if err != nil {
		return nil, false, err
	}
	if rawEnvelope != nil {
		kind := signatureTypeKeychain
		if rawEnvelope[0] == envelopeTypeMultisig {
			kind = signatureTypeMultisig
		}
		tx.Signature = &temposigner.SignatureEnvelope{Type: kind, Raw: rawEnvelope}
	}
	if feePayerForm {
		tx.AwaitingFeePayer = true
		tx.From = sender
	}
	return tx, feePayerForm, nil
}

// carriedAsRaw reports whether tempo-go v0.5 cannot decode a sender signature
// envelope, which this package then carries as raw bytes: a Keychain V1, a
// Keychain V2 whose inner signature is not secp256k1, or a multisig envelope.
func carriedAsRaw(envelope []byte) bool {
	if len(envelope) == 65 || len(envelope) == 0 {
		return false
	}
	switch envelope[0] {
	case envelopeTypeKeychainV1, envelopeTypeMultisig:
		return true
	case envelopeTypeKeychainV2:
		return len(envelope) != keychain.KeychainSignatureLength
	default:
		return false
	}
}

// envelopeSigner returns the account a sender signature envelope other than
// secp256k1 names, as ox's SignatureEnvelope.extractAddress does: the
// address of a P256 or WebAuthn public key, the user account of a Keychain
// envelope, or a multisig account. Like mppx, it does not verify the P256,
// WebAuthn or multisig signatures themselves; the chain does when the
// transaction is broadcast, and an invalid one is never included.
func envelopeSigner(envelope []byte) (common.Address, error) {
	if len(envelope) < 2 {
		return common.Address{}, fmt.Errorf("signature envelope is too short")
	}
	data := envelope[1:]
	switch envelope[0] {
	case envelopeTypeP256:
		if len(data) != p256EnvelopeDataLength {
			return common.Address{}, fmt.Errorf("invalid P256 signature length %d", len(data))
		}
		return publicKeyAddress(data[64:128]), nil
	case envelopeTypeWebAuthn:
		if len(data) < webAuthnMinDataLength || len(data) > webAuthnMaxDataLength {
			return common.Address{}, fmt.Errorf("invalid WebAuthn signature length %d", len(data))
		}
		return publicKeyAddress(data[len(data)-64:]), nil
	case envelopeTypeKeychainV1, envelopeTypeKeychainV2:
		if len(data) < common.AddressLength+2 {
			return common.Address{}, fmt.Errorf("keychain signature is too short")
		}
		inner := data[common.AddressLength:]
		switch {
		case len(inner) == 65:
		case inner[0] == envelopeTypeP256 || inner[0] == envelopeTypeWebAuthn:
			if _, err := envelopeSigner(inner); err != nil {
				return common.Address{}, err
			}
		case inner[0] == envelopeTypeMultisig && envelope[0] == envelopeTypeKeychainV2:
			if _, err := envelopeSigner(inner); err != nil {
				return common.Address{}, err
			}
		default:
			return common.Address{}, fmt.Errorf("unsupported keychain inner signature")
		}
		user := common.BytesToAddress(data[:common.AddressLength])
		if user == (common.Address{}) {
			return common.Address{}, fmt.Errorf("keychain signature user account is zero")
		}
		return user, nil
	case envelopeTypeMultisig:
		var fields []rlp.RawValue
		if err := rlp.DecodeBytes(data, &fields); err != nil || len(fields) != 3 {
			return common.Address{}, fmt.Errorf("invalid multisig signature")
		}
		var account []byte
		if err := rlp.DecodeBytes(fields[0], &account); err != nil || len(account) != common.AddressLength {
			return common.Address{}, fmt.Errorf("invalid multisig account")
		}
		if kind, _, _, err := rlp.Split(fields[1]); err != nil || kind != rlp.List {
			return common.Address{}, fmt.Errorf("invalid multisig config")
		}
		var signatures [][]byte
		if err := rlp.DecodeBytes(fields[2], &signatures); err != nil || len(signatures) == 0 {
			return common.Address{}, fmt.Errorf("invalid multisig signatures")
		}
		address := common.BytesToAddress(account)
		if address == (common.Address{}) {
			return common.Address{}, fmt.Errorf("multisig account is zero")
		}
		return address, nil
	default:
		return common.Address{}, fmt.Errorf("unsupported signature type 0x%02x", envelope[0])
	}
}

// publicKeyAddress returns the address of an uncompressed public key given
// as its 32-byte x and y coordinates.
func publicKeyAddress(xy []byte) common.Address {
	return common.BytesToAddress(crypto.Keccak256(xy)[12:])
}

// verifyTransactionSender returns the account a transaction's sender
// signature authorizes. A secp256k1 signature is recovered; a Keychain
// envelope's secp256k1 inner signature is verified against the sign payload
// (Keychain V1) or keccak256(0x04 || payload || user) (Keychain V2) and
// authorizes the user account; P256, WebAuthn, multisig and keychain
// envelopes with such inner signatures name their account (see
// envelopeSigner).
func verifyTransactionSender(tx *tempotx.Tx) (common.Address, error) {
	if tx.Signature == nil {
		return common.Address{}, fmt.Errorf("transaction has no sender signature")
	}
	var sender common.Address
	switch tx.Signature.Type {
	case "", tempotx.SignatureTypeSecp256k1:
		recovered, err := tempotx.VerifySignature(tx)
		if err != nil {
			return common.Address{}, err
		}
		sender = recovered
	case tempotx.SignatureTypeP256, tempotx.SignatureTypeWebAuthn, signatureTypeMultisig:
		named, err := envelopeSigner(tx.Signature.Raw)
		if err != nil {
			return common.Address{}, err
		}
		sender = named
	case signatureTypeKeychain:
		raw := tx.Signature.Raw
		user, err := envelopeSigner(raw)
		if err != nil {
			return common.Address{}, err
		}
		if inner := raw[1+common.AddressLength:]; len(inner) == 65 {
			hash, err := tempotx.GetSignPayload(tx)
			if err != nil {
				return common.Address{}, err
			}
			payload := hash
			if raw[0] == envelopeTypeKeychainV2 {
				wrapped := make([]byte, 0, 1+common.HashLength+common.AddressLength)
				wrapped = append(wrapped, envelopeTypeKeychainV2)
				wrapped = append(wrapped, hash.Bytes()...)
				wrapped = append(wrapped, user.Bytes()...)
				payload = crypto.Keccak256Hash(wrapped)
			}
			if _, err := recoverSecp256k1(payload, inner); err != nil {
				return common.Address{}, err
			}
		}
		sender = user
	default:
		return common.Address{}, fmt.Errorf("unsupported sender signature type %q", tx.Signature.Type)
	}
	if tx.From != (common.Address{}) && tx.From != sender {
		return common.Address{}, fmt.Errorf("transaction sender does not match signature")
	}
	return sender, nil
}
