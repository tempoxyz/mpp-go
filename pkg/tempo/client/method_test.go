package tempoclient

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/tempoxyz/mpp-go/pkg/mpp"
	"github.com/tempoxyz/mpp-go/pkg/tempo"
	temporpc "github.com/tempoxyz/tempo-go/pkg/client"
)

const (
	// testPrivateKey is the fixed payer key used in Tempo client tests.
	testPrivateKey = "0x59c6995e998f97a5a0044966f0945389dc9e86dae88c7a8412f4603b6b78690d"
	testCurrency   = "0x20c0000000000000000000000000000000000001"
	testRecipient  = "0x70997970c51812dc3a010c7d01b50e0d17dc79c8"
	testRealm      = "api.example.com"
	testTxHash     = "0xabc123"
)

type mockRPC struct {
	chainID     uint64
	nonce       uint64
	gasPrice    string
	estimateGas string
	sentRawTxs  []string
	txHash      string
}

func (m *mockRPC) GetChainID(context.Context) (uint64, error) {
	return m.chainID, nil
}

func (m *mockRPC) GetTransactionCount(context.Context, string) (uint64, error) {
	return m.nonce, nil
}

func (m *mockRPC) SendRawTransaction(_ context.Context, serialized string) (string, error) {
	m.sentRawTxs = append(m.sentRawTxs, serialized)
	if m.txHash != "" {
		return m.txHash, nil
	}
	return testTxHash, nil
}

func (m *mockRPC) SendRequest(_ context.Context, method string, _ ...interface{}) (*temporpc.JSONRPCResponse, error) {
	switch method {
	case "eth_gasPrice":
		return &temporpc.JSONRPCResponse{Result: m.gasPrice}, nil
	case "eth_estimateGas":
		return &temporpc.JSONRPCResponse{Result: m.estimateGas}, nil
	default:
		return nil, fmt.Errorf("unexpected rpc method %q", method)
	}
}

func TestCreateCredential_RejectsHashCredentialForFeePayerChallenge(t *testing.T) {
	t.Parallel()

	method, err := New(Config{PrivateKey: testPrivateKey, CredentialType: tempo.CredentialTypeHash})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	challenge := buildChallenge(t, tempo.ChargeRequestParams{
		Amount:    "0.50",
		Currency:  testCurrency,
		Recipient: testRecipient,
		Decimals:  6,
		ChainID:   42431,
		FeePayer:  true,
	})

	_, err = method.CreateCredential(context.Background(), challenge)
	if err == nil || !strings.Contains(err.Error(), "hash credentials cannot be used with fee payer challenges") {
		t.Fatalf("CreateCredential() error = %v, want fee payer hash rejection", err)
	}
}

func TestCreateCredential_RejectsChainIDMismatch(t *testing.T) {
	t.Parallel()

	rpc := &mockRPC{chainID: 1}
	method, err := New(Config{PrivateKey: testPrivateKey, RPC: rpc, ChainID: 42431})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	challenge := buildChallenge(t, tempo.ChargeRequestParams{
		Amount:    "0.50",
		Currency:  testCurrency,
		Recipient: testRecipient,
		Decimals:  6,
		ChainID:   42431,
	})

	_, err = method.CreateCredential(context.Background(), challenge)
	if err == nil || !strings.Contains(err.Error(), "chain id mismatch") {
		t.Fatalf("CreateCredential() error = %v, want chain mismatch error", err)
	}
}

func TestCreateCredential_HashCredentialBroadcasts(t *testing.T) {
	t.Parallel()

	rpc := &mockRPC{
		chainID:     42431,
		nonce:       7,
		gasPrice:    "0x1",
		estimateGas: "0x5208",
		txHash:      testTxHash,
	}
	method, err := New(Config{
		PrivateKey:     testPrivateKey,
		RPC:            rpc,
		ChainID:        42431,
		CredentialType: tempo.CredentialTypeHash,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	challenge := buildChallenge(t, tempo.ChargeRequestParams{
		Amount:         "0.50",
		Currency:       testCurrency,
		Recipient:      testRecipient,
		Decimals:       6,
		ChainID:        42431,
		SupportedModes: []tempo.ChargeMode{tempo.ChargeModePush},
	})

	credential, err := method.CreateCredential(context.Background(), challenge)
	if err != nil {
		t.Fatalf("CreateCredential() error = %v", err)
	}
	if credential.Payload["type"] != string(tempo.CredentialTypeHash) {
		t.Fatalf("credential.Payload[type] = %#v, want hash", credential.Payload["type"])
	}
	if credential.Payload["hash"] != testTxHash {
		t.Fatalf("credential.Payload[hash] = %#v, want %q", credential.Payload["hash"], testTxHash)
	}
	if len(rpc.sentRawTxs) != 1 {
		t.Fatalf("len(rpc.sentRawTxs) = %d, want 1", len(rpc.sentRawTxs))
	}
	if !strings.HasPrefix(rpc.sentRawTxs[0], "0x76") {
		t.Fatalf("sent raw tx prefix = %q, want 0x76", rpc.sentRawTxs[0][:4])
	}
}

func buildChallenge(t *testing.T, params tempo.ChargeRequestParams) *mpp.Challenge {
	t.Helper()

	request, err := tempo.NormalizeChargeRequest(params)
	if err != nil {
		t.Fatalf("NormalizeChargeRequest() error = %v", err)
	}

	return mpp.NewChallenge(
		"secret-key",
		testRealm,
		tempo.MethodName,
		tempo.IntentCharge,
		request.Map(),
		mpp.WithExpires(mpp.Expires.Minutes(5)),
	)
}
