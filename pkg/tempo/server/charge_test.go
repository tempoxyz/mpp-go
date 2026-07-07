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
	proofHash, err := tempo.ProofTypedDataHash(42431, challenge.ID, challenge.Realm)
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
	proofHash, err := tempo.ProofTypedDataHash(42431, challenge.ID, challenge.Realm)
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

	got := canonicalReceiptTransfers(transfers)
	want := []decodedTransfer{
		{amount: "500000", hasMemo: true, memo: "0x01", recipient: testRecipient},
		{amount: "500000", hasMemo: true, memo: "0x02", recipient: testRecipient},
	}

	assert.Equal(t, want, got)
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
