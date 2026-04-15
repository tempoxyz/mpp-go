package server

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/tempoxyz/mpp-go/pkg/mpp"
	"github.com/tempoxyz/mpp-go/pkg/tempo"
	tempoclient "github.com/tempoxyz/mpp-go/pkg/tempo/client"
	temporpc "github.com/tempoxyz/tempo-go/pkg/client"
	temposigner "github.com/tempoxyz/tempo-go/pkg/signer"
	tempotx "github.com/tempoxyz/tempo-go/pkg/transaction"
)

const (
	testPrivateKey  = "0x59c6995e998f97a5a0044966f0945389dc9e86dae88c7a8412f4603b6b78690d"
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
	case "eth_getTransactionReceipt":
		hash := params[0].(string)
		return &temporpc.JSONRPCResponse{Result: m.receipts[hash]}, nil
	default:
		return nil, fmt.Errorf("unexpected rpc method %q", method)
	}
}

func TestChargeFlow_TransactionCredential(t *testing.T) {
	ctx := context.Background()
	request := buildRequest(t, false, nil)
	rpc := newMockRPC(request)
	clientMethod := newClientMethod(t, rpc, tempo.CredentialTypeTransaction)
	challenge := buildChallenge(t, request)

	credential, err := clientMethod.CreateCredential(ctx, challenge)
	if err != nil {
		t.Fatalf("CreateCredential() error = %v", err)
	}

	intent, err := NewChargeIntent(ChargeIntentConfig{RPC: rpc})
	if err != nil {
		t.Fatalf("NewChargeIntent() error = %v", err)
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
}

func TestChargeFlow_FeePayerTransaction(t *testing.T) {
	ctx := context.Background()
	request := buildRequest(t, true, nil)
	rpc := newMockRPC(request)
	clientMethod := newClientMethod(t, rpc, tempo.CredentialTypeTransaction)
	challenge := buildChallenge(t, request)

	credential, err := clientMethod.CreateCredential(ctx, challenge)
	if err != nil {
		t.Fatalf("CreateCredential() error = %v", err)
	}
	signature := credential.Payload["signature"].(string)
	if !strings.Contains(signature, "feefeefeefee") {
		t.Fatalf("expected sponsored client transaction marker, got %q", signature)
	}

	intent, err := NewChargeIntent(ChargeIntentConfig{RPC: rpc, FeePayerPrivateKey: feePayerKey})
	if err != nil {
		t.Fatalf("NewChargeIntent() error = %v", err)
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

	intent, err := NewChargeIntent(ChargeIntentConfig{RPC: rpc})
	if err != nil {
		t.Fatalf("NewChargeIntent() error = %v", err)
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

func TestChargeFlow_HashCredentialReplayProtected(t *testing.T) {
	ctx := context.Background()
	modes := []tempo.ChargeMode{tempo.ChargeModePush}
	request := buildRequest(t, false, modes)
	rpc := newMockRPC(request)
	clientMethod := newClientMethod(t, rpc, tempo.CredentialTypeHash)
	challenge := buildChallenge(t, request)

	credential, err := clientMethod.CreateCredential(ctx, challenge)
	if err != nil {
		t.Fatalf("CreateCredential() error = %v", err)
	}
	if credential.Payload["type"] != "hash" {
		t.Fatalf("expected hash credential, got %#v", credential.Payload)
	}
	if len(rpc.sentRawTxs) != 1 {
		t.Fatalf("expected client broadcast, got %d", len(rpc.sentRawTxs))
	}

	intent, err := NewChargeIntent(ChargeIntentConfig{RPC: rpc})
	if err != nil {
		t.Fatalf("NewChargeIntent() error = %v", err)
	}
	if _, err := intent.Verify(ctx, credential, request.Map()); err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if _, err := intent.Verify(ctx, credential, request.Map()); err == nil {
		t.Fatal("expected replay-protection error on second verification")
	}
}

func newClientMethod(t *testing.T, rpc tempo.RPCClient, credentialType tempo.CredentialType) *tempoclient.Method {
	t.Helper()
	method, err := tempoclient.New(tempoclient.Config{
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
	callData := common.Bytes2Hex(tx.Calls[0].Data)
	amount, _ := new(big.Int).SetString(request.Amount, 10)
	topics := []any{
		tempo.TransferWithMemoTopic.Hex(),
		addressTopic(sender.Hex()),
		addressTopic(request.Recipient),
		"0x" + callData[136:200],
	}
	return map[string]any{
		"status": "0x1",
		"logs": []any{
			map[string]any{
				"address": request.Currency,
				"topics":  topics,
				"data":    fmt.Sprintf("0x%064x", amount),
			},
		},
	}
}

func addressTopic(address string) string {
	return fmt.Sprintf("0x%064s", strings.TrimPrefix(strings.ToLower(address), "0x"))
}

func init() {
	_, _ = temposigner.NewSigner(testPrivateKey)
}
