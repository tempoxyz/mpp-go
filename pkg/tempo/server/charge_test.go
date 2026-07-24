package chargeserver

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/stretchr/testify/assert"
	"github.com/tempoxyz/mpp-go/pkg/mpp"
	"github.com/tempoxyz/mpp-go/pkg/tempo"
	"github.com/tempoxyz/mpp-go/pkg/tempo/client"
	temporpc "github.com/tempoxyz/tempo-go/pkg/client"
	"github.com/tempoxyz/tempo-go/pkg/keychain"
	temposigner "github.com/tempoxyz/tempo-go/pkg/signer"
	tempotx "github.com/tempoxyz/tempo-go/pkg/transaction"
)

const (
	// testPrivateKey is the fixed payer key used across Tempo charge tests.
	testPrivateKey = "0x59c6995e998f97a5a0044966f0945389dc9e86dae88c7a8412f4603b6b78690d"
	// feePayerKey is the co-signer key used for sponsored-transaction tests.
	feePayerKey     = "0xdd83cd66cd98801a07e0b7c1a5b02364b369e696da7c0ab444acffea5cca86fc"
	testCurrency    = "0x20c0000000000000000000000000000000000001"
	testRecipient   = "0x70997970c51812dc3a010c7d01b50e0d17dc79c8"
	testRealm       = "api.example.com"
	testReceiptHash = "0xabc123"
)

func TestDecodeCallTransferRejectsPaddedCalldata(t *testing.T) {
	t.Parallel()

	amount := big.NewInt(1000000)
	transferData := common.FromHex(tempo.EncodeTransfer(testRecipient, amount))
	if _, ok := decodeCallTransfer(transferData); !assert.True(t, ok,
		"decodeCallTransfer() = false, want true for exact transfer calldata") {
		return
	}
	if _, ok := decodeCallTransfer(append(append([]byte(nil), transferData...), 0x01)); !assert.False(t, ok,
		"decodeCallTransfer() = true, want false for padded transfer calldata") {
		return
	}

	memo := "0x" + strings.Repeat("ab", 32)
	transferWithMemo, err := tempo.EncodeTransferWithMemo(testRecipient, amount, memo)
	if !assert.NoError(t, err,
		"EncodeTransferWithMemo() returned an unexpected error") {
		return
	}
	transferWithMemoData := common.FromHex(transferWithMemo)
	if _, ok := decodeCallTransfer(transferWithMemoData); !assert.True(t, ok,
		"decodeCallTransfer() = false, want true for exact transferWithMemo calldata") {
		return
	}
	if _, ok := decodeCallTransfer(append(append([]byte(nil), transferWithMemoData...), 0x01)); !assert.False(t, ok,
		"decodeCallTransfer() = true, want false for padded transferWithMemo calldata") {
		return
	}
}

type mockRPC struct {
	chainID          uint64
	nonce            uint64
	gasPrice         string
	estimateGas      string
	callResult       string
	receipts         map[string]map[string]any
	sentRawTxs       []string
	estimateGasCalls []map[string]any
	onSend           func(raw string) (string, map[string]any, error)
	onEstimateGas    func(params ...interface{}) (*temporpc.JSONRPCResponse, error)
	onGetReceipt     func(hash string) (*temporpc.JSONRPCResponse, error)
}

func (m *mockRPC) GetChainID(context.Context) (uint64, error) {
	return m.chainID, nil
}

func (m *mockRPC) GetTransactionCount(context.Context, string) (uint64, error) {
	return m.nonce, nil
}

func (m *mockRPC) SendRawTransaction(_ context.Context, serialized string) (string, error) {
	m.sentRawTxs = append(m.sentRawTxs, serialized)
	if m.onSend == nil {
		return testReceiptHash, nil
	}
	hash, receipt, err := m.onSend(serialized)
	if err != nil {
		return "", err
	}
	if receipt != nil {
		m.receipts[hash] = receipt
	}
	return hash, nil
}

func (m *mockRPC) SendRequest(_ context.Context, method string, params ...interface{}) (*temporpc.JSONRPCResponse, error) {
	switch method {
	case "eth_gasPrice":
		return &temporpc.JSONRPCResponse{Result: m.gasPrice}, nil
	case "eth_estimateGas":
		if len(params) > 0 {
			if callObject, ok := params[0].(map[string]any); ok {
				m.estimateGasCalls = append(m.estimateGasCalls, callObject)
			}
		}
		if m.onEstimateGas != nil {
			return m.onEstimateGas(params...)
		}
		return &temporpc.JSONRPCResponse{Result: m.estimateGas}, nil
	case "eth_call":
		return &temporpc.JSONRPCResponse{Result: m.callResult}, nil
	case "eth_getTransactionReceipt":
		hash := params[0].(string)
		if m.onGetReceipt != nil {
			return m.onGetReceipt(hash)
		}
		return &temporpc.JSONRPCResponse{Result: m.receipts[hash]}, nil
	default:
		return nil, fmt.Errorf("unexpected rpc method %q", method)
	}
}

func TestChargeFlow_FeePayerTransactionViaRemoteSigner(t *testing.T) {
	ctx := context.Background()
	request := buildRequest(t, true, nil)
	request.MethodDetails.FeePayerURL = "https://fee-payer.example.com"
	rpc := newMockRPC(request)
	clientMethod := newClientMethod(t, rpc, tempo.CredentialTypeTransaction)
	challenge := buildChallenge(t, request)

	credential, err := clientMethod.CreateCredential(ctx, challenge)
	if !assert.NoErrorf(t, err,
		"CreateCredential() error = %v", err) {
		return
	}

	raw := credential.Payload["signature"].(string)

	feePayerSigner, err := temposigner.NewSigner(feePayerKey)
	if !assert.NoErrorf(t, err,
		"NewSigner() error = %v", err) {
		return
	}

	feePayerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Method string   `json:"method"`
			Params []string `json:"params"`
		}
		{
			err := json.NewDecoder(r.Body).Decode(&body)
			if !assert.NoErrorf(t, err,
				"Decode(request) error = %v", err) {
				return
			}
		}
		if !assert.Equalf(t, "eth_signRawTransaction", body.Method,
			"method = %q, want eth_signRawTransaction", body.Method) {
			return
		}
		if !assert.Falsef(t, len(body.Params) != 1 || body.Params[0] != raw,
			"params = %#v, want original raw tx", body.Params) {
			return
		}

		coSignedTx, err := tempotx.Deserialize(raw)
		if !assert.NoErrorf(t, err,
			"Deserialize(raw) error = %v", err) {
			return
		}

		sender, err := tempotx.VerifySignature(coSignedTx)
		if !assert.NoErrorf(t, err,
			"VerifySignature() error = %v", err) {
			return
		}

		coSignedTx.From = sender
		coSignedTx.FeeToken = common.HexToAddress(request.Currency)
		coSignedTx.AwaitingFeePayer = false
		{
			err := tempotx.AddFeePayerSignature(coSignedTx, feePayerSigner)
			if !assert.NoErrorf(t, err,
				"AddFeePayerSignature() error = %v", err) {
				return
			}
		}

		serialized, err := tempotx.Serialize(coSignedTx, nil)
		if !assert.NoErrorf(t, err,
			"Serialize() error = %v", err) {
			return
		}

		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": serialized})
	}))
	defer feePayerServer.Close()
	request.MethodDetails.FeePayerURL = feePayerServer.URL

	intent, err := NewIntent(IntentConfig{RPC: rpc})
	if !assert.NoErrorf(t, err,
		"NewIntent() error = %v", err) {
		return
	}

	receipt, err := intent.Verify(ctx, credential, request.Map())
	if !assert.NoErrorf(t, err,
		"Verify() error = %v", err) {
		return
	}
	if !assert.Equalf(t, testReceiptHash, receipt.Reference,
		"receipt reference = %q, want %q", receipt.Reference, testReceiptHash) {
		return
	}
	if !assert.Lenf(t, rpc.sentRawTxs, 1,
		"expected 1 broadcast, got %d", len(rpc.sentRawTxs)) {
		return
	}
	if !assert.NotContainsf(t, rpc.sentRawTxs[0], "feefeefeefee",
		"broadcast transaction still contains fee payer marker: %q", rpc.sentRawTxs[0]) {
		return
	}

}

func TestChargeFlow_FeePayerTransactionViaRemoteSignerRejectsTamperedFeeToken(t *testing.T) {
	ctx := context.Background()
	request := buildRequest(t, true, nil)
	rpc := newMockRPC(request)
	clientMethod := newClientMethod(t, rpc, tempo.CredentialTypeTransaction)
	challenge := buildChallenge(t, request)

	credential, err := clientMethod.CreateCredential(ctx, challenge)
	if !assert.NoErrorf(t, err,
		"CreateCredential() error = %v", err) {
		return
	}

	raw := credential.Payload["signature"].(string)

	feePayerSigner, err := temposigner.NewSigner(feePayerKey)
	if !assert.NoErrorf(t, err,
		"NewSigner() error = %v", err) {
		return
	}

	feePayerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		coSignedTx, err := tempotx.Deserialize(raw)
		if !assert.NoErrorf(t, err,
			"Deserialize(raw) error = %v", err) {
			return
		}

		sender, err := tempotx.VerifySignature(coSignedTx)
		if !assert.NoErrorf(t, err,
			"VerifySignature() error = %v", err) {
			return
		}

		coSignedTx.From = sender
		coSignedTx.FeeToken = common.HexToAddress("0x20c0000000000000000000000000000000000002")
		coSignedTx.AwaitingFeePayer = false
		{
			err := tempotx.AddFeePayerSignature(coSignedTx, feePayerSigner)
			if !assert.NoErrorf(t, err,
				"AddFeePayerSignature() error = %v", err) {
				return
			}
		}

		serialized, err := tempotx.Serialize(coSignedTx, nil)
		if !assert.NoErrorf(t, err,
			"Serialize() error = %v", err) {
			return
		}

		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": serialized})
	}))
	defer feePayerServer.Close()
	request.MethodDetails.FeePayerURL = feePayerServer.URL

	intent, err := NewIntent(IntentConfig{RPC: rpc})
	if !assert.NoErrorf(t, err,
		"NewIntent() error = %v", err) {
		return
	}

	if _, err := intent.Verify(ctx, credential, request.Map()); err == nil || !strings.Contains(err.Error(), "fee token") {
		assert.Failf(t, "", "Verify() error = %v, want fee token rejection", err)
		return
	}
	if !assert.Lenf(t, rpc.sentRawTxs, 0,
		"expected tampered transaction to be rejected before broadcast, got %d broadcasts", len(rpc.sentRawTxs)) {
		return
	}

}

func TestChargeFlow_ProofCredentialWithAccessKey(t *testing.T) {
	ctx := context.Background()
	request, err := tempo.NormalizeChargeRequest(tempo.ChargeRequestParams{
		Amount:    "0",
		Currency:  testCurrency,
		Recipient: testRecipient,
		Decimals:  6,
		ChainID:   42431,
	})
	if !assert.NoErrorf(t, err,
		"NormalizeChargeRequest() error = %v", err) {
		return
	}

	rpc := newMockRPC(request)
	challenge := buildChallenge(t, request)

	rootSigner, err := temposigner.NewSigner(testPrivateKey)
	if !assert.NoErrorf(t, err,
		"NewSigner(root) error = %v", err) {
		return
	}

	accessKey, err := temposigner.NewSigner(feePayerKey)
	if !assert.NoErrorf(t, err,
		"NewSigner(access key) error = %v", err) {
		return
	}
	proofHash, err := tempo.ProofTypedDataHash(42431, rootSigner.Address(), challenge.ID, challenge.Realm)
	if !assert.NoErrorf(t, err,
		"ProofTypedDataHash() error = %v", err) {
		return
	}

	v2Payload := make([]byte, 0, 1+len(proofHash.Bytes())+common.AddressLength)
	v2Payload = append(v2Payload, keychain.KeychainSignatureType)
	v2Payload = append(v2Payload, proofHash.Bytes()...)
	v2Payload = append(v2Payload, rootSigner.Address().Bytes()...)
	innerSignature, err := accessKey.Sign(crypto.Keccak256Hash(v2Payload))
	if !assert.NoErrorf(t, err,
		"accessKey.Sign() error = %v", err) {
		return
	}

	rpc.callResult = encodeActiveKeyInfo(accessKey.Address(), time.Now().Add(time.Hour).Unix())

	credential := &mpp.Credential{
		Challenge: challenge.ToEcho(),
		Payload: tempo.ChargeCredentialPayload{
			Type:      tempo.CredentialTypeProof,
			Signature: hexutil.Encode(keychain.BuildKeychainSignature(innerSignature, rootSigner.Address())),
		}.Map(),
		Source: tempo.ProofSource(42431, rootSigner.Address()),
	}

	intent, err := NewIntent(IntentConfig{RPC: rpc})
	if !assert.NoErrorf(t, err,
		"NewIntent() error = %v", err) {
		return
	}

	receipt, err := intent.Verify(ctx, credential, request.Map())
	if !assert.NoErrorf(t, err,
		"Verify() error = %v", err) {
		return
	}
	if !assert.Equalf(t, challenge.ID, receipt.Reference,
		"receipt reference = %q, want %q", receipt.Reference, challenge.ID) {
		return
	}

}

func TestChargeFlow_ProofCredentialWithAccessKeyWithoutExpiry(t *testing.T) {
	ctx := context.Background()
	request, err := tempo.NormalizeChargeRequest(tempo.ChargeRequestParams{
		Amount:    "0",
		Currency:  testCurrency,
		Recipient: testRecipient,
		Decimals:  6,
		ChainID:   42431,
	})
	if !assert.NoErrorf(t, err,
		"NormalizeChargeRequest() error = %v", err) {
		return
	}

	rpc := newMockRPC(request)
	challenge := buildChallenge(t, request)

	rootSigner, err := temposigner.NewSigner(testPrivateKey)
	if !assert.NoErrorf(t, err,
		"NewSigner(root) error = %v", err) {
		return
	}

	accessKey, err := temposigner.NewSigner(feePayerKey)
	if !assert.NoErrorf(t, err,
		"NewSigner(access key) error = %v", err) {
		return
	}
	proofHash, err := tempo.ProofTypedDataHash(42431, rootSigner.Address(), challenge.ID, challenge.Realm)
	if !assert.NoErrorf(t, err,
		"ProofTypedDataHash() error = %v", err) {
		return
	}

	v2Payload := make([]byte, 0, 1+len(proofHash.Bytes())+common.AddressLength)
	v2Payload = append(v2Payload, keychain.KeychainSignatureType)
	v2Payload = append(v2Payload, proofHash.Bytes()...)
	v2Payload = append(v2Payload, rootSigner.Address().Bytes()...)
	innerSignature, err := accessKey.Sign(crypto.Keccak256Hash(v2Payload))
	if !assert.NoErrorf(t, err,
		"accessKey.Sign() error = %v", err) {
		return
	}

	rpc.callResult = encodeActiveKeyInfo(accessKey.Address(), 0)

	credential := &mpp.Credential{
		Challenge: challenge.ToEcho(),
		Payload: tempo.ChargeCredentialPayload{
			Type:      tempo.CredentialTypeProof,
			Signature: hexutil.Encode(keychain.BuildKeychainSignature(innerSignature, rootSigner.Address())),
		}.Map(),
		Source: tempo.ProofSource(42431, rootSigner.Address()),
	}

	intent, err := NewIntent(IntentConfig{RPC: rpc})
	if !assert.NoErrorf(t, err,
		"NewIntent() error = %v", err) {
		return
	}

	receipt, err := intent.Verify(ctx, credential, request.Map())
	if !assert.NoErrorf(t, err,
		"Verify() error = %v", err) {
		return
	}
	if !assert.Equalf(t, challenge.ID, receipt.Reference,
		"receipt reference = %q, want %q", receipt.Reference, challenge.ID) {
		return
	}

}

func TestChargeFlow_HashCredentialRejectsExtraTransferLogs(t *testing.T) {
	ctx := context.Background()
	request, err := tempo.NormalizeChargeRequest(tempo.ChargeRequestParams{
		Amount:         "0.50",
		Currency:       testCurrency,
		Recipient:      testRecipient,
		Decimals:       6,
		ChainID:        42431,
		SupportedModes: []tempo.ChargeMode{tempo.ChargeModePush},
		Splits: []tempo.SplitParams{{
			Amount:    "0.10",
			Recipient: "0x3c44cdddb6a900fa2b585dd299e03d12fa4293bc",
		}},
	})
	if !assert.NoErrorf(t, err,
		"NormalizeChargeRequest() error = %v", err) {
		return
	}

	rpc := newMockRPC(request)
	rpc.onSend = func(raw string) (string, map[string]any, error) {
		tx, err := tempotx.Deserialize(raw)
		if err != nil {
			return "", nil, err
		}
		sender, err := tempotx.VerifySignature(tx)
		if err != nil {
			return "", nil, err
		}
		receipt := buildReceipt(raw, request, sender)
		logs := append([]any(nil), receipt["logs"].([]any)...)
		logs = append(logs, transferLog(request.Currency, sender.Hex(), request.Recipient, big.NewInt(1), ""))
		receipt["logs"] = logs
		return testReceiptHash, receipt, nil
	}
	clientMethod := newClientMethod(t, rpc, tempo.CredentialTypeHash)
	challenge := buildChallenge(t, request)

	credential, err := clientMethod.CreateCredential(ctx, challenge)
	if !assert.NoErrorf(t, err,
		"CreateCredential() error = %v", err) {
		return
	}

	intent, err := NewIntent(IntentConfig{RPC: rpc})
	if !assert.NoErrorf(t, err,
		"NewIntent() error = %v", err) {
		return
	}

	if _, err := intent.Verify(ctx, credential, request.Map()); err == nil || !strings.Contains(err.Error(), "does not satisfy") {
		assert.Failf(t, "", "Verify() error = %v, want receipt mismatch", err)
		return
	}
}

func TestChargeFlow_HashCredentialIgnoresFeeControllerLogs(t *testing.T) {
	ctx := context.Background()
	request, err := tempo.NormalizeChargeRequest(tempo.ChargeRequestParams{
		Amount:         "0.50",
		Currency:       testCurrency,
		Recipient:      testRecipient,
		Decimals:       6,
		ChainID:        42431,
		SupportedModes: []tempo.ChargeMode{tempo.ChargeModePush},
	})
	if !assert.NoErrorf(t, err,
		"NormalizeChargeRequest() error = %v", err) {
		return
	}

	rpc := newMockRPC(request)
	rpc.onSend = func(raw string) (string, map[string]any, error) {
		tx, err := tempotx.Deserialize(raw)
		if err != nil {
			return "", nil, err
		}
		sender, err := tempotx.VerifySignature(tx)
		if err != nil {
			return "", nil, err
		}
		receipt := buildReceipt(raw, request, sender)
		logs := append([]any(nil), receipt["logs"].([]any)...)
		logs = append(logs, transferLog(request.Currency, sender.Hex(), "0xfeec000000000000000000000000000000000000", big.NewInt(1), ""))
		receipt["logs"] = logs
		return testReceiptHash, receipt, nil
	}
	clientMethod := newClientMethod(t, rpc, tempo.CredentialTypeHash)
	challenge := buildChallenge(t, request)

	credential, err := clientMethod.CreateCredential(ctx, challenge)
	if !assert.NoErrorf(t, err,
		"CreateCredential() error = %v", err) {
		return
	}

	intent, err := NewIntent(IntentConfig{RPC: rpc})
	if !assert.NoErrorf(t, err,
		"NewIntent() error = %v", err) {
		return
	}

	if _, err := intent.Verify(ctx, credential, request.Map()); err != nil {
		assert.Failf(t, "", "Verify() error = %v", err)
		return
	}
}

func TestChargeFlow_HashCredentialRejectsExplicitPrimaryMemo(t *testing.T) {
	ctx := context.Background()
	request, err := tempo.NormalizeChargeRequest(tempo.ChargeRequestParams{
		Amount:         "0.50",
		Currency:       testCurrency,
		Recipient:      testRecipient,
		Decimals:       6,
		ChainID:        42431,
		Memo:           "0x0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20",
		SupportedModes: []tempo.ChargeMode{tempo.ChargeModePush},
	})
	if !assert.NoErrorf(t, err,
		"NormalizeChargeRequest() error = %v", err) {
		return
	}

	signer, err := temposigner.NewSigner(testPrivateKey)
	if !assert.NoErrorf(t, err,
		"NewSigner() error = %v", err) {
		return
	}

	challenge := buildChallenge(t, request)
	credential := &mpp.Credential{
		Challenge: challenge.ToEcho(),
		Payload: tempo.ChargeCredentialPayload{
			Type: tempo.CredentialTypeHash,
			Hash: testReceiptHash,
		}.Map(),
		Source: tempo.ProofSource(42431, signer.Address()),
	}

	intent, err := NewIntent(IntentConfig{RPC: newMockRPC(request)})
	if !assert.NoErrorf(t, err,
		"NewIntent() error = %v", err) {
		return
	}

	if _, err := intent.Verify(ctx, credential, request.Map()); err == nil || !strings.Contains(err.Error(), "explicit memo") {
		assert.Failf(t, "", "Verify() error = %v, want explicit memo rejection", err)
		return
	}
}

func TestChargeFlow_RejectsMalformedCredentialSource(t *testing.T) {
	ctx := context.Background()
	request := buildRequest(t, false, nil)
	rpc := newMockRPC(request)
	clientMethod := newClientMethod(t, rpc, tempo.CredentialTypeTransaction)
	challenge := buildChallenge(t, request)

	credential, err := clientMethod.CreateCredential(ctx, challenge)
	if !assert.NoErrorf(t, err,
		"CreateCredential() error = %v", err) {
		return
	}

	credential.Source = "not-a-did"

	intent, err := NewIntent(IntentConfig{RPC: rpc})
	if !assert.NoErrorf(t, err,
		"NewIntent() error = %v", err) {
		return
	}

	if _, err := intent.Verify(ctx, credential, request.Map()); err == nil || !strings.Contains(err.Error(), "credential source is invalid") {
		assert.Failf(t, "", "Verify() error = %v, want invalid credential source", err)
		return
	}
}

func TestChargeFlow_ProofCredentialRejectsDifferentRealm(t *testing.T) {
	ctx := context.Background()
	request, err := tempo.NormalizeChargeRequest(tempo.ChargeRequestParams{
		Amount:    "0",
		Currency:  testCurrency,
		Recipient: testRecipient,
		Decimals:  6,
		ChainID:   42431,
	})
	if !assert.NoErrorf(t, err,
		"NormalizeChargeRequest() error = %v", err) {
		return
	}

	rpc := newMockRPC(request)
	clientMethod := newClientMethod(t, rpc, tempo.CredentialTypeProof)
	challenge := buildChallenge(t, request)

	credential, err := clientMethod.CreateCredential(ctx, challenge)
	if !assert.NoErrorf(t, err,
		"CreateCredential() error = %v", err) {
		return
	}

	credential.Challenge.Realm = "other.example.com"

	intent, err := NewIntent(IntentConfig{RPC: rpc})
	if !assert.NoErrorf(t, err,
		"NewIntent() error = %v", err) {
		return
	}
	{

		_, err := intent.Verify(ctx, credential, request.Map())
		if !assert.Falsef(t, err == nil || !strings.Contains(err.Error(), "proof signature does not match source"),
			"Verify() error = %v, want proof signature mismatch", err) {
			return
		}
	}

}

func TestChargeFlow_TransactionCredentialReservesHashBeforeBroadcast(t *testing.T) {
	ctx := context.Background()
	request := buildRequest(t, false, nil)
	rpc := newMockRPC(request)
	rpc.onSend = func(raw string) (string, map[string]any, error) {
		tx, err := tempotx.Deserialize(raw)
		if err != nil {
			return "", nil, err
		}
		sender, err := tempotx.VerifySignature(tx)
		if err != nil {
			return "", nil, err
		}
		hash, err := tempotx.ComputeHash(raw)
		if err != nil {
			return "", nil, err
		}
		return hash.Hex(), buildReceipt(raw, request, sender), nil
	}
	clientMethod := newClientMethod(t, rpc, tempo.CredentialTypeTransaction)
	challenge := buildChallenge(t, request)

	credential, err := clientMethod.CreateCredential(ctx, challenge)
	if !assert.NoErrorf(t, err,
		"CreateCredential() error = %v", err) {
		return
	}

	intent, err := NewIntent(IntentConfig{RPC: rpc})
	if !assert.NoErrorf(t, err,
		"NewIntent() error = %v", err) {
		return
	}
	{

		_, err := intent.Verify(ctx, credential, request.Map())
		if !assert.NoErrorf(t, err,
			"first Verify() error = %v", err) {
			return
		}
	}
	if !assert.Lenf(t, rpc.sentRawTxs, 1,
		"expected 1 broadcast after first Verify(), got %d", len(rpc.sentRawTxs)) {
		return
	}
	{

		_, err := intent.Verify(ctx, credential, request.Map())
		if !assert.NoErrorf(t, err,
			"second Verify() error = %v", err) {
			return
		}
	}
	if !assert.Lenf(t, rpc.sentRawTxs, 1,
		"expected no second broadcast, got %d", len(rpc.sentRawTxs)) {
		return
	}

}

func TestChargeFlow_TransactionCredentialRefetchesReservedHashAfterReceiptFailure(t *testing.T) {
	ctx := context.Background()
	request := buildRequest(t, false, nil)
	rpc := newMockRPC(request)
	failReceiptFetch := true
	rpc.onSend = func(raw string) (string, map[string]any, error) {
		tx, err := tempotx.Deserialize(raw)
		if err != nil {
			return "", nil, err
		}
		sender, err := tempotx.VerifySignature(tx)
		if err != nil {
			return "", nil, err
		}
		hash, err := tempotx.ComputeHash(raw)
		if err != nil {
			return "", nil, err
		}
		return hash.Hex(), buildReceipt(raw, request, sender), nil
	}
	rpc.onGetReceipt = func(hash string) (*temporpc.JSONRPCResponse, error) {
		if failReceiptFetch {
			failReceiptFetch = false
			return nil, fmt.Errorf("temporary receipt rpc failure")
		}
		return &temporpc.JSONRPCResponse{Result: rpc.receipts[hash]}, nil
	}
	clientMethod := newClientMethod(t, rpc, tempo.CredentialTypeTransaction)
	challenge := buildChallenge(t, request)

	credential, err := clientMethod.CreateCredential(ctx, challenge)
	if !assert.NoErrorf(t, err,
		"CreateCredential() error = %v", err) {
		return
	}

	intent, err := NewIntent(IntentConfig{RPC: rpc})
	if !assert.NoErrorf(t, err,
		"NewIntent() error = %v", err) {
		return
	}
	{

		_, err := intent.Verify(ctx, credential, request.Map())
		if !assert.Falsef(t, err == nil || !strings.Contains(err.Error(), "failed to fetch transaction receipt"),
			"first Verify() error = %v, want receipt fetch failure", err) {
			return
		}
	}
	if !assert.Lenf(t, rpc.sentRawTxs, 1,
		"expected first Verify() to broadcast once, got %d", len(rpc.sentRawTxs)) {
		return
	}
	{

		_, err := intent.Verify(ctx, credential, request.Map())
		if !assert.NoErrorf(t, err,
			"second Verify() error = %v", err) {
			return
		}
	}
	if !assert.Lenf(t, rpc.sentRawTxs, 1,
		"expected retry to refetch without rebroadcast, got %d broadcasts", len(rpc.sentRawTxs)) {
		return
	}

}

func TestChargeFlow_TransactionCredentialReleasesReservationAfterBroadcastFailure(t *testing.T) {
	ctx := context.Background()
	request := buildRequest(t, false, nil)
	rpc := newMockRPC(request)
	rpc.onSend = func(raw string) (string, map[string]any, error) {
		return "", nil, fmt.Errorf("network down")
	}
	clientMethod := newClientMethod(t, rpc, tempo.CredentialTypeTransaction)
	challenge := buildChallenge(t, request)

	credential, err := clientMethod.CreateCredential(ctx, challenge)
	if !assert.NoErrorf(t, err,
		"CreateCredential() error = %v", err) {
		return
	}

	intent, err := NewIntent(IntentConfig{RPC: rpc})
	if !assert.NoErrorf(t, err,
		"NewIntent() error = %v", err) {
		return
	}
	{

		_, err := intent.Verify(ctx, credential, request.Map())
		if !assert.Falsef(t, err == nil || !strings.Contains(err.Error(), "transaction submission failed"),
			"first Verify() error = %v, want submission failure", err) {
			return
		}
	}
	if !assert.Lenf(t, rpc.sentRawTxs, 1,
		"expected first Verify() to broadcast once, got %d", len(rpc.sentRawTxs)) {
		return
	}

	rpc.onSend = func(raw string) (string, map[string]any, error) {
		tx, err := tempotx.Deserialize(raw)
		if err != nil {
			return "", nil, err
		}
		sender, err := tempotx.VerifySignature(tx)
		if err != nil {
			return "", nil, err
		}
		return testReceiptHash, buildReceipt(raw, request, sender), nil
	}
	{
		_, err := intent.Verify(ctx, credential, request.Map())
		if !assert.NoErrorf(t, err,
			"second Verify() error = %v", err) {
			return
		}
	}
	if !assert.Lenf(t, rpc.sentRawTxs, 2,
		"expected second Verify() to broadcast after release, got %d broadcasts", len(rpc.sentRawTxs)) {
		return
	}

}

func TestChargeFlow_RejectsFeePayerTransactionOutsideSponsorPolicy(t *testing.T) {
	ctx := context.Background()
	request := buildRequest(t, true, nil)
	rpc := newMockRPC(request)
	rpc.estimateGas = fmt.Sprintf("0x%x", feePayerMaxGas)
	clientMethod := newClientMethod(t, rpc, tempo.CredentialTypeTransaction)
	challenge := buildChallenge(t, request)

	credential, err := clientMethod.CreateCredential(ctx, challenge)
	if !assert.NoErrorf(t, err,
		"CreateCredential() error = %v", err) {
		return
	}

	intent, err := NewIntent(IntentConfig{RPC: rpc, FeePayerPrivateKey: feePayerKey})
	if !assert.NoErrorf(t, err,
		"NewIntent() error = %v", err) {
		return
	}

	if _, err := intent.Verify(ctx, credential, request.Map()); err == nil || !strings.Contains(err.Error(), "sponsor policy") {
		assert.Failf(t, "", "Verify() error = %v, want sponsor policy rejection", err)
		return
	}
}

func TestChargeFlow_FeePayerTransactionUsesChallengeOnceAfterRevert(t *testing.T) {
	ctx := context.Background()
	request := buildRequest(t, true, nil)
	rpc := newMockRPC(request)
	rpc.onSend = func(raw string) (string, map[string]any, error) {
		return testReceiptHash, map[string]any{"status": "0x0", "logs": []any{}}, nil
	}
	clientMethod := newClientMethod(t, rpc, tempo.CredentialTypeTransaction)
	challenge := buildChallenge(t, request)

	credential, err := clientMethod.CreateCredential(ctx, challenge)
	if !assert.NoErrorf(t, err,
		"CreateCredential() error = %v", err) {
		return
	}

	intent, err := NewIntent(IntentConfig{RPC: rpc, FeePayerPrivateKey: feePayerKey})
	if !assert.NoErrorf(t, err,
		"NewIntent() error = %v", err) {
		return
	}
	{

		_, err := intent.Verify(ctx, credential, request.Map())
		if !assert.Falsef(t, err == nil || !strings.Contains(err.Error(), "transaction reverted"),
			"first Verify() error = %v, want reverted transaction", err) {
			return
		}
	}
	if !assert.Lenf(t, rpc.sentRawTxs, 1,
		"expected 1 broadcast after first Verify(), got %d", len(rpc.sentRawTxs)) {
		return
	}
	{

		_, err := intent.Verify(ctx, credential, request.Map())
		if !assert.Falsef(t, err == nil || !strings.Contains(err.Error(), "challenge already used"),
			"second Verify() error = %v, want reused challenge rejection", err) {
			return
		}
	}
	if !assert.Lenf(t, rpc.sentRawTxs, 1,
		"expected no second broadcast, got %d", len(rpc.sentRawTxs)) {
		return
	}

}

func TestChargeFlow_FeePayerTransactionFailsPreflightBeforeBroadcast(t *testing.T) {
	ctx := context.Background()
	request := buildRequest(t, true, nil)
	rpc := newMockRPC(request)
	rpc.onEstimateGas = func(params ...interface{}) (*temporpc.JSONRPCResponse, error) {
		callObject, ok := params[0].(map[string]any)
		if !assert.Truef(t, ok,
			"estimateGas params[0] type = %T, want map[string]any", params[0]) {
			return *new(*temporpc.JSONRPCResponse), *new(error)
		}

		if _, ok := callObject["calls"]; !ok {
			return &temporpc.JSONRPCResponse{Result: rpc.estimateGas}, nil
		}
		if !assert.NotEqual(t, "", callObject["from"],
			"estimateGas call object missing from") {
			return *new(*temporpc.JSONRPCResponse), *new(error)
		}
		if !assert.Equalf(t, request.Currency, callObject["feeToken"],
			"estimateGas feeToken = %v, want %s", callObject["feeToken"], request.Currency) {
			return *new(*temporpc.JSONRPCResponse), *new(error)
		}

		calls, ok := callObject["calls"].([]map[string]any)
		if !assert.Falsef(t, !ok || len(calls) == 0,
			"estimateGas calls = %#v, want non-empty call batch", callObject["calls"]) {
			return *new(*temporpc.JSONRPCResponse), *new(error)
		}
		{

			_, ok := callObject["nonceKey"]
			if !assert.True(t, ok,
				"estimateGas call object missing nonceKey") {
				return *new(*temporpc.JSONRPCResponse), *new(error)
			}
		}
		{

			_, ok := callObject["validBefore"]
			if !assert.True(t, ok,
				"estimateGas call object missing validBefore") {
				return *new(*temporpc.JSONRPCResponse), *new(error)
			}
		}

		return temporpc.NewJSONRPCErrorResponse(1, temporpc.InvalidTransactionType, "execution reverted", nil), nil
	}
	clientMethod := newClientMethod(t, rpc, tempo.CredentialTypeTransaction)
	challenge := buildChallenge(t, request)

	credential, err := clientMethod.CreateCredential(ctx, challenge)
	if !assert.NoErrorf(t, err,
		"CreateCredential() error = %v", err) {
		return
	}

	intent, err := NewIntent(IntentConfig{RPC: rpc, FeePayerPrivateKey: feePayerKey})
	if !assert.NoErrorf(t, err,
		"NewIntent() error = %v", err) {
		return
	}
	{

		_, err := intent.Verify(ctx, credential, request.Map())
		if !assert.Falsef(t, err == nil || !strings.Contains(err.Error(), "transaction preflight failed"),
			"Verify() error = %v, want preflight failure", err) {
			return
		}
	}
	if !assert.Lenf(t, rpc.sentRawTxs, 0,
		"expected no broadcast after failed preflight, got %d", len(rpc.sentRawTxs)) {
		return
	}
	if !assert.Falsef(t, len(rpc.estimateGasCalls) < 2,
		"expected client estimate and server preflight calls, got %d", len(rpc.estimateGasCalls)) {
		return
	}

}

func TestChargeFlow_RejectsUnsupportedFeePayerToken(t *testing.T) {
	ctx := context.Background()
	request, err := tempo.NormalizeChargeRequest(tempo.ChargeRequestParams{
		Amount:    "0.50",
		Currency:  "0x20c0000000000000000000000000000000000002",
		Recipient: testRecipient,
		Decimals:  6,
		ChainID:   42431,
		FeePayer:  true,
	})
	if !assert.NoErrorf(t, err,
		"NormalizeChargeRequest() error = %v", err) {
		return
	}

	rpc := newMockRPC(request)
	clientMethod := newClientMethod(t, rpc, tempo.CredentialTypeTransaction)
	challenge := buildChallenge(t, request)

	credential, err := clientMethod.CreateCredential(ctx, challenge)
	if !assert.NoErrorf(t, err,
		"CreateCredential() error = %v", err) {
		return
	}

	intent, err := NewIntent(IntentConfig{RPC: rpc, FeePayerPrivateKey: feePayerKey})
	if !assert.NoErrorf(t, err,
		"NewIntent() error = %v", err) {
		return
	}
	{

		_, err := intent.Verify(ctx, credential, request.Map())
		if !assert.Falsef(t, err == nil || !strings.Contains(err.Error(), "not supported"),
			"Verify() error = %v, want unsupported fee token rejection", err) {
			return
		}
	}
	if !assert.Lenf(t, rpc.sentRawTxs, 0,
		"expected unsupported fee token to be rejected before broadcast, got %d broadcasts", len(rpc.sentRawTxs)) {
		return
	}

}

func TestChargeFlow_CustomFeePayerPolicyAllowsConfiguredToken(t *testing.T) {
	ctx := context.Background()
	request, err := tempo.NormalizeChargeRequest(tempo.ChargeRequestParams{
		Amount:    "0.50",
		Currency:  "0x20c0000000000000000000000000000000000002",
		Recipient: testRecipient,
		Decimals:  6,
		ChainID:   42431,
		FeePayer:  true,
	})
	if !assert.NoErrorf(t, err,
		"NormalizeChargeRequest() error = %v", err) {
		return
	}

	rpc := newMockRPC(request)
	clientMethod := newClientMethod(t, rpc, tempo.CredentialTypeTransaction)
	challenge := buildChallenge(t, request)

	credential, err := clientMethod.CreateCredential(ctx, challenge)
	if !assert.NoErrorf(t, err,
		"CreateCredential() error = %v", err) {
		return
	}

	intent, err := NewIntent(IntentConfig{
		RPC:                rpc,
		FeePayerPrivateKey: feePayerKey,
		FeePayerPolicies: map[string]FeePayerPolicy{
			request.Currency: {
				MaxFeePerGas:         big.NewInt(10),
				MaxPriorityFeePerGas: big.NewInt(10),
				MaxTotalFee:          big.NewInt(1_000_000),
			},
		},
	})
	if !assert.NoErrorf(t, err,
		"NewIntent() error = %v", err) {
		return
	}
	{

		_, err := intent.Verify(ctx, credential, request.Map())
		if !assert.NoErrorf(t, err,
			"Verify() error = %v", err) {
			return
		}
	}
	if !assert.Lenf(t, rpc.sentRawTxs, 1,
		"expected configured fee token to broadcast once, got %d", len(rpc.sentRawTxs)) {
		return
	}

}

// TestChargeFlow_TransactionCredentialKeychain covers the new branch in
// verifyTransaction that accepts Keychain (Account Abstraction) envelopes.
// Without the fix every keychain-signed payment fails with
// "transaction signature is invalid" before it can ever reach RPC submission.
//
// The "legacy YParity" subtest covers tempo-cli ≤ 1.6 emitting the inner
// secp256k1 V in the {27, 28} EIP-155 form rather than the {0, 1} EIP-2 form
// that tempo-go's RecoverAddress requires; the byte-mutation must happen on
// a copy so the original raw envelope reaches SendRawTransaction unchanged.
//
// Strategy: borrow a clientMethod-built secp256k1 credential to get a tx with
// validly-encoded transfer Calls, then re-sign that tx with
// keychain.SignWithAccessKey before submitting it as the credential payload.
// The result is structurally indistinguishable from what tempo-cli sends.
func TestChargeFlow_TransactionCredentialKeychain(t *testing.T) {
	cases := []struct {
		name       string
		mutateRawV bool // flip inner-V from {0,1} → {27,28} after signing
	}{
		{name: "canonical YParity {0,1}", mutateRawV: false},
		{name: "legacy YParity {27,28}", mutateRawV: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			request := buildRequest(t, false, nil)
			rpc := newMockRPC(request)
			challenge := buildChallenge(t, request)

			rootSigner, err := temposigner.NewSigner(testPrivateKey)
			if err != nil {
				t.Fatalf("NewSigner(root) error = %v", err)
			}
			accessSigner, err := temposigner.NewSigner(feePayerKey)
			if err != nil {
				t.Fatalf("NewSigner(access) error = %v", err)
			}

			clientMethod := newClientMethod(t, rpc, tempo.CredentialTypeTransaction)
			seedCred, err := clientMethod.CreateCredential(ctx, challenge)
			if err != nil {
				t.Fatalf("CreateCredential(seed) error = %v", err)
			}
			seedRaw := seedCred.Payload["signature"].(string)
			tx, err := tempotx.Deserialize(seedRaw)
			if err != nil {
				t.Fatalf("Deserialize(seed) error = %v", err)
			}
			if err := keychain.SignWithAccessKey(tx, accessSigner, rootSigner.Address()); err != nil {
				t.Fatalf("SignWithAccessKey() error = %v", err)
			}
			if tc.mutateRawV {
				if tx.Signature.Raw[85] >= 2 {
					t.Fatalf("expected canonical YParity {0,1}, got 0x%02x", tx.Signature.Raw[85])
				}
				tx.Signature.Raw[85] += 27
			}
			originalByte85 := tx.Signature.Raw[85]
			serialized, err := tempotx.Serialize(tx, nil)
			if err != nil {
				t.Fatalf("Serialize() error = %v", err)
			}

			// The default mockRPC.onSend recovers the sender via off-chain
			// ecrecover, which doesn't speak keychain. Use the known root
			// address directly (the chain doesn't off-chain-verify either)
			// and assert the broadcast envelope is byte-identical to what
			// verifyTransaction received — i.e. our YParity normalisation
			// didn't mutate the original bytes en route to the broadcast.
			rpc.onSend = func(raw string) (string, map[string]any, error) {
				broadcast, err := tempotx.Deserialize(raw)
				if err != nil {
					return "", nil, err
				}
				if broadcast.Signature == nil || broadcast.Signature.Type != "keychain" {
					return "", nil, fmt.Errorf("expected broadcast keychain signature, got %#v", broadcast.Signature)
				}
				if broadcast.Signature.Raw[85] != originalByte85 {
					return "", nil, fmt.Errorf("broadcast YParity = 0x%02x, want unmodified 0x%02x — verifier mutated the envelope bytes", broadcast.Signature.Raw[85], originalByte85)
				}
				return testReceiptHash, buildReceipt(raw, request, rootSigner.Address()), nil
			}

			credential := &mpp.Credential{
				Challenge: challenge.ToEcho(),
				Payload: tempo.ChargeCredentialPayload{
					Type:      tempo.CredentialTypeTransaction,
					Signature: serialized,
				}.Map(),
			}

			intent, err := NewIntent(IntentConfig{RPC: rpc})
			if err != nil {
				t.Fatalf("NewIntent() error = %v", err)
			}
			receipt, err := intent.Verify(ctx, credential, request.Map())
			if err != nil {
				t.Fatalf("Verify() error = %v", err)
			}
			if receipt.Reference != testReceiptHash {
				t.Fatalf("receipt reference = %q, want %q", receipt.Reference, testReceiptHash)
			}
		})
	}
}

// TestTransactionMatches_AllowsKeyAuthorization covers AA wallet payments:
// tempo-cli's smart-account flow always populates tx.KeyAuthorization to scope
// which session key may execute the transaction, but the field is orthogonal
// to payment correctness — that's established by walking tx.Calls. The earlier
// `tx.KeyAuthorization != nil` reject made every keychain-signed payment fail
// transactionMatches before signature verification could even run.
func TestTransactionMatches_AllowsKeyAuthorization(t *testing.T) {
	request, err := tempo.NormalizeChargeRequest(tempo.ChargeRequestParams{
		Amount:    "0.50",
		Currency:  testCurrency,
		Recipient: testRecipient,
		Decimals:  6,
		ChainID:   42431,
	})
	if err != nil {
		t.Fatalf("NormalizeChargeRequest() error = %v", err)
	}
	challenge := buildChallenge(t, request)

	rootSigner, err := temposigner.NewSigner(testPrivateKey)
	if err != nil {
		t.Fatalf("NewSigner() error = %v", err)
	}
	clientMethod := newClientMethod(t, newMockRPC(request), tempo.CredentialTypeTransaction)
	credential, err := clientMethod.CreateCredential(context.Background(), challenge)
	if err != nil {
		t.Fatalf("CreateCredential() error = %v", err)
	}
	raw := credential.Payload["signature"].(string)
	tx, err := tempotx.Deserialize(raw)
	if err != nil {
		t.Fatalf("Deserialize() error = %v", err)
	}

	// Sanity: without KeyAuthorization the payment matches.
	if !transactionMatches(tx, request, challenge.Realm, challenge.ID) {
		t.Fatal("transactionMatches = false on baseline tx, want true")
	}

	// Now mark the tx as AA-style: authorize the same key that signed it.
	tx.KeyAuthorization = []interface{}{rootSigner.Address(), uint8(0)}
	if !transactionMatches(tx, request, challenge.Realm, challenge.ID) {
		t.Fatal("transactionMatches = false with KeyAuthorization set, want true (payment correctness comes from tx.Calls, not from the auth tuple)")
	}

	// And AccessList still rejects (separate concern, not relaxed).
	tx.KeyAuthorization = nil
	tx.AccessList = tempotx.AccessList{{Address: common.HexToAddress(testRecipient)}}
	if transactionMatches(tx, request, challenge.Realm, challenge.ID) {
		t.Fatal("transactionMatches = true with non-empty AccessList, want false")
	}
}

func TestFetchReceipt_RespectsContextCancellation(t *testing.T) {
	rpc := &mockRPC{receipts: map[string]map[string]any{}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	started := time.Now()
	_, err := fetchReceipt(ctx, rpc, testReceiptHash)
	if !assert.Falsef(t, err == nil || !strings.Contains(err.Error(), context.Canceled.Error()),
		"fetchReceipt() error = %v, want context cancellation", err) {
		return
	}

	if elapsed := time.Since(started); elapsed >= receiptRetryDelay/2 {
		assert.Failf(t, "", "fetchReceipt() took %s, want early cancellation before retry delay %s", elapsed, receiptRetryDelay)
		return
	}
}

func TestCanonicalReceiptTransfers_PairsDuplicateMemoTransfersWithDistinctBaseLogs(t *testing.T) {
	t.Parallel()

	transfers := []decodedTransfer{
		{amount: "500000", recipient: testRecipient},
		{amount: "500000", hasMemo: true, memo: "0x01", recipient: testRecipient},
		{amount: "500000", recipient: testRecipient},
		{amount: "500000", hasMemo: true, memo: "0x02", recipient: testRecipient},
	}

	got, ok := canonicalReceiptTransfers(transfers)
	want := []decodedTransfer{
		{amount: "500000", hasMemo: true, memo: "0x01", recipient: testRecipient},
		{amount: "500000", hasMemo: true, memo: "0x02", recipient: testRecipient},
	}

	assert.True(t, ok)
	assert.Equal(t, want, got)
}

func TestCanonicalReceiptTransfers_RejectsUnpairedMemoTransfers(t *testing.T) {
	t.Parallel()

	transfers := []decodedTransfer{
		{amount: "500000", recipient: testRecipient},
		{amount: "500000", hasMemo: true, memo: "0x01", recipient: testRecipient},
		{amount: "500000", recipient: testRecipient},
		{amount: "500000", hasMemo: true, memo: "0x02", recipient: testRecipient},
		{amount: "500000", hasMemo: true, memo: "0x03", recipient: testRecipient},
	}

	got, ok := canonicalReceiptTransfers(transfers)

	assert.False(t, ok)
	assert.Nil(t, got)
}

func newClientMethod(t *testing.T, rpc tempo.RPCClient, credentialType tempo.CredentialType) *chargeclient.Method {
	t.Helper()
	method, err := chargeclient.New(chargeclient.Config{
		PrivateKey:     testPrivateKey,
		RPC:            rpc,
		ChainID:        42431,
		CredentialType: credentialType,
	})
	if !assert.NoErrorf(t, err,
		"tempo/client.New() error = %v", err) {
		return *new(*chargeclient.Method)
	}

	return method
}

func buildRequest(t *testing.T, feePayer bool, modes []tempo.ChargeMode) tempo.ChargeRequest {
	t.Helper()
	request, err := tempo.NormalizeChargeRequest(tempo.ChargeRequestParams{
		Amount:         "0.50",
		Currency:       testCurrency,
		Recipient:      testRecipient,
		Decimals:       6,
		ChainID:        42431,
		FeePayer:       feePayer,
		SupportedModes: modes,
	})
	if !assert.NoErrorf(t, err,
		"NormalizeChargeRequest() error = %v", err) {
		return *new(tempo.ChargeRequest)
	}

	return request
}

func buildChallenge(t *testing.T, request tempo.ChargeRequest) *mpp.Challenge {
	t.Helper()
	return mpp.NewChallenge(
		"secret-key",
		testRealm,
		tempo.MethodName,
		tempo.IntentCharge,
		request.Map(),
		mpp.WithExpires(mpp.Expires.Minutes(5)),
	)
}

func newMockRPC(request tempo.ChargeRequest) *mockRPC {
	rpc := &mockRPC{
		chainID:     42431,
		nonce:       7,
		gasPrice:    "0x1",
		estimateGas: "0x5208",
		receipts:    map[string]map[string]any{},
	}
	rpc.onSend = func(raw string) (string, map[string]any, error) {
		tx, err := tempotx.Deserialize(raw)
		if err != nil {
			return "", nil, err
		}
		sender, err := tempotx.VerifySignature(tx)
		if err != nil {
			return "", nil, err
		}
		return testReceiptHash, buildReceipt(raw, request, sender), nil
	}
	return rpc
}

func buildReceipt(raw string, request tempo.ChargeRequest, sender common.Address) map[string]any {
	tx, _ := tempotx.Deserialize(raw)
	logs := make([]any, 0, len(tx.Calls)*2)
	for _, call := range tx.Calls {
		callData := common.Bytes2Hex(call.Data)
		amount := new(big.Int)
		amount.SetString(callData[72:136], 16)
		recipient := common.HexToAddress("0x" + callData[32:72]).Hex()
		if strings.HasPrefix(callData, tempo.TransferWithMemoSelector) {
			logs = append(logs, transferLog(request.Currency, sender.Hex(), recipient, amount, ""))
			logs = append(logs, transferLog(request.Currency, sender.Hex(), recipient, amount, "0x"+callData[136:200]))
			continue
		}
		logs = append(logs, transferLog(request.Currency, sender.Hex(), recipient, amount, ""))
	}
	return map[string]any{
		"status": "0x1",
		"logs":   logs,
	}
}

func transferLog(currency, sender, recipient string, amount *big.Int, memo string) map[string]any {
	topics := []any{
		tempo.TransferTopic.Hex(),
		addressTopic(sender),
		addressTopic(recipient),
	}
	if memo != "" {
		topics[0] = tempo.TransferWithMemoTopic.Hex()
		topics = append(topics, memo)
	}
	return map[string]any{
		"address": currency,
		"topics":  topics,
		"data":    fmt.Sprintf("0x%064x", amount),
	}
}

func encodeActiveKeyInfo(accessKey common.Address, expiry int64) string {
	result := make([]byte, 160)
	copy(result[44:64], accessKey.Bytes())
	new(big.Int).SetInt64(expiry).FillBytes(result[64:96])
	return hexutil.Encode(result)
}

func addressTopic(address string) string {
	return fmt.Sprintf("0x%064s", strings.TrimPrefix(strings.ToLower(address), "0x"))
}

func init() {
	_, _ = temposigner.NewSigner(testPrivateKey)
}
