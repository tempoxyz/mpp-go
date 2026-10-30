package chargeserver

import (
	"context"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	temposigner "github.com/tempoxyz/tempo-go/pkg/signer"

	"github.com/tempoxyz/mpp-go/pkg/mpp"
	mppserver "github.com/tempoxyz/mpp-go/pkg/server"
	"github.com/tempoxyz/mpp-go/pkg/tempo"
)

// Fixtures computed with viem 2.57.1 (mppx's pinned version) from mppx's
// src/tempo/internal/proof.ts typedData({account: root, chainId: 42431,
// challengeId, realm}): hashTypedData, signTypedData by
// privateKeyToAccount(0x11..11), and by viem/tempo
// Account.fromSecp256k1(0x22..22, {access: root}), a Keychain V2 envelope.
// They are the vectors pympp's tests/test_tempo_proof.py pins.
const (
	viemRootKey           = "0x1111111111111111111111111111111111111111111111111111111111111111"
	viemAccessKey         = "0x2222222222222222222222222222222222222222222222222222222222222222"
	viemChallengeID       = "kM9xPqWvT2nJrHsY4aDfEb"
	viemRealm             = "api.example.com"
	viemProofHash         = "0x2db9f3ad46355ef4479f0b322396221c624618117d42cfa3ddcce707682ab155"
	viemProofSignature    = "0x3baf02ba159042829a9bcaa17774cecb25b0194cccff0dd076db7474befcaa2b26592fc409ed59c24888f9d3b276debe61696c86793e9a35632cbff79c3496d01c"
	viemKeychainSignature = "0x0419e7e376e7c213b7e7e7e46cc70a5dd086daff2aa64ad10a15b8ed2c208c147daf58f5ec3989b4de18173565f10d3acad84fc1cf3ccc20181099d57723b7b9f9ed3821442c44f641e0eb4e8a4f2965d15be9e8621c"
)

func TestProofMatchesViemFixtures(t *testing.T) {
	t.Parallel()

	root, err := temposigner.NewSigner(viemRootKey)
	require.NoError(t, err)
	access, err := temposigner.NewSigner(viemAccessKey)
	require.NoError(t, err)

	hash, err := tempo.ProofTypedDataHash(42431, root.Address(), viemChallengeID, viemRealm)
	require.NoError(t, err)
	assert.Equal(t, viemProofHash, hash.Hex())

	signer, err := recoverProofSigner(hash, viemProofSignature, root.Address())
	require.NoError(t, err)
	assert.Equal(t, root.Address(), signer)

	signer, err = recoverProofSigner(hash, viemKeychainSignature, root.Address())
	require.NoError(t, err)
	assert.Equal(t, access.Address(), signer)

	// The envelope names its account; another source is refused.
	_, err = recoverProofSigner(hash, viemKeychainSignature, access.Address())
	require.Error(t, err)
}

// A Keychain V1 (type 0x03) proof's inner signature signs the proof digest
// itself, as mppx's recoverAuthorizedProofSigner accepts.
func TestChargeFlow_ProofCredentialWithKeychainV1AccessKey(t *testing.T) {
	request, err := tempo.NormalizeChargeRequest(tempo.ChargeRequestParams{
		Amount: "0", Currency: testCurrency, Recipient: testRecipient, Decimals: 6, ChainID: 42431,
	})
	require.NoError(t, err)
	rpc := newMockRPC(request)
	challenge := buildChallenge(t, request)
	root, err := temposigner.NewSigner(testPrivateKey)
	require.NoError(t, err)
	access, err := temposigner.NewSigner(feePayerKey)
	require.NoError(t, err)
	hash, err := tempo.ProofTypedDataHash(42431, root.Address(), challenge.ID, challenge.Realm)
	require.NoError(t, err)
	inner, err := access.Sign(hash)
	require.NoError(t, err)
	envelope := make([]byte, 0, 86)
	envelope = append(envelope, keychainV1SignatureType)
	envelope = append(envelope, root.Address().Bytes()...)
	envelope = append(envelope, common.LeftPadBytes(inner.R.Bytes(), 32)...)
	envelope = append(envelope, common.LeftPadBytes(inner.S.Bytes(), 32)...)
	envelope = append(envelope, inner.YParity+27)
	rpc.callResult = encodeActiveKeyInfo(access.Address(), time.Now().Add(time.Hour).Unix())

	credential := &mpp.Credential{
		Challenge: challenge.ToEcho(),
		Payload:   tempo.ChargeCredentialPayload{Type: tempo.CredentialTypeProof, Signature: hexutil.Encode(envelope)}.Map(),
		Source:    tempo.ProofSource(42431, root.Address()),
	}
	intent, err := NewIntent(IntentConfig{Store: tempo.NewMemoryStore(), RPC: rpc})
	require.NoError(t, err)
	receipt, err := intent.Verify(context.Background(), credential, request.Map())
	require.NoError(t, err)
	assert.Equal(t, challenge.ID, receipt.Reference)

	// A revoked or unknown access key does not authorize the proof.
	rpc.callResult = encodeActiveKeyInfo(common.Address{}, 0)
	challenge = buildChallenge(t, request)
	credential.Challenge = challenge.ToEcho()
	hash, err = tempo.ProofTypedDataHash(42431, root.Address(), challenge.ID, challenge.Realm)
	require.NoError(t, err)
	inner, err = access.Sign(hash)
	require.NoError(t, err)
	copy(envelope[21:53], common.LeftPadBytes(inner.R.Bytes(), 32))
	copy(envelope[53:85], common.LeftPadBytes(inner.S.Bytes(), 32))
	envelope[85] = inner.YParity
	credential.Payload["signature"] = hexutil.Encode(envelope)
	_, err = intent.Verify(context.Background(), credential, request.Map())
	require.ErrorContains(t, err, "proof signature does not match source")
}

// A push (hash) credential pays a sponsored challenge, as in mppx: the client
// broadcast the transaction and paid its fees itself.
func TestChargeFlow_HashCredentialPaysSponsoredChallenge(t *testing.T) {
	request := buildRequest(t, true, nil)
	rpc := newMockRPC(request)
	challenge := buildChallenge(t, request)
	credential, err := newClientMethod(t, rpc, tempo.CredentialTypeHash).CreateCredential(context.Background(), challenge)
	require.NoError(t, err)
	require.Equal(t, string(tempo.CredentialTypeHash), credential.Payload["type"])
	require.Len(t, rpc.sentRawTxs, 1)

	intent, err := NewIntent(IntentConfig{RPC: rpc, Store: tempo.NewMemoryStore(), FeePayerPrivateKey: feePayerKey})
	require.NoError(t, err)
	receipt, err := intent.Verify(context.Background(), credential, request.Map())
	require.NoError(t, err)
	assert.Equal(t, credential.Payload["hash"], receipt.Reference)
	// The server broadcast nothing: the client already did.
	assert.Len(t, rpc.sentRawTxs, 1)
}

// Push-only sponsored routes never use the fee payer, so issuing one does not
// need the sponsored expiry floor (mppx Charge.ts request()).
func TestSponsoredIssuanceFloorSkipsPushOnlyCharges(t *testing.T) {
	t.Parallel()

	method := newMethod(methodConfig{Intent: testSponsoringIntent(), Recipient: sponsoredIssuanceRecipient, ChainID: 42431})
	short := mpp.ExpiresIn(10 * time.Second)
	_, err := method.BuildChargeRequest(mppserver.ChargeParams{
		Amount: "0.50", FeePayer: true, Expires: short, SupportedModes: []tempo.ChargeMode{tempo.ChargeModePush},
	})
	require.NoError(t, err)
	_, err = method.BuildChargeRequest(mppserver.ChargeParams{
		Amount: "0.50", FeePayer: true, Expires: short, SupportedModes: []tempo.ChargeMode{tempo.ChargeModePush, tempo.ChargeModePull},
	})
	require.ErrorContains(t, err, "sponsored charge challenges must expire at least 15s after issuance")
}
