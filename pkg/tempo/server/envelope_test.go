package chargeserver

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/hex"
	"math/big"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	temposigner "github.com/tempoxyz/tempo-go/pkg/signer"
	tempotx "github.com/tempoxyz/tempo-go/pkg/transaction"

	"github.com/tempoxyz/mpp-go/pkg/mpp"
	"github.com/tempoxyz/mpp-go/pkg/tempo"
)

func p256Envelope(t *testing.T) ([]byte, common.Address) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	uncompressed, err := key.PublicKey.Bytes()
	require.NoError(t, err)
	xy := uncompressed[1:]
	envelope := []byte{envelopeTypeP256}
	envelope = append(envelope, common.LeftPadBytes([]byte{1}, 32)...)
	envelope = append(envelope, common.LeftPadBytes([]byte{2}, 32)...)
	envelope = append(envelope, xy...)
	envelope = append(envelope, 0)
	return envelope, common.BytesToAddress(crypto.Keccak256(xy)[12:])
}

func multisigEnvelope(t *testing.T, account common.Address) []byte {
	t.Helper()
	body, err := rlp.EncodeToBytes([]any{account.Bytes(), []any{[]byte{1}}, [][]byte{make([]byte, 65)}})
	require.NoError(t, err)
	return append([]byte{envelopeTypeMultisig}, body...)
}

func TestEnvelopeSignerNamesAccount(t *testing.T) {
	t.Parallel()

	p256, p256Address := p256Envelope(t)
	user := common.HexToAddress("0x00000000000000000000000000000000000000aa")
	keychainP256 := append(append([]byte{envelopeTypeKeychainV2}, user.Bytes()...), p256...)
	keychainV1 := append(append([]byte{envelopeTypeKeychainV1}, user.Bytes()...), make([]byte, 65)...)
	webAuthnData := append(make([]byte, 37), []byte(`{"type":"webauthn.get"}`)...)
	webAuthn := append([]byte{envelopeTypeWebAuthn}, webAuthnData...)
	webAuthn = append(webAuthn, p256[1:65]...)   // r, s
	webAuthn = append(webAuthn, p256[65:129]...) // x, y

	for _, tt := range []struct {
		name     string
		envelope []byte
		want     common.Address
	}{
		{name: "p256", envelope: p256, want: p256Address},
		{name: "webauthn", envelope: webAuthn, want: p256Address},
		{name: "keychain v2 p256 inner", envelope: keychainP256, want: user},
		{name: "keychain v1 secp256k1 inner", envelope: keychainV1, want: user},
		{name: "multisig", envelope: multisigEnvelope(t, user), want: user},
	} {
		got, err := envelopeSigner(tt.envelope)
		require.NoError(t, err, tt.name)
		assert.Equal(t, tt.want, got, tt.name)
	}
	for _, bad := range [][]byte{{envelopeTypeP256, 1}, {0x09, 1, 2}, append([]byte{envelopeTypeKeychainV1}, user.Bytes()...)} {
		_, err := envelopeSigner(bad)
		require.Error(t, err)
	}
}

// sponsoredCredentialWithEnvelope returns a sponsored transaction credential
// whose sender signature is replaced by envelope(signPayload).
func sponsoredCredentialWithEnvelope(
	t *testing.T,
	rpc *mockRPC,
	request tempo.ChargeRequest,
	sender common.Address,
	envelope func(common.Hash) []byte,
) *mpp.Credential {
	t.Helper()
	challenge := buildChallenge(t, request)
	credential, err := newClientMethod(t, rpc, tempo.CredentialTypeTransaction).CreateCredential(context.Background(), challenge)
	require.NoError(t, err)
	tx, _, err := deserializeTransactionCredential(credential.Payload["signature"].(string))
	require.NoError(t, err)
	tx.Signature = nil
	payload, err := tempotx.GetSignPayload(tx)
	require.NoError(t, err)
	raw := envelope(payload)
	kind := signatureTypeKeychain
	switch raw[0] {
	case envelopeTypeP256:
		kind = tempotx.SignatureTypeP256
	case envelopeTypeMultisig:
		kind = signatureTypeMultisig
	}
	tx.Signature = &temposigner.SignatureEnvelope{Type: kind, Raw: raw}
	serialized, err := tempotx.Serialize(tx, nil)
	require.NoError(t, err)
	credential.Payload["signature"] = serialized
	credential.Source = tempo.ProofSource(42431, sender)
	return credential
}

// Like mppx's prepareSponsoredTransaction, a local fee payer co-signs keychain
// (V1 and V2 with any inner signature), P256, WebAuthn and multisig senders.
func TestChargeFlow_FeePayerCoSignsNonSecp256k1Senders(t *testing.T) {
	root, err := temposigner.NewSigner(testPrivateKey)
	require.NoError(t, err)
	access, err := temposigner.NewSigner(secondPayerPrivateKey)
	require.NoError(t, err)
	p256, p256Address := p256Envelope(t)

	signInner := func(hash common.Hash) []byte {
		signature, err := access.Sign(hash)
		require.NoError(t, err)
		out := make([]byte, 65)
		signature.R.FillBytes(out[:32])
		signature.S.FillBytes(out[32:64])
		out[64] = signature.YParity
		return out
	}
	for _, tt := range []struct {
		name     string
		sender   common.Address
		envelope func(common.Hash) []byte
	}{
		{name: "keychain v1", sender: root.Address(), envelope: func(hash common.Hash) []byte {
			return append(append([]byte{envelopeTypeKeychainV1}, root.Address().Bytes()...), signInner(hash)...)
		}},
		{name: "keychain v2", sender: root.Address(), envelope: func(hash common.Hash) []byte {
			wrapped := crypto.Keccak256Hash([]byte{envelopeTypeKeychainV2}, hash.Bytes(), root.Address().Bytes())
			return append(append([]byte{envelopeTypeKeychainV2}, root.Address().Bytes()...), signInner(wrapped)...)
		}},
		{name: "keychain v2 p256 inner", sender: root.Address(), envelope: func(common.Hash) []byte {
			return append(append([]byte{envelopeTypeKeychainV2}, root.Address().Bytes()...), p256...)
		}},
		{name: "p256", sender: p256Address, envelope: func(common.Hash) []byte { return p256 }},
		{name: "multisig", sender: root.Address(), envelope: func(common.Hash) []byte { return multisigEnvelope(t, root.Address()) }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			request := buildRequest(t, true, nil)
			rpc := newMockRPC(request)
			rpc.onSend = func(raw string) (string, map[string]any, error) {
				tx, _, err := deserializeTransactionCredential(raw)
				if err != nil {
					return "", nil, err
				}
				hash, err := tempotx.ComputeHash(raw)
				if err != nil {
					return "", nil, err
				}
				receipt := receiptForCalls(tx.Calls, request, tt.sender)
				return hash.Hex(), receipt, nil
			}
			credential := sponsoredCredentialWithEnvelope(t, rpc, request, tt.sender, tt.envelope)
			intent, err := NewIntent(IntentConfig{RPC: rpc, Store: tempo.NewMemoryStore(), FeePayerPrivateKey: feePayerKey})
			require.NoError(t, err)
			_, err = intent.Verify(context.Background(), credential, request.Map())
			require.NoError(t, err)
			require.Len(t, rpc.sentRawTxs, 1)
			broadcast, _, err := deserializeTransactionCredential(rpc.sentRawTxs[0])
			require.NoError(t, err)
			require.NotNil(t, broadcast.FeePayerSignature)
			// The sender envelope is broadcast unchanged, apart from a
			// secp256k1 inner recovery byte's 27/28 node encoding.
			sent := broadcast.Signature.Raw
			want := tt.envelope(common.Hash{})
			if len(sent) == 86 {
				sent, want = sent[:85], want[:85]
			}
			if tt.name != "keychain v1" && tt.name != "keychain v2" {
				assert.Equal(t, want, sent)
			}
		})
	}
}

func receiptForCalls(calls []tempotx.Call, request tempo.ChargeRequest, sender common.Address) map[string]any {
	logs := make([]any, 0, len(calls)*2)
	for _, call := range calls {
		data := hex.EncodeToString(call.Data)
		amount, _ := new(big.Int).SetString(data[72:136], 16)
		recipient := common.HexToAddress("0x" + data[32:72]).Hex()
		logs = append(logs, transferLog(request.Currency, sender.Hex(), recipient, amount, ""))
		if strings.HasPrefix(data, tempo.TransferWithMemoSelector) {
			logs = append(logs, transferLog(request.Currency, sender.Hex(), recipient, amount, "0x"+data[136:200]))
		}
	}
	return map[string]any{"status": "0x1", "logs": logs}
}

// mppx refuses a sponsored (or any) transaction carrying an authorizationList.
func TestDeserializeTransactionCredentialRefusesAuthorizationList(t *testing.T) {
	t.Parallel()

	request := buildRequest(t, true, nil)
	rpc := newMockRPC(request)
	credential, err := newClientMethod(t, rpc, tempo.CredentialTypeTransaction).CreateCredential(context.Background(), buildChallenge(t, request))
	require.NoError(t, err)
	tx, _, err := deserializeTransactionCredential(credential.Payload["signature"].(string))
	require.NoError(t, err)
	serialized, err := tempotx.Serialize(tx, nil)
	require.NoError(t, err)
	encoded := common.FromHex(serialized)
	var fields []rlp.RawValue
	require.NoError(t, rlp.DecodeBytes(encoded[1:], &fields))
	fields[12], err = rlp.EncodeToBytes([]any{[]any{[]byte{1}}})
	require.NoError(t, err)
	body, err := rlp.EncodeToBytes(fields)
	require.NoError(t, err)

	_, _, err = deserializeTransactionCredential(hexutil.Encode(append([]byte{0x76}, body...)))
	require.ErrorContains(t, err, "authorizationList is not supported")

	credential.Payload["signature"] = hexutil.Encode(append([]byte{0x76}, body...))
	intent, err := NewIntent(IntentConfig{RPC: rpc, Store: tempo.NewMemoryStore(), FeePayerPrivateKey: feePayerKey})
	require.NoError(t, err)
	_, err = intent.Verify(context.Background(), credential, request.Map())
	require.ErrorContains(t, err, "failed to deserialize transaction payload")
	require.Empty(t, rpc.sentRawTxs)
}
