package chargeclient

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

func TestCreateCredentialScenarios(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		config           Config
		rpc              *mockRPC
		params           tempo.ChargeRequestParams
		wantErr          string
		wantType         tempo.CredentialType
		wantPayloadKey   string
		wantPayload      string
		wantBroadcasts   int
		wantRawPrefix    string
		wantSourcePrefix string
	}{
		{
			name: "hash credential rejected for fee payer challenge",
			config: Config{
				ChainID:        42431,
				CredentialType: tempo.CredentialTypeHash,
			},
			rpc: &mockRPC{chainID: 42431},
			params: tempo.ChargeRequestParams{
				Amount:    "0.50",
				Currency:  testCurrency,
				Recipient: testRecipient,
				Decimals:  6,
				ChainID:   42431,
				FeePayer:  true,
			},
			wantErr:        "hash credentials cannot be used with fee payer challenges",
			wantBroadcasts: 0,
		},
		{
			name: "chain id mismatch",
			config: Config{
				ChainID: 42431,
			},
			rpc: &mockRPC{chainID: 1},
			params: tempo.ChargeRequestParams{
				Amount:    "0.50",
				Currency:  testCurrency,
				Recipient: testRecipient,
				Decimals:  6,
				ChainID:   42431,
			},
			wantErr:        "chain id mismatch",
			wantBroadcasts: 0,
		},
		{
			name: "hash credential broadcasts",
			config: Config{
				ChainID:        42431,
				CredentialType: tempo.CredentialTypeHash,
			},
			rpc: &mockRPC{
				chainID:     42431,
				nonce:       7,
				gasPrice:    "0x1",
				estimateGas: "0x5208",
				txHash:      testTxHash,
			},
			params: tempo.ChargeRequestParams{
				Amount:         "0.50",
				Currency:       testCurrency,
				Recipient:      testRecipient,
				Decimals:       6,
				ChainID:        42431,
				SupportedModes: []tempo.ChargeMode{tempo.ChargeModePush},
			},
			wantType:         tempo.CredentialTypeHash,
			wantPayloadKey:   "hash",
			wantPayload:      testTxHash,
			wantBroadcasts:   1,
			wantRawPrefix:    "0x76",
			wantSourcePrefix: "did:pkh:eip155:42431:0x",
		},
		{
			name: "zero amount uses proof credential",
			config: Config{
				ChainID:        42431,
				CredentialType: tempo.CredentialTypeHash,
			},
			rpc: &mockRPC{chainID: 42431},
			params: tempo.ChargeRequestParams{
				Amount:    "0",
				Currency:  testCurrency,
				Recipient: testRecipient,
				Decimals:  6,
				ChainID:   42431,
			},
			wantType:         tempo.CredentialTypeProof,
			wantPayloadKey:   "signature",
			wantBroadcasts:   0,
			wantSourcePrefix: "did:pkh:eip155:42431:0x",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			config := tt.config
			config.PrivateKey = testPrivateKey
			config.RPC = tt.rpc

			method, err := New(config)
			if err != nil {
				t.Fatalf("New() error = %v", err)
			}

			challenge := buildChallenge(t, tt.params)
			credential, err := method.CreateCredential(context.Background(), challenge)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("CreateCredential() error = %v, want substring %q", err, tt.wantErr)
				}
				if got := len(tt.rpc.sentRawTxs); got != tt.wantBroadcasts {
					t.Fatalf("len(tt.rpc.sentRawTxs) = %d, want %d", got, tt.wantBroadcasts)
				}
				return
			}
			if err != nil {
				t.Fatalf("CreateCredential() error = %v", err)
			}

			if credential.Payload["type"] != string(tt.wantType) {
				t.Fatalf("credential.Payload[type] = %#v, want %q", credential.Payload["type"], tt.wantType)
			}
			if tt.wantPayload != "" && credential.Payload[tt.wantPayloadKey] != tt.wantPayload {
				t.Fatalf("credential.Payload[%s] = %#v, want %q", tt.wantPayloadKey, credential.Payload[tt.wantPayloadKey], tt.wantPayload)
			}
			if tt.wantPayloadKey == "signature" {
				if _, ok := credential.Payload[tt.wantPayloadKey].(string); !ok {
					t.Fatalf("credential.Payload[%s] = %#v, want string", tt.wantPayloadKey, credential.Payload[tt.wantPayloadKey])
				}
			}
			if tt.wantSourcePrefix != "" && !strings.HasPrefix(credential.Source, tt.wantSourcePrefix) {
				t.Fatalf("credential.Source = %q, want prefix %q", credential.Source, tt.wantSourcePrefix)
			}
			if got := len(tt.rpc.sentRawTxs); got != tt.wantBroadcasts {
				t.Fatalf("len(tt.rpc.sentRawTxs) = %d, want %d", got, tt.wantBroadcasts)
			}
			if tt.wantRawPrefix != "" && !strings.HasPrefix(tt.rpc.sentRawTxs[0], tt.wantRawPrefix) {
				t.Fatalf("sent raw tx prefix = %q, want %q", tt.rpc.sentRawTxs[0][:4], tt.wantRawPrefix)
			}
		})
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
