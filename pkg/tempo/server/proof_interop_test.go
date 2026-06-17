package chargeserver

import (
	"context"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/stretchr/testify/assert"
	"github.com/tempoxyz/mpp-go/pkg/mpp"
	"github.com/tempoxyz/mpp-go/pkg/tempo"
	"github.com/tempoxyz/tempo-go/pkg/keychain"
	temposigner "github.com/tempoxyz/tempo-go/pkg/signer"
)

// encodePlainProofSignature serializes a secp256k1 signature into the 65-byte
// (r || s || yParity) form used by plain (non-keychain) Tempo proof credentials.
func encodePlainProofSignature(t *testing.T, signer *temposigner.Signer, hash common.Hash) string {
	t.Helper()
	sig, err := signer.Sign(hash)
	assert.NoError(t, err)
	raw := make([]byte, 65)
	sig.R.FillBytes(raw[:32])
	sig.S.FillBytes(raw[32:64])
	raw[64] = sig.YParity
	return hexutil.Encode(raw)
}

// TestVerifyProof_RejectsCrossAccountAccessKeyReplay checks that a proof signed
// for root account A cannot be replayed by claiming a different root account B,
// even when the same access key is active for both.
func TestVerifyProof_RejectsCrossAccountAccessKeyReplay(t *testing.T) {
	ctx := context.Background()
	request, err := tempo.NormalizeChargeRequest(tempo.ChargeRequestParams{
		Amount:    "0",
		Currency:  testCurrency,
		Recipient: testRecipient,
		Decimals:  6,
		ChainID:   42431,
	})
	assert.NoError(t, err)
	challenge := buildChallenge(t, request)

	rootA, err := temposigner.NewSigner(testPrivateKey)
	assert.NoError(t, err)
	accessKey, err := temposigner.NewSigner(feePayerKey)
	assert.NoError(t, err)
	// Root account B is a distinct payer the attacker wants to impersonate.
	rootB := common.HexToAddress("0x000000000000000000000000000000000000bEEF")
	assert.NotEqual(t, rootA.Address(), rootB)

	// Access key signs a plain (non-keychain) proof bound to root account A.
	proofHashA, err := tempo.ProofTypedDataHash(42431, rootA.Address(), challenge.ID, challenge.Realm)
	assert.NoError(t, err)
	signature := encodePlainProofSignature(t, accessKey, proofHashA)

	// Honest submission (source = A) verifies. A fresh intent/store per case
	// avoids replay-store interference.
	{
		rpc := newMockRPC(request)
		rpc.callResult = encodeActiveKeyInfo(accessKey.Address(), time.Now().Add(time.Hour).Unix())
		intent, err := NewIntent(IntentConfig{RPC: rpc})
		assert.NoError(t, err)
		credential := &mpp.Credential{
			Challenge: challenge.ToEcho(),
			Payload: tempo.ChargeCredentialPayload{
				Type:      tempo.CredentialTypeProof,
				Signature: signature,
			}.Map(),
			Source: tempo.ProofSource(42431, rootA.Address()),
		}
		_, err = intent.Verify(ctx, credential, request.Map())
		assert.NoErrorf(t, err, "honest proof bound to A must verify")
	}

	// Replay (source = B): the same access key is active for B too (mock returns
	// it as active), but the replay must still be rejected.
	{
		rpc := newMockRPC(request)
		rpc.callResult = encodeActiveKeyInfo(accessKey.Address(), time.Now().Add(time.Hour).Unix())
		intent, err := NewIntent(IntentConfig{RPC: rpc})
		assert.NoError(t, err)
		credential := &mpp.Credential{
			Challenge: challenge.ToEcho(),
			Payload: tempo.ChargeCredentialPayload{
				Type:      tempo.CredentialTypeProof,
				Signature: signature,
			}.Map(),
			Source: tempo.ProofSource(42431, rootB),
		}
		_, err = intent.Verify(ctx, credential, request.Map())
		assert.Errorf(t, err, "cross-account replay against B must be rejected")
		assert.Contains(t, err.Error(), "proof signature does not match source")
	}
}

// TestRecoverProofSigner_AcceptsLegacyVSignatureVector pins a known proof
// signature with a legacy v=27/28 recovery byte and checks it recovers to the
// bound wallet, exercising the digest and the y-parity normalization in
// recoverProofSigner.
func TestRecoverProofSigner_AcceptsLegacyVSignatureVector(t *testing.T) {
	t.Parallel()

	account := common.HexToAddress("0x1a642f0E3c3aF545E7AcBD38b07251B3990914F1")
	const (
		chainID     = int64(42431)
		challengeID = "kM9xPqWvT2nJrHsY4aDfEb"
		realm       = "api.example.com"
		// Proof signature with a legacy v=27/28 recovery byte.
		legacyVSignature = "0x53f5d64d9f995e841b4212639b2e17e508e96752e10316df3814a16443dcbdb626c082190a4c3ecc3148101eb443d15bd83b579380b1be735a9c99f0df36c9fe1b"
	)

	proofHash, err := tempo.ProofTypedDataHash(chainID, account, challengeID, realm)
	assert.NoError(t, err)

	signer, err := recoverProofSigner(proofHash, legacyVSignature, account)
	assert.NoError(t, err)
	assert.Equalf(t, account.Hex(), signer.Hex(),
		"legacy-v proof signature must recover to the bound wallet")
}

// TestRecoverProofSigner_KeychainAcceptsLegacyVRecoveryByte checks that a
// keychain-v2 proof whose inner secp256k1 signature uses the legacy v=27/28
// recovery byte still recovers to the access-key address. Without recovery-byte
// normalization in the keychain path this fails, because RecoverAddress
// requires a 0/1 yParity.
func TestRecoverProofSigner_KeychainAcceptsLegacyVRecoveryByte(t *testing.T) {
	t.Parallel()

	rootSigner, err := temposigner.NewSigner(testPrivateKey)
	assert.NoError(t, err)
	accessKey, err := temposigner.NewSigner(feePayerKey)
	assert.NoError(t, err)

	proofHash, err := tempo.ProofTypedDataHash(42431, rootSigner.Address(), "challenge-1", "api.example.com")
	assert.NoError(t, err)

	// Build the keychain-v2 inner signing payload and sign it with the access key.
	v2Payload := make([]byte, 0, 1+32+common.AddressLength)
	v2Payload = append(v2Payload, keychain.KeychainSignatureType)
	v2Payload = append(v2Payload, proofHash.Bytes()...)
	v2Payload = append(v2Payload, rootSigner.Address().Bytes()...)
	inner, err := accessKey.Sign(crypto.Keccak256Hash(v2Payload))
	assert.NoError(t, err)

	// Force a legacy v=27/28 recovery byte.
	inner.YParity += 27
	keychainSig := keychain.BuildKeychainSignature(inner, rootSigner.Address())

	signer, err := recoverProofSigner(proofHash, hexutil.Encode(keychainSig), rootSigner.Address())
	assert.NoError(t, err)
	assert.Equalf(t, accessKey.Address().Hex(), signer.Hex(),
		"keychain proof with legacy v byte must recover to the access key")
}
