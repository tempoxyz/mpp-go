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
	chainID     uint64
	nonce       uint64
	gasPrice    string
	estimateGas string
	callResult  string
	receipts    map[string]map[string]any
	sentRawTxs  []string
	onSend      func(raw string) (string, map[string]any, error)
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
		return &temporpc.JSONRPCResponse{Result: m.estimateGas}, nil
	case "eth_call":
		return &temporpc.JSONRPCResponse{Result: m.callResult}, nil
	case "eth_getTransactionReceipt":
		hash := params[0].(string)
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
	if err != nil {
		t.Fatalf("CreateCredential() error = %v", err)
	}
	raw := credential.Payload["signature"].(string)

	feePayerSigner, err := temposigner.NewSigner(feePayerKey)
	if err != nil {
		t.Fatalf("NewSigner() error = %v", err)
	}
	feePayerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Method string   `json:"method"`
			Params []string `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("Decode(request) error = %v", err)
		}
		if body.Method != "eth_signRawTransaction" {
			t.Fatalf("method = %q, want eth_signRawTransaction", body.Method)
		}
		if len(body.Params) != 1 || body.Params[0] != raw {
			t.Fatalf("params = %#v, want original raw tx", body.Params)
		}
		coSignedTx, err := tempotx.Deserialize(raw)
		if err != nil {
			t.Fatalf("Deserialize(raw) error = %v", err)
		}
		sender, err := tempotx.VerifySignature(coSignedTx)
		if err != nil {
			t.Fatalf("VerifySignature() error = %v", err)
		}
		coSignedTx.From = sender
		coSignedTx.FeeToken = common.HexToAddress(request.Currency)
		coSignedTx.AwaitingFeePayer = false
		if err := tempotx.AddFeePayerSignature(coSignedTx, feePayerSigner); err != nil {
			t.Fatalf("AddFeePayerSignature() error = %v", err)
		}
		serialized, err := tempotx.Serialize(coSignedTx, nil)
		if err != nil {
			t.Fatalf("Serialize() error = %v", err)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": serialized})
	}))
	defer feePayerServer.Close()
	request.MethodDetails.FeePayerURL = feePayerServer.URL

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
	if len(rpc.sentRawTxs) != 1 {
		t.Fatalf("expected 1 broadcast, got %d", len(rpc.sentRawTxs))
	}
	if strings.Contains(rpc.sentRawTxs[0], "feefeefeefee") {
		t.Fatalf("broadcast transaction still contains fee payer marker: %q", rpc.sentRawTxs[0])
	}
}

func TestChargeFlow_FeePayerTransactionViaRemoteSignerRejectsTamperedFeeToken(t *testing.T) {
	ctx := context.Background()
	request := buildRequest(t, true, nil)
	rpc := newMockRPC(request)
	clientMethod := newClientMethod(t, rpc, tempo.CredentialTypeTransaction)
	challenge := buildChallenge(t, request)

	credential, err := clientMethod.CreateCredential(ctx, challenge)
	if err != nil {
		t.Fatalf("CreateCredential() error = %v", err)
	}
	raw := credential.Payload["signature"].(string)

	feePayerSigner, err := temposigner.NewSigner(feePayerKey)
	if err != nil {
		t.Fatalf("NewSigner() error = %v", err)
	}
	feePayerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		coSignedTx, err := tempotx.Deserialize(raw)
		if err != nil {
			t.Fatalf("Deserialize(raw) error = %v", err)
		}
		sender, err := tempotx.VerifySignature(coSignedTx)
		if err != nil {
			t.Fatalf("VerifySignature() error = %v", err)
		}
		coSignedTx.From = sender
		coSignedTx.FeeToken = common.HexToAddress("0x20c0000000000000000000000000000000000002")
		coSignedTx.AwaitingFeePayer = false
		if err := tempotx.AddFeePayerSignature(coSignedTx, feePayerSigner); err != nil {
			t.Fatalf("AddFeePayerSignature() error = %v", err)
		}
		serialized, err := tempotx.Serialize(coSignedTx, nil)
		if err != nil {
			t.Fatalf("Serialize() error = %v", err)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": serialized})
	}))
	defer feePayerServer.Close()
	request.MethodDetails.FeePayerURL = feePayerServer.URL

	intent, err := NewIntent(IntentConfig{RPC: rpc})
	if err != nil {
		t.Fatalf("NewIntent() error = %v", err)
	}
	if _, err := intent.Verify(ctx, credential, request.Map()); err == nil || !strings.Contains(err.Error(), "fee token") {
		t.Fatalf("Verify() error = %v, want fee token rejection", err)
	}
	if len(rpc.sentRawTxs) != 0 {
		t.Fatalf("expected tampered transaction to be rejected before broadcast, got %d broadcasts", len(rpc.sentRawTxs))
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
	if err != nil {
		t.Fatalf("NormalizeChargeRequest() error = %v", err)
	}
	rpc := newMockRPC(request)
	challenge := buildChallenge(t, request)

	rootSigner, err := temposigner.NewSigner(testPrivateKey)
	if err != nil {
		t.Fatalf("NewSigner(root) error = %v", err)
	}
	accessKey, err := temposigner.NewSigner(feePayerKey)
	if err != nil {
		t.Fatalf("NewSigner(access key) error = %v", err)
	}
	proofHash, err := tempo.ProofTypedDataHash(42431, challenge.ID)
	if err != nil {
		t.Fatalf("ProofTypedDataHash() error = %v", err)
	}
	v2Payload := make([]byte, 0, 1+len(proofHash.Bytes())+common.AddressLength)
	v2Payload = append(v2Payload, keychain.KeychainSignatureType)
	v2Payload = append(v2Payload, proofHash.Bytes()...)
	v2Payload = append(v2Payload, rootSigner.Address().Bytes()...)
	innerSignature, err := accessKey.Sign(crypto.Keccak256Hash(v2Payload))
	if err != nil {
		t.Fatalf("accessKey.Sign() error = %v", err)
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
	if err != nil {
		t.Fatalf("NewIntent() error = %v", err)
	}
	receipt, err := intent.Verify(ctx, credential, request.Map())
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if receipt.Reference != challenge.ID {
		t.Fatalf("receipt reference = %q, want %q", receipt.Reference, challenge.ID)
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
	if err != nil {
		t.Fatalf("NormalizeChargeRequest() error = %v", err)
	}
	rpc := newMockRPC(request)
	challenge := buildChallenge(t, request)

	rootSigner, err := temposigner.NewSigner(testPrivateKey)
	if err != nil {
		t.Fatalf("NewSigner(root) error = %v", err)
	}
	accessKey, err := temposigner.NewSigner(feePayerKey)
	if err != nil {
		t.Fatalf("NewSigner(access key) error = %v", err)
	}
	proofHash, err := tempo.ProofTypedDataHash(42431, challenge.ID)
	if err != nil {
		t.Fatalf("ProofTypedDataHash() error = %v", err)
	}
	v2Payload := make([]byte, 0, 1+len(proofHash.Bytes())+common.AddressLength)
	v2Payload = append(v2Payload, keychain.KeychainSignatureType)
	v2Payload = append(v2Payload, proofHash.Bytes()...)
	v2Payload = append(v2Payload, rootSigner.Address().Bytes()...)
	innerSignature, err := accessKey.Sign(crypto.Keccak256Hash(v2Payload))
	if err != nil {
		t.Fatalf("accessKey.Sign() error = %v", err)
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
	if err != nil {
		t.Fatalf("NewIntent() error = %v", err)
	}
	receipt, err := intent.Verify(ctx, credential, request.Map())
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if receipt.Reference != challenge.ID {
		t.Fatalf("receipt reference = %q, want %q", receipt.Reference, challenge.ID)
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
	if err != nil {
		t.Fatalf("NormalizeChargeRequest() error = %v", err)
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
	if err != nil {
		t.Fatalf("CreateCredential() error = %v", err)
	}

	intent, err := NewIntent(IntentConfig{RPC: rpc})
	if err != nil {
		t.Fatalf("NewIntent() error = %v", err)
	}
	if _, err := intent.Verify(ctx, credential, request.Map()); err == nil || !strings.Contains(err.Error(), "does not satisfy") {
		t.Fatalf("Verify() error = %v, want receipt mismatch", err)
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
	if err != nil {
		t.Fatalf("NormalizeChargeRequest() error = %v", err)
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
	if err != nil {
		t.Fatalf("CreateCredential() error = %v", err)
	}

	intent, err := NewIntent(IntentConfig{RPC: rpc})
	if err != nil {
		t.Fatalf("NewIntent() error = %v", err)
	}
	if _, err := intent.Verify(ctx, credential, request.Map()); err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
}

func TestChargeFlow_RejectsMalformedCredentialSource(t *testing.T) {
	ctx := context.Background()
	request := buildRequest(t, false, nil)
	rpc := newMockRPC(request)
	clientMethod := newClientMethod(t, rpc, tempo.CredentialTypeTransaction)
	challenge := buildChallenge(t, request)

	credential, err := clientMethod.CreateCredential(ctx, challenge)
	if err != nil {
		t.Fatalf("CreateCredential() error = %v", err)
	}
	credential.Source = "not-a-did"

	intent, err := NewIntent(IntentConfig{RPC: rpc})
	if err != nil {
		t.Fatalf("NewIntent() error = %v", err)
	}
	if _, err := intent.Verify(ctx, credential, request.Map()); err == nil || !strings.Contains(err.Error(), "credential source is invalid") {
		t.Fatalf("Verify() error = %v, want invalid credential source", err)
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
	if err != nil {
		t.Fatalf("CreateCredential() error = %v", err)
	}

	intent, err := NewIntent(IntentConfig{RPC: rpc, FeePayerPrivateKey: feePayerKey})
	if err != nil {
		t.Fatalf("NewIntent() error = %v", err)
	}
	if _, err := intent.Verify(ctx, credential, request.Map()); err == nil || !strings.Contains(err.Error(), "sponsor policy") {
		t.Fatalf("Verify() error = %v, want sponsor policy rejection", err)
	}
}

func TestFetchReceipt_RespectsContextCancellation(t *testing.T) {
	rpc := &mockRPC{receipts: map[string]map[string]any{}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	started := time.Now()
	_, err := fetchReceipt(ctx, rpc, testReceiptHash)
	if err == nil || !strings.Contains(err.Error(), context.Canceled.Error()) {
		t.Fatalf("fetchReceipt() error = %v, want context cancellation", err)
	}
	if elapsed := time.Since(started); elapsed >= receiptRetryDelay/2 {
		t.Fatalf("fetchReceipt() took %s, want early cancellation before retry delay %s", elapsed, receiptRetryDelay)
	}
}

func newClientMethod(t *testing.T, rpc tempo.RPCClient, credentialType tempo.CredentialType) *chargeclient.Method {
	t.Helper()
	method, err := chargeclient.New(chargeclient.Config{
		PrivateKey:     testPrivateKey,
		RPC:            rpc,
		ChainID:        42431,
		CredentialType: credentialType,
	})
	if err != nil {
		t.Fatalf("tempo/client.New() error = %v", err)
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
	if err != nil {
		t.Fatalf("NormalizeChargeRequest() error = %v", err)
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
