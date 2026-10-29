package chargeserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/tempoxyz/mpp-go/pkg/internal/mpptest"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	temporpc "github.com/tempoxyz/tempo-go/pkg/client"
	"github.com/tempoxyz/tempo-go/pkg/keychain"
	temposigner "github.com/tempoxyz/tempo-go/pkg/signer"
	tempotx "github.com/tempoxyz/tempo-go/pkg/transaction"

	"github.com/tempoxyz/mpp-go/pkg/mpp"
	"github.com/tempoxyz/mpp-go/pkg/server"
	"github.com/tempoxyz/mpp-go/pkg/tempo"
	chargeclient "github.com/tempoxyz/mpp-go/pkg/tempo/client"
)

const (
	// testPrivateKey is the fixed payer key used across Tempo charge tests.
	testPrivateKey = "0x59c6995e998f97a5a0044966f0945389dc9e86dae88c7a8412f4603b6b78690d"
	// secondPayerPrivateKey signs credentials for a payer distinct from testPrivateKey.
	secondPayerPrivateKey = "0x5de4111afa1a4b94908f83103eb1f1706367c2e68ca870fc3fb9a804cdab365a"
	// feePayerKey is the co-signer key used for sponsored-transaction tests.
	feePayerKey = "0xdd83cd66cd98801a07e0b7c1a5b02364b369e696da7c0ab444acffea5cca86fc"
	// accessKey is a deterministic test-only key authorized by testPrivateKey.
	accessKey       = "0x0000000000000000000000000000000000000000000000000000000000000003"
	testCurrency    = "0x20c0000000000000000000000000000000000001"
	testRecipient   = "0x70997970c51812dc3a010c7d01b50e0d17dc79c8"
	testRealm       = "api.example.com"
	testReceiptHash = "0x2120c5a4e1f6b7d9c3a8e2f4b6d8a0c2e4f6a8b0c2d4e6f8a0b2c4d6e8f0a2b4"
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
	callRequests     []map[string]any
	estimateGasCalls []map[string]any
	onCall           func(params ...interface{}) (*temporpc.JSONRPCResponse, error)
	onSend           func(raw string) (string, map[string]any, error)
	onEstimateGas    func(params ...interface{}) (*temporpc.JSONRPCResponse, error)
	onGetReceipt     func(hash string) (*temporpc.JSONRPCResponse, error)
	onGetTransaction func(hash string) (*temporpc.JSONRPCResponse, error)
	// balances answers TIP-20 balanceOf eth_calls by token address; those
	// calls are recorded in balanceCalls, not callRequests or onCall.
	balances     map[common.Address]*big.Int
	balanceCalls []common.Address
}

type recordingStore struct {
	inner            tempo.Store
	deleteErr        error
	deleteCalls      int
	putCalls         int
	putIfAbsentCalls int
}

func newRecordingStore() *recordingStore {
	return &recordingStore{inner: tempo.NewMemoryStore()}
}

func (s *recordingStore) Get(ctx context.Context, key string) (string, bool, error) {
	return s.inner.Get(ctx, key)
}

func (s *recordingStore) Put(ctx context.Context, key, value string) error {
	s.putCalls++
	return s.inner.Put(ctx, key, value)
}

func (s *recordingStore) PutIfAbsent(ctx context.Context, key, value string) (bool, error) {
	s.putIfAbsentCalls++
	return s.inner.PutIfAbsent(ctx, key, value)
}

func (s *recordingStore) Delete(ctx context.Context, key string) error {
	s.deleteCalls++
	if s.deleteErr != nil {
		return s.deleteErr
	}
	return s.inner.Delete(ctx, key)
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
		return mustTxHash(serialized), nil
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
		if len(params) > 0 {
			if callObject, ok := params[0].(map[string]any); ok {
				if data, _ := callObject["data"].(string); strings.HasPrefix(data, "0x70a08231") {
					token := common.HexToAddress(callObject["to"].(string))
					m.balanceCalls = append(m.balanceCalls, token)
					balance := m.balances[token]
					if balance == nil {
						balance = new(big.Int)
					}
					return &temporpc.JSONRPCResponse{Result: fmt.Sprintf("0x%064x", balance)}, nil
				}
			}
		}
		if len(params) > 0 {
			if callObject, ok := params[0].(map[string]any); ok {
				m.callRequests = append(m.callRequests, callObject)
			}
		}
		if m.onCall != nil {
			return m.onCall(params...)
		}
		return &temporpc.JSONRPCResponse{Result: m.callResult}, nil
	case "eth_getTransactionReceipt":
		hash := params[0].(string)
		if m.onGetReceipt != nil {
			return m.onGetReceipt(hash)
		}
		return &temporpc.JSONRPCResponse{Result: m.receipts[hash]}, nil
	case "eth_getTransactionByHash":
		if m.onGetTransaction != nil {
			return m.onGetTransaction(params[0].(string))
		}
		return nil, fmt.Errorf("unexpected rpc method %q", method)
	default:
		return nil, fmt.Errorf("unexpected rpc method %q", method)
	}
}

func TestIntentTransactionCredentialLifecycle(t *testing.T) {
	ctx := context.Background()
	request := buildRequest(t, false, []tempo.ChargeMode{tempo.ChargeModePull})
	rpc := newMockRPC(request)
	requestMap := request.Map()
	scope := map[string]any{
		"resource": "/paid",
		"route":    "/paid",
	}
	requestMap["_mppx_scope"] = scope
	challenge := mpptest.NewChallenge(
		"test-secret-key-minimum-32-byte-secret",
		testRealm,
		tempo.MethodName,
		tempo.IntentCharge,
		requestMap,
		mpp.WithExpires(mpp.ExpiresIn(5*time.Minute)),
	)
	credential, err := newClientMethod(t, rpc, tempo.CredentialTypeTransaction).CreateCredential(ctx, challenge)
	require.NoError(t, err)
	rpc.callRequests = nil
	store := newRecordingStore()
	method, err := New(Config{RPC: rpc, Store: store})
	require.NoError(t, err)
	payment, err := server.New(method, testRealm, "test-secret-key-minimum-32-byte-secret")
	require.NoError(t, err)

	validation, err := payment.ValidateCredential(ctx, credential)
	require.NoError(t, err)
	assert.Equal(t, "pull", validation.Details["mode"])
	assert.Equal(t, credential.Source, validation.Source)
	assert.NotEmpty(t, validation.Details["sender"])
	assert.Equal(t, credential.Payload["signature"], validation.Details["serializedTransaction"])
	assert.Len(t, validation.Details["transfers"], 1)
	assert.Equal(t, scope, validation.Request["_mppx_scope"])
	assert.Empty(t, rpc.sentRawTxs)
	assert.Len(t, rpc.callRequests, 1)
	assert.Zero(t, store.putCalls)
	assert.Zero(t, store.putIfAbsentCalls)
	assert.Zero(t, store.deleteCalls)

	receipt, err := payment.BroadcastCredential(ctx, credential)
	require.NoError(t, err)
	require.Len(t, rpc.sentRawTxs, 1)
	assert.Equal(t, mustTxHash(rpc.sentRawTxs[0]), receipt.Reference)
	assert.Len(t, rpc.callRequests, 3)
	assert.Equal(t, 1, store.putIfAbsentCalls)
}

func TestValidationTransferDetailsDistinguishesAttributionFromWildcardMemo(t *testing.T) {
	request := buildRequest(t, false, []tempo.ChargeMode{tempo.ChargeModePull})
	request.MethodDetails.Splits = []tempo.Split{{
		Amount:    "100000",
		Recipient: "0x3c44cdddb6a900fa2b585dd299e03d12fa4293bc",
	}}

	details := validationTransferDetails(request)
	require.Len(t, details, 2)
	assert.Equal(t, true, details[0]["requireAttribution"])
	assert.NotContains(t, details[0], "allowAnyMemo")
	assert.Equal(t, true, details[1]["allowAnyMemo"])
	assert.NotContains(t, details[1], "requireAttribution")
}

func TestIntentValidateTransactionRejectsFailedSimulationWithoutMutation(t *testing.T) {
	ctx := context.Background()
	request := buildRequest(t, false, []tempo.ChargeMode{tempo.ChargeModePull})
	rpc := newMockRPC(request)
	credential, err := newClientMethod(t, rpc, tempo.CredentialTypeTransaction).CreateCredential(ctx, buildChallenge(t, request))
	require.NoError(t, err)
	rpc.callRequests = nil
	rpc.onCall = func(...interface{}) (*temporpc.JSONRPCResponse, error) {
		return temporpc.NewJSONRPCErrorResponse(1, temporpc.InvalidTransactionType, "execution reverted", nil), nil
	}
	store := newRecordingStore()
	intent, err := NewIntent(IntentConfig{RPC: rpc, Store: store})
	require.NoError(t, err)

	_, err = intent.Validate(ctx, credential, request.Map())
	require.ErrorContains(t, err, "transaction preflight failed")
	assert.Empty(t, rpc.sentRawTxs)
	assert.Len(t, rpc.callRequests, 1)
	assert.Zero(t, store.putIfAbsentCalls)
	assert.Zero(t, store.deleteCalls)
}

func TestIntentBroadcastRejectsCredentialExpiringDuringValidation(t *testing.T) {
	ctx := context.Background()
	request := buildRequest(t, false, []tempo.ChargeMode{tempo.ChargeModePull})
	rpc := newMockRPC(request)
	credential, err := newClientMethod(t, rpc, tempo.CredentialTypeTransaction).CreateCredential(ctx, buildChallenge(t, request))
	require.NoError(t, err)
	rpc.callRequests = nil
	rpc.onCall = func(...interface{}) (*temporpc.JSONRPCResponse, error) {
		credential.Challenge.Expires = time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)
		return &temporpc.JSONRPCResponse{Result: rpc.callResult}, nil
	}
	store := newRecordingStore()
	intent, err := NewIntent(IntentConfig{RPC: rpc, Store: store})
	require.NoError(t, err)

	_, err = intent.Broadcast(ctx, credential, request.Map())
	require.ErrorContains(t, err, "expired")
	assert.Len(t, rpc.callRequests, 1)
	assert.Empty(t, rpc.sentRawTxs)
	assert.Zero(t, store.putIfAbsentCalls)
	assert.Zero(t, store.deleteCalls)
}

func TestIntentBroadcastSurfacesSponsoredReplayReleaseFailure(t *testing.T) {
	ctx := context.Background()
	request := buildRequest(t, true, []tempo.ChargeMode{tempo.ChargeModePull})
	rpc := newMockRPC(request)
	credential, err := newClientMethod(t, rpc, tempo.CredentialTypeTransaction).CreateCredential(ctx, buildChallenge(t, request))
	require.NoError(t, err)
	rpc.callRequests = nil
	simulations := 0
	rpc.onCall = func(...interface{}) (*temporpc.JSONRPCResponse, error) {
		simulations++
		if simulations == 2 {
			credential.Challenge.Expires = time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)
		}
		return &temporpc.JSONRPCResponse{Result: rpc.callResult}, nil
	}
	store := newRecordingStore()
	store.deleteErr = errors.New("delete unavailable")
	intent, err := NewIntent(IntentConfig{
		RPC:                rpc,
		Store:              store,
		FeePayerPrivateKey: feePayerKey,
	})
	require.NoError(t, err)

	_, err = intent.Broadcast(ctx, credential, request.Map())
	require.ErrorContains(t, err, "expired")
	require.ErrorContains(t, err, "failed to release replay claim: delete unavailable")
	assert.Len(t, rpc.callRequests, 2)
	assert.Empty(t, rpc.sentRawTxs)
	assert.Equal(t, 1, store.putIfAbsentCalls)
	assert.Equal(t, 1, store.deleteCalls)
}

func TestIntentValidatePushAndProofCredentialsDoesNotConsumeReplayState(t *testing.T) {
	tests := []struct {
		name           string
		credentialType tempo.CredentialType
		prepareRequest func(tempo.ChargeRequest) tempo.ChargeRequest
		wantMode       string
	}{
		{
			name:           "push",
			credentialType: tempo.CredentialTypeHash,
			prepareRequest: func(request tempo.ChargeRequest) tempo.ChargeRequest {
				request.MethodDetails.SupportedModes = []tempo.ChargeMode{tempo.ChargeModePush}
				return request
			},
			wantMode: "push",
		},
		{
			name:           "proof",
			credentialType: tempo.CredentialTypeProof,
			prepareRequest: func(request tempo.ChargeRequest) tempo.ChargeRequest {
				request.Amount = "0"
				return request
			},
			wantMode: "proof",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			request := tt.prepareRequest(buildRequest(t, false, nil))
			rpc := newMockRPC(request)
			credential, err := newClientMethod(t, rpc, tt.credentialType).CreateCredential(ctx, buildChallenge(t, request))
			require.NoError(t, err)
			store := newRecordingStore()
			intent, err := NewIntent(IntentConfig{RPC: rpc, Store: store})
			require.NoError(t, err)

			for range 2 {
				validation, err := intent.Validate(ctx, credential, request.Map())
				require.NoError(t, err)
				assert.Equal(t, tt.wantMode, validation.Details["mode"])
			}
			assert.Zero(t, store.putIfAbsentCalls)

			_, err = intent.Broadcast(ctx, credential, request.Map())
			require.NoError(t, err)
			assert.Equal(t, 1, store.putIfAbsentCalls)

			_, err = intent.Validate(ctx, credential, request.Map())
			require.NoError(t, err)
			_, err = intent.Broadcast(ctx, credential, request.Map())
			require.ErrorContains(t, err, "already used")
			var replayed *mpp.PaymentError
			require.ErrorAs(t, err, &replayed)
			assert.Equal(t, http.StatusForbidden, replayed.Status)
			assert.Equal(t, mpp.ErrorTypeVerificationFailed, replayed.Type)
			assert.Equal(t, 2, store.putIfAbsentCalls)
		})
	}
}

func TestIntentValidateSponsoredCredentialDoesNotInvokeFeePayer(t *testing.T) {
	ctx := context.Background()
	request := buildRequest(t, true, []tempo.ChargeMode{tempo.ChargeModePull})
	feePayerCalls := 0
	feePayerServer := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		feePayerCalls++
	}))
	defer feePayerServer.Close()
	request.MethodDetails.FeePayerURL = feePayerServer.URL
	rpc := newMockRPC(request)
	credential, err := newClientMethod(t, rpc, tempo.CredentialTypeTransaction).CreateCredential(ctx, buildChallenge(t, request))
	require.NoError(t, err)
	rpc.estimateGasCalls = nil
	rpc.callRequests = nil
	store := newRecordingStore()
	intent, err := NewIntent(IntentConfig{RPC: rpc, Store: store})
	require.NoError(t, err)

	validation, err := intent.Validate(ctx, credential, request.Map())
	require.NoError(t, err)
	assert.Equal(t, "pull", validation.Details["mode"])
	assert.Zero(t, feePayerCalls)
	assert.Empty(t, rpc.sentRawTxs)
	assert.Empty(t, rpc.estimateGasCalls)
	assert.Empty(t, rpc.callRequests)
	assert.Zero(t, store.putIfAbsentCalls)
}

func TestIntentBroadcastSponsoredCredentialSimulatesBeforeFeePayer(t *testing.T) {
	ctx := context.Background()
	request := buildRequest(t, true, []tempo.ChargeMode{tempo.ChargeModePull})
	feePayerCalls := 0
	feePayerServer := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		feePayerCalls++
	}))
	defer feePayerServer.Close()
	request.MethodDetails.FeePayerURL = feePayerServer.URL
	rpc := newMockRPC(request)
	credential, err := newClientMethod(t, rpc, tempo.CredentialTypeTransaction).CreateCredential(ctx, buildChallenge(t, request))
	require.NoError(t, err)
	rpc.callRequests = nil
	rpc.onCall = func(...interface{}) (*temporpc.JSONRPCResponse, error) {
		return temporpc.NewJSONRPCErrorResponse(1, temporpc.InvalidTransactionType, "execution reverted", nil), nil
	}
	store := newRecordingStore()
	intent, err := NewIntent(IntentConfig{RPC: rpc, Store: store})
	require.NoError(t, err)

	_, err = intent.Broadcast(ctx, credential, request.Map())
	require.ErrorContains(t, err, "transaction preflight failed")
	assert.Len(t, rpc.callRequests, 1)
	assert.Zero(t, feePayerCalls)
	assert.Empty(t, rpc.sentRawTxs)
	assert.Zero(t, store.putIfAbsentCalls)
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
		coSignedTx.FeeToken = common.HexToAddress(tempo.PathUSDAddress)
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

	intent, err := NewIntent(IntentConfig{Store: tempo.NewMemoryStore(), RPC: rpc})
	if !assert.NoErrorf(t, err,
		"NewIntent() error = %v", err) {
		return
	}

	receipt, err := intent.Verify(ctx, credential, request.Map())
	if !assert.NoErrorf(t, err,
		"Verify() error = %v", err) {
		return
	}
	if !assert.Lenf(t, rpc.sentRawTxs, 1, "broadcasts") {
		return
	}
	if want := mustTxHash(rpc.sentRawTxs[0]); !assert.Equalf(t, want, receipt.Reference,
		"receipt reference = %q, want %q", receipt.Reference, want) {
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

	intent, err := NewIntent(IntentConfig{Store: tempo.NewMemoryStore(), RPC: rpc})
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

func TestChargeHTTPFlow_KeychainFeePayerSigningForm(t *testing.T) {
	cases := []struct {
		name          string
		legacyYParity bool
	}{
		{name: "canonical y parity"},
		{name: "legacy y parity", legacyYParity: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			request := buildRequest(t, true, nil)
			rpc := newMockRPC(request)

			rootSigner, err := temposigner.NewSigner(testPrivateKey)
			if err != nil {
				t.Fatalf("NewSigner(root) error = %v", err)
			}
			feePayerSigner, err := temposigner.NewSigner(feePayerKey)
			if err != nil {
				t.Fatalf("NewSigner(fee payer) error = %v", err)
			}

			rpc.onSend = func(raw string) (string, map[string]any, error) {
				broadcast, err := tempotx.Deserialize(raw)
				if err != nil {
					return "", nil, err
				}
				if broadcast.Signature == nil || broadcast.Signature.Type != "keychain" {
					return "", nil, fmt.Errorf("broadcast signature = %#v, want keychain", broadcast.Signature)
				}
				if broadcast.KeyAuthorization == nil {
					return "", nil, fmt.Errorf("broadcast omitted key authorization")
				}
				// The local fee payer pays gas in its allowed fee token (pathUSD on
				// Moderato), not in the charge currency.
				if broadcast.FeeToken != common.HexToAddress(tempo.PathUSDAddress) {
					return "", nil, fmt.Errorf("broadcast fee token = %s, want %s", broadcast.FeeToken.Hex(), tempo.PathUSDAddress)
				}
				// Nodes hash keychain inner signatures with a 27/28 recovery ID,
				// so the broadcast uses that encoding for either input form.
				gotParity := raw[len(raw)-2:]
				if gotParity != "1b" && gotParity != "1c" {
					return "", nil, fmt.Errorf("broadcast y parity = 0x%s, want 27/28", gotParity)
				}
				feePayer, err := tempotx.VerifyFeePayerSignature(broadcast, rootSigner.Address())
				if err != nil {
					return "", nil, err
				}
				if feePayer != feePayerSigner.Address() {
					return "", nil, fmt.Errorf("broadcast fee payer = %s, want %s", feePayer.Hex(), feePayerSigner.Address().Hex())
				}
				return mustTxHash(raw), buildReceipt(raw, request, rootSigner.Address()), nil
			}

			intent, err := NewIntent(IntentConfig{Store: tempo.NewMemoryStore(), RPC: rpc, FeePayerPrivateKey: feePayerKey})
			if err != nil {
				t.Fatalf("NewIntent() error = %v", err)
			}
			method := newMethod(methodConfig{
				Intent:     intent,
				Currencies: []string{testCurrency},
				Recipient:  testRecipient,
				ChainID:    42431,
				FeePayer:   true,
			})
			payment, err := server.New(method, testRealm, "test-secret-key-at-least-32-bytes")
			if err != nil {
				t.Fatalf("server.New() error = %v", err)
			}
			handler := server.ChargeMiddleware(payment, server.ChargeParams{
				Amount:   "0.50",
				FeePayer: true,
			})(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
			}))
			httpServer := httptest.NewServer(handler)
			defer httpServer.Close()

			challengeResponse, err := httpServer.Client().Get(httpServer.URL)
			if err != nil {
				t.Fatalf("GET challenge error = %v", err)
			}
			_ = challengeResponse.Body.Close()
			if challengeResponse.StatusCode != http.StatusPaymentRequired {
				t.Fatalf("challenge status = %d, want %d", challengeResponse.StatusCode, http.StatusPaymentRequired)
			}
			challenge, err := mpp.ParseChallenge(challengeResponse.Header.Get("WWW-Authenticate"))
			if err != nil {
				t.Fatalf("ParseChallenge() error = %v", err)
			}
			credential := buildKeychainFeePayerCredential(t, rpc, challenge, keychainCredentialOptions{
				legacyYParity: tc.legacyYParity,
			})

			paidRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, httpServer.URL, nil)
			if err != nil {
				t.Fatalf("NewRequestWithContext() error = %v", err)
			}
			paidRequest.Header.Set("Authorization", mpptest.FormatCredential(credential))
			paidResponse, err := httpServer.Client().Do(paidRequest)
			if err != nil {
				t.Fatalf("paid GET error = %v", err)
			}
			_ = paidResponse.Body.Close()
			if paidResponse.StatusCode != http.StatusOK {
				t.Fatalf("paid status = %d, want %d", paidResponse.StatusCode, http.StatusOK)
			}
			if _, err := mpp.ParseReceipt(paidResponse.Header.Get("Payment-Receipt")); err != nil {
				t.Fatalf("ParseReceipt() error = %v", err)
			}
			if len(rpc.sentRawTxs) != 1 {
				t.Fatalf("broadcast count = %d, want 1", len(rpc.sentRawTxs))
			}
		})
	}
}

func TestChargeFlow_KeychainFeePayerSigningFormRejectsTampering(t *testing.T) {
	cases := []struct {
		name      string
		options   keychainCredentialOptions
		wantError string
	}{
		{
			name:      "sender does not match keychain root",
			options:   keychainCredentialOptions{sender: common.HexToAddress("0x1111111111111111111111111111111111111111")},
			wantError: "transaction signature is invalid",
		},
		{
			name:      "sender is zero address",
			options:   keychainCredentialOptions{zeroSender: true},
			wantError: "failed to deserialize transaction payload",
		},
		{
			name:      "fee token is not an allowed fee token",
			options:   keychainCredentialOptions{feeToken: common.HexToAddress("0x20c0000000000000000000000000000000000002")},
			wantError: "fee payer transaction fee token is not supported",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			request := buildRequest(t, true, nil)
			rpc := newMockRPC(request)
			challenge := buildChallenge(t, request)
			credential := buildKeychainFeePayerCredential(t, rpc, challenge, tc.options)
			intent, err := NewIntent(IntentConfig{Store: tempo.NewMemoryStore(), RPC: rpc, FeePayerPrivateKey: feePayerKey})
			if err != nil {
				t.Fatalf("NewIntent() error = %v", err)
			}

			_, err = intent.Verify(context.Background(), credential, request.Map())
			if err == nil || !strings.Contains(err.Error(), tc.wantError) {
				t.Fatalf("Verify() error = %v, want %q", err, tc.wantError)
			}
			if len(rpc.sentRawTxs) != 0 {
				t.Fatalf("broadcast count = %d, want 0", len(rpc.sentRawTxs))
			}
		})
	}
}

type keychainCredentialOptions struct {
	feeToken      common.Address
	legacyYParity bool
	sender        common.Address
	zeroSender    bool
}

func buildKeychainFeePayerCredential(
	t *testing.T,
	rpc tempo.RPCClient,
	challenge *mpp.Challenge,
	options keychainCredentialOptions,
) *mpp.Credential {
	t.Helper()

	credential, err := newClientMethod(t, rpc, tempo.CredentialTypeTransaction).CreateCredential(context.Background(), challenge)
	if err != nil {
		t.Fatalf("CreateCredential() error = %v", err)
	}
	tx, err := tempotx.Deserialize(credential.Payload["signature"].(string))
	if err != nil {
		t.Fatalf("Deserialize(seed) error = %v", err)
	}

	rootSigner, err := temposigner.NewSigner(testPrivateKey)
	if err != nil {
		t.Fatalf("NewSigner(root) error = %v", err)
	}
	accessSigner, err := temposigner.NewSigner(accessKey)
	if err != nil {
		t.Fatalf("NewSigner(access) error = %v", err)
	}
	tx.Signature = nil
	tx.From = common.Address{}
	feeToken := options.feeToken
	if feeToken == (common.Address{}) {
		feeToken = common.HexToAddress(tempo.PathUSDAddress)
	}
	tx.FeeToken = feeToken
	authorization := keychain.NewKeyAuthorization(42431, keychain.SignatureTypeSecp256k1, accessSigner.Address()).
		WithExpiry(uint64(time.Now().Add(5 * time.Minute).Unix()))
	if err := authorization.SignAndAttach(tx, rootSigner); err != nil {
		t.Fatalf("SignAndAttach() error = %v", err)
	}
	if err := keychain.SignWithAccessKey(tx, accessSigner, rootSigner.Address()); err != nil {
		t.Fatalf("SignWithAccessKey() error = %v", err)
	}
	if options.legacyYParity {
		tx.Signature.Raw[keychain.KeychainSignatureLength-1] += 27
	}

	sender := options.sender
	if sender == (common.Address{}) && !options.zeroSender {
		sender = rootSigner.Address()
	}
	serialized, err := tempotx.Serialize(tx, &tempotx.SerializeOptions{
		Format: tempotx.FormatFeePayer,
		Sender: sender,
	})
	if err != nil {
		t.Fatalf("Serialize(FormatFeePayer) error = %v", err)
	}
	if !strings.HasPrefix(serialized, "0x78") {
		t.Fatalf("serialized transaction prefix = %.4s, want 0x78", serialized)
	}
	credential.Payload["signature"] = serialized
	return credential
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

	intent, err := NewIntent(IntentConfig{Store: tempo.NewMemoryStore(), RPC: rpc})
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

	intent, err := NewIntent(IntentConfig{Store: tempo.NewMemoryStore(), RPC: rpc})
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
		return mustTxHash(raw), receipt, nil
	}
	clientMethod := newClientMethod(t, rpc, tempo.CredentialTypeHash)
	challenge := buildChallenge(t, request)

	credential, err := clientMethod.CreateCredential(ctx, challenge)
	if !assert.NoErrorf(t, err,
		"CreateCredential() error = %v", err) {
		return
	}

	intent, err := NewIntent(IntentConfig{Store: tempo.NewMemoryStore(), RPC: rpc})
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
		return mustTxHash(raw), receipt, nil
	}
	clientMethod := newClientMethod(t, rpc, tempo.CredentialTypeHash)
	challenge := buildChallenge(t, request)

	credential, err := clientMethod.CreateCredential(ctx, challenge)
	if !assert.NoErrorf(t, err,
		"CreateCredential() error = %v", err) {
		return
	}

	intent, err := NewIntent(IntentConfig{Store: tempo.NewMemoryStore(), RPC: rpc})
	if !assert.NoErrorf(t, err,
		"NewIntent() error = %v", err) {
		return
	}

	if _, err := intent.Verify(ctx, credential, request.Map()); err != nil {
		assert.Failf(t, "", "Verify() error = %v", err)
		return
	}
}

func TestMatchTransfers_RejectsConflictingSplitAttributionMemos(t *testing.T) {
	t.Parallel()

	const (
		challengeID    = "challenge-1"
		splitRecipient = "0x3c44cdddb6a900fa2b585dd299e03d12fa4293bc"
	)
	bound := tempo.EncodeAttribution(testRealm, "", challengeID)
	memos := map[string]string{
		"bound":           bound,
		"other challenge": tempo.EncodeAttribution(testRealm, "", "other-challenge"),
		"other realm":     tempo.EncodeAttribution("other.example.com", "", challengeID),
		"other client":    tempo.EncodeAttribution(testRealm, "another-client", challengeID),
		"uppercase":       "0x" + strings.ToUpper(strings.TrimPrefix(bound, "0x")),
		"custom":          "0x" + strings.Repeat("ab", 32),
		"plain":           "",
	}
	expected := expectedTransfers(tempo.ChargeRequest{
		Amount:    "1000000",
		Recipient: testRecipient,
		MethodDetails: tempo.MethodDetails{
			Splits: []tempo.Split{{Amount: "200000", Recipient: splitRecipient}},
		},
	})

	tests := []struct {
		primary, split string
		accepted       bool
	}{
		{"bound", "plain", true},
		{"bound", "bound", true},
		{"bound", "other client", true},
		{"bound", "uppercase", true},
		{"bound", "custom", true},
		{"bound", "other challenge", false},
		{"other challenge", "bound", false},
		{"bound", "other realm", false},
		{"other realm", "bound", false},
		{"other challenge", "plain", false},
		{"other realm", "plain", false},
		{"plain", "plain", false},
		{"custom", "plain", false},
	}
	for _, tt := range tests {
		t.Run(tt.primary+"/"+tt.split, func(t *testing.T) {
			t.Parallel()
			transfer := func(amount, recipient, memo string) decodedTransfer {
				return decodedTransfer{amount: amount, recipient: recipient, hasMemo: memo != "", memo: memo}
			}
			actual := []decodedTransfer{
				transfer("800000", testRecipient, memos[tt.primary]),
				transfer("200000", splitRecipient, memos[tt.split]),
			}
			assert.Equal(t, tt.accepted, matchTransfers(actual, expected, testRealm, challengeID))
		})
	}
}

func TestMatchTransfers_AcceptsConfiguredAttributionShapedSplitMemo(t *testing.T) {
	t.Parallel()

	const (
		challengeID    = "challenge-1"
		splitRecipient = "0x3c44cdddb6a900fa2b585dd299e03d12fa4293bc"
	)
	// A server-configured split memo is static, so it can never carry this
	// challenge's nonce even when it has the attribution layout.
	configured := tempo.EncodeAttribution("partner.example.com", "", "partner-challenge")
	expected := expectedTransfers(tempo.ChargeRequest{
		Amount:    "1000000",
		Recipient: testRecipient,
		MethodDetails: tempo.MethodDetails{
			Splits: []tempo.Split{{Amount: "200000", Recipient: splitRecipient, Memo: configured}},
		},
	})
	actual := []decodedTransfer{
		{amount: "800000", recipient: testRecipient, hasMemo: true, memo: tempo.EncodeAttribution(testRealm, "", challengeID)},
		{amount: "200000", recipient: splitRecipient, hasMemo: true, memo: configured},
	}
	assert.True(t, matchTransfers(actual, expected, testRealm, challengeID))

	actual[1].memo = tempo.EncodeAttribution("partner.example.com", "", "other-challenge")
	assert.False(t, matchTransfers(actual, expected, testRealm, challengeID))
}

func TestChargeFlow_HashCredentialRejectsReplayWithAlternateSpelling(t *testing.T) {
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
	// Nodes look receipts up by value, so every spelling of the hash resolves
	// to the same receipt; the replay key must not depend on the spelling.
	rpc.onGetReceipt = func(hash string) (*temporpc.JSONRPCResponse, error) {
		normalized := strings.ToLower(strings.TrimPrefix(strings.TrimPrefix(hash, "0x"), "0X"))
		return &temporpc.JSONRPCResponse{Result: rpc.receipts["0x"+normalized]}, nil
	}
	challenge := buildChallenge(t, request)
	credential, err := newClientMethod(t, rpc, tempo.CredentialTypeHash).CreateCredential(ctx, challenge)
	if !assert.NoErrorf(t, err,
		"CreateCredential() error = %v", err) {
		return
	}

	intent, err := NewIntent(IntentConfig{Store: tempo.NewMemoryStore(), RPC: rpc})
	if !assert.NoErrorf(t, err,
		"NewIntent() error = %v", err) {
		return
	}
	if _, err := intent.Verify(ctx, credential, request.Map()); err != nil {
		assert.Failf(t, "", "Verify() error = %v", err)
		return
	}

	paidHash := credential.Payload["hash"].(string)
	for name, spelling := range map[string]string{
		"unprefixed": strings.TrimPrefix(paidHash, "0x"),
		"uppercase":  "0X" + strings.ToUpper(strings.TrimPrefix(paidHash, "0x")),
	} {
		replay := *credential
		replay.Payload = map[string]any{"type": "hash", "hash": spelling}
		_, err := intent.Verify(ctx, &replay, request.Map())
		assert.ErrorContainsf(t, err, "already used", "%s replay should be rejected", name)
	}
}

func TestChargeFlow_HashCredentialAcceptsAttributionMemo(t *testing.T) {
	ctx := context.Background()
	method := newMethod(methodConfig{Intent: testStoredIntent(),
		Currencies: []string{testCurrency},
		Recipient:  testRecipient,
		Decimals:   6,
		ChainID:    42431,
	})
	requestMap, err := method.BuildChargeRequest(server.ChargeParams{
		Amount:         "0.50",
		SupportedModes: []tempo.ChargeMode{tempo.ChargeModePush},
	})
	if !assert.NoErrorf(t, err,
		"BuildChargeRequest() error = %v", err) {
		return
	}
	request, err := tempo.ParseChargeRequest(requestMap)
	if !assert.NoErrorf(t, err,
		"ParseChargeRequest() error = %v", err) {
		return
	}
	if !assert.Equal(t, []tempo.ChargeMode{tempo.ChargeModePush}, request.MethodDetails.SupportedModes) {
		return
	}

	rpc := newMockRPC(request)
	challenge := buildChallenge(t, request)
	credential, err := newClientMethod(t, rpc, tempo.CredentialTypeHash).CreateCredential(ctx, challenge)
	if !assert.NoErrorf(t, err,
		"CreateCredential() error = %v", err) {
		return
	}

	intent, err := NewIntent(IntentConfig{Store: tempo.NewMemoryStore(), RPC: rpc})
	if !assert.NoErrorf(t, err,
		"NewIntent() error = %v", err) {
		return
	}

	receipt, err := intent.Verify(ctx, credential, request.Map())
	if !assert.NoErrorf(t, err, "Verify() error = %v", err) ||
		!assert.Equal(t, credential.Payload["hash"], receipt.Reference) {
		return
	}

	if _, err := intent.Verify(ctx, credential, request.Map()); err == nil || !strings.Contains(err.Error(), "already used") {
		assert.Failf(t, "", "Verify() error = %v, want hash replay rejection", err)
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

	intent, err := NewIntent(IntentConfig{Store: tempo.NewMemoryStore(), RPC: rpc})
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

	intent, err := NewIntent(IntentConfig{Store: tempo.NewMemoryStore(), RPC: rpc})
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

	intent, err := NewIntent(IntentConfig{Store: tempo.NewMemoryStore(), RPC: rpc})
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
		if !assert.ErrorContains(t, err, "transaction hash already used") {
			return
		}
	}
	if !assert.Lenf(t, rpc.sentRawTxs, 1,
		"expected no second broadcast, got %d", len(rpc.sentRawTxs)) {
		return
	}

}

func TestChargeFlow_TransactionCredentialRetryRecoversAfterReceiptFailure(t *testing.T) {
	ctx := context.Background()
	request := buildRequest(t, false, nil)
	rpc := newMockRPC(request)
	failReceiptFetch := true
	rpc.onGetReceipt = func(hash string) (*temporpc.JSONRPCResponse, error) {
		// Fail the first receipt read after the broadcast.
		if failReceiptFetch && len(rpc.sentRawTxs) > 0 {
			failReceiptFetch = false
			return nil, fmt.Errorf("temporary receipt rpc failure")
		}
		return &temporpc.JSONRPCResponse{Result: rpc.receipts[hash]}, nil
	}
	credential, err := newClientMethod(t, rpc, tempo.CredentialTypeTransaction).CreateCredential(ctx, buildChallenge(t, request))
	require.NoError(t, err)
	intent, err := NewIntent(IntentConfig{Store: tempo.NewMemoryStore(), RPC: rpc})
	require.NoError(t, err)

	// The node accepted the transaction, but its receipt could not be read:
	// the outcome is unknown and the credential must stay usable.
	_, err = intent.Verify(ctx, credential, request.Map())
	require.ErrorContains(t, err, "outcome is unknown")
	require.ErrorContains(t, err, "failed to fetch transaction receipt")
	require.Len(t, rpc.sentRawTxs, 1)

	// The transaction was included and spent the payer's balance, so
	// simulating it again fails. A retry with the same credential finds its
	// receipt first and settles from it without simulating or re-broadcasting.
	rpc.onCall = drainedBalanceCall
	receipt, err := intent.Verify(ctx, credential, request.Map())
	require.NoError(t, err)
	require.Len(t, rpc.sentRawTxs, 1)
	assert.Equal(t, mustTxHash(rpc.sentRawTxs[0]), receipt.Reference)

	// Only the successful settlement consumes the credential.
	_, err = intent.Verify(ctx, credential, request.Map())
	require.ErrorContains(t, err, "transaction hash already used")
	assert.Len(t, rpc.sentRawTxs, 1)
}

// drainedBalanceCall fails every simulation, like a node after the payment
// spent the payer's balance and nonce.
func drainedBalanceCall(...interface{}) (*temporpc.JSONRPCResponse, error) {
	return temporpc.NewJSONRPCErrorResponse(1, temporpc.InvalidTransactionType, "execution reverted: insufficient balance", nil), nil
}

func TestChargeFlow_TransactionCredentialRetryRebroadcastsWhenNotIncluded(t *testing.T) {
	ctx := context.Background()
	request := buildRequest(t, false, nil)
	rpc := newMockRPC(request)
	defaultSend := rpc.onSend
	dropped := true
	rpc.onSend = func(raw string) (string, map[string]any, error) {
		hash, receipt, err := defaultSend(raw)
		if dropped {
			// The node accepted the transaction but never included it.
			dropped = false
			return hash, nil, err
		}
		return hash, receipt, err
	}
	rpc.onGetReceipt = func(hash string) (*temporpc.JSONRPCResponse, error) {
		if len(rpc.sentRawTxs) == 1 {
			return nil, fmt.Errorf("temporary receipt rpc failure")
		}
		return &temporpc.JSONRPCResponse{Result: rpc.receipts[hash]}, nil
	}
	credential, err := newClientMethod(t, rpc, tempo.CredentialTypeTransaction).CreateCredential(ctx, buildChallenge(t, request))
	require.NoError(t, err)
	intent, err := NewIntent(IntentConfig{Store: tempo.NewMemoryStore(), RPC: rpc})
	require.NoError(t, err)

	_, err = intent.Verify(ctx, credential, request.Map())
	require.ErrorContains(t, err, "outcome is unknown")

	// No receipt yet: the retry simulates and re-broadcasts the identical
	// signed transaction.
	receipt, err := intent.Verify(ctx, credential, request.Map())
	require.NoError(t, err)
	require.Len(t, rpc.sentRawTxs, 2)
	assert.Equal(t, rpc.sentRawTxs[0], rpc.sentRawTxs[1])
	assert.Equal(t, mustTxHash(rpc.sentRawTxs[1]), receipt.Reference)
}

func TestChargeFlow_TransactionCredentialReceiptWaitOutlivesRequest(t *testing.T) {
	request := buildRequest(t, false, nil)
	rpc := newMockRPC(request)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	receiptLookups := 0
	rpc.onGetReceipt = func(hash string) (*temporpc.JSONRPCResponse, error) {
		receiptLookups++
		if receiptLookups == 1 {
			// The client disconnects while the receipt is pending.
			cancel()
			return &temporpc.JSONRPCResponse{}, nil
		}
		return &temporpc.JSONRPCResponse{Result: rpc.receipts[hash]}, nil
	}
	credential, err := newClientMethod(t, rpc, tempo.CredentialTypeTransaction).CreateCredential(context.Background(), buildChallenge(t, request))
	require.NoError(t, err)
	intent, err := NewIntent(IntentConfig{Store: tempo.NewMemoryStore(), RPC: rpc})
	require.NoError(t, err)

	receipt, err := intent.Verify(ctx, credential, request.Map())
	require.NoError(t, err)
	require.Len(t, rpc.sentRawTxs, 1)
	assert.Equal(t, mustTxHash(rpc.sentRawTxs[0]), receipt.Reference)
	assert.Equal(t, 2, receiptLookups)
}

func TestChargeFlow_TransactionRejectsMismatchedBroadcastHash(t *testing.T) {
	for _, feePayer := range []bool{false, true} {
		t.Run(fmt.Sprintf("feePayer=%t", feePayer), func(t *testing.T) {
			ctx := context.Background()
			request := buildRequest(t, feePayer, nil)
			rpc := newMockRPC(request)
			defaultSend := rpc.onSend
			rpc.onSend = func(raw string) (string, map[string]any, error) {
				_, receipt, err := defaultSend(raw)
				return testReceiptHash, receipt, err
			}
			credential, err := newClientMethod(t, rpc, tempo.CredentialTypeTransaction).CreateCredential(ctx, buildChallenge(t, request))
			require.NoError(t, err)
			config := IntentConfig{Store: tempo.NewMemoryStore(), RPC: rpc}
			if feePayer {
				config.FeePayerPrivateKey = feePayerKey
			}
			intent, err := NewIntent(config)
			require.NoError(t, err)

			_, err = intent.Verify(ctx, credential, request.Map())
			require.ErrorContains(t, err, "broadcast transaction hash does not match the signed transaction")
			require.Len(t, rpc.sentRawTxs, 1)

			// The claim is released, so a retry against an honest node settles.
			rpc.onSend = defaultSend
			_, err = intent.Verify(ctx, credential, request.Map())
			require.NoError(t, err)
			assert.Len(t, rpc.sentRawTxs, 2)
		})
	}
}

func TestChargeFlow_FeePayerRetryRecoversAfterReceiptFailure(t *testing.T) {
	ctx := context.Background()
	request := buildRequest(t, true, nil)
	rpc := newMockRPC(request)
	failReceiptFetch := true
	rpc.onGetReceipt = func(hash string) (*temporpc.JSONRPCResponse, error) {
		// Fail the first receipt read after the broadcast.
		if failReceiptFetch && len(rpc.sentRawTxs) > 0 {
			failReceiptFetch = false
			return nil, fmt.Errorf("temporary receipt rpc failure")
		}
		return &temporpc.JSONRPCResponse{Result: rpc.receipts[hash]}, nil
	}
	credential, err := newClientMethod(t, rpc, tempo.CredentialTypeTransaction).CreateCredential(ctx, buildChallenge(t, request))
	require.NoError(t, err)
	intent, err := NewIntent(IntentConfig{Store: tempo.NewMemoryStore(), RPC: rpc, FeePayerPrivateKey: feePayerKey})
	require.NoError(t, err)

	_, err = intent.Verify(ctx, credential, request.Map())
	require.ErrorContains(t, err, "outcome is unknown")
	require.Len(t, rpc.sentRawTxs, 1)

	// The included transaction spent the payer's balance. The local fee
	// payer re-signs the retry identically, so its receipt is found before
	// any simulation and the retry settles without re-broadcasting.
	rpc.onCall = drainedBalanceCall
	receipt, err := intent.Verify(ctx, credential, request.Map())
	require.NoError(t, err)
	require.Len(t, rpc.sentRawTxs, 1)
	assert.Equal(t, mustTxHash(rpc.sentRawTxs[0]), receipt.Reference)

	_, err = intent.Verify(ctx, credential, request.Map())
	require.ErrorContains(t, err, "already used")
	assert.Len(t, rpc.sentRawTxs, 1)
}

// A sponsored payment that landed is recovered by a retry that arrives after
// its fee payer deadline, while a transaction that never landed still fails it.
func TestChargeFlow_FeePayerRetryRecoversAfterDeadline(t *testing.T) {
	ctx := context.Background()
	request := buildRequest(t, true, nil)
	rpc := newMockRPC(request)
	failReceiptFetch := true
	rpc.onGetReceipt = func(hash string) (*temporpc.JSONRPCResponse, error) {
		if failReceiptFetch && len(rpc.sentRawTxs) > 0 {
			failReceiptFetch = false
			return nil, fmt.Errorf("temporary receipt rpc failure")
		}
		return &temporpc.JSONRPCResponse{Result: rpc.receipts[hash]}, nil
	}
	credential, err := newClientMethod(t, rpc, tempo.CredentialTypeTransaction).CreateCredential(ctx, buildChallenge(t, request))
	require.NoError(t, err)
	intent, err := NewIntent(IntentConfig{Store: tempo.NewMemoryStore(), RPC: rpc, FeePayerPrivateKey: feePayerKey})
	require.NoError(t, err)

	_, err = intent.Verify(ctx, credential, request.Map())
	require.ErrorContains(t, err, "outcome is unknown")
	require.Len(t, rpc.sentRawTxs, 1)

	// Less than 15 seconds of the 60-second window are left.
	intent.now = func() time.Time { return time.Now().Add(50 * time.Second) }
	rpc.onCall = drainedBalanceCall
	receipt, err := intent.Verify(ctx, credential, request.Map())
	require.NoError(t, err)
	require.Len(t, rpc.sentRawTxs, 1)
	assert.Equal(t, mustTxHash(rpc.sentRawTxs[0]), receipt.Reference)

	// The same late credential is rejected when its transaction never landed.
	unsentRPC := newMockRPC(request)
	unsent, err := newClientMethod(t, unsentRPC, tempo.CredentialTypeTransaction).CreateCredential(ctx, buildChallenge(t, request))
	require.NoError(t, err)
	late, err := NewIntent(IntentConfig{Store: tempo.NewMemoryStore(), RPC: unsentRPC, FeePayerPrivateKey: feePayerKey})
	require.NoError(t, err)
	late.now = intent.now
	_, err = late.Verify(ctx, unsent, request.Map())
	require.ErrorContains(t, err, "remaining validity")
	assert.Empty(t, unsentRPC.sentRawTxs)
}

// failNthPutIfAbsentStore fails its failOn-th PutIfAbsent call.
type failNthPutIfAbsentStore struct {
	tempo.Store
	failOn int
	calls  int
}

func (s *failNthPutIfAbsentStore) PutIfAbsent(ctx context.Context, key, value string) (bool, error) {
	s.calls++
	if s.calls == s.failOn {
		return false, errors.New("store unavailable")
	}
	return s.Store.PutIfAbsent(ctx, key, value)
}

// A sponsored payment whose final replay claim fails after the transaction is
// included must recover its receipt on retry instead of being rejected as used.
func TestChargeFlow_FeePayerRetryAfterFinalClaimStoreFailure(t *testing.T) {
	ctx := context.Background()
	request := buildRequest(t, true, nil)
	rpc := newMockRPC(request)
	credential, err := newClientMethod(t, rpc, tempo.CredentialTypeTransaction).CreateCredential(ctx, buildChallenge(t, request))
	require.NoError(t, err)

	// Call 1 claims the sponsored transaction, call 2 the included transaction hash.
	store := &failNthPutIfAbsentStore{Store: tempo.NewMemoryStore(), failOn: 2}
	intent, err := NewIntent(IntentConfig{Store: store, RPC: rpc, FeePayerPrivateKey: feePayerKey})
	require.NoError(t, err)

	_, err = intent.Verify(ctx, credential, request.Map())
	require.ErrorContains(t, err, "store unavailable")
	require.Len(t, rpc.sentRawTxs, 1)

	// The transaction is on chain and the payer's balance is spent.
	rpc.onCall = drainedBalanceCall
	receipt, err := intent.Verify(ctx, credential, request.Map())
	require.NoError(t, err)
	require.Len(t, rpc.sentRawTxs, 1)
	assert.Equal(t, mustTxHash(rpc.sentRawTxs[0]), receipt.Reference)

	// The payment is now consumed.
	_, err = intent.Verify(ctx, credential, request.Map())
	require.Error(t, err)
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

	intent, err := NewIntent(IntentConfig{Store: tempo.NewMemoryStore(), RPC: rpc})
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
		return mustTxHash(raw), buildReceipt(raw, request, sender), nil
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

	intent, err := NewIntent(IntentConfig{Store: tempo.NewMemoryStore(), RPC: rpc, FeePayerPrivateKey: feePayerKey})
	if !assert.NoErrorf(t, err,
		"NewIntent() error = %v", err) {
		return
	}

	if _, err := intent.Verify(ctx, credential, request.Map()); err == nil || !strings.Contains(err.Error(), "sponsor policy") {
		assert.Failf(t, "", "Verify() error = %v, want sponsor policy rejection", err)
		return
	}
}

func TestChargeFlow_FeePayerTransactionUsesEnvelopeOnceAfterRevert(t *testing.T) {
	ctx := context.Background()
	request := buildRequest(t, true, nil)
	rpc := newMockRPC(request)
	rpc.onSend = func(raw string) (string, map[string]any, error) {
		return mustTxHash(raw), map[string]any{"status": "0x0", "logs": []any{}}, nil
	}
	clientMethod := newClientMethod(t, rpc, tempo.CredentialTypeTransaction)
	challenge := buildChallenge(t, request)

	credential, err := clientMethod.CreateCredential(ctx, challenge)
	if !assert.NoErrorf(t, err,
		"CreateCredential() error = %v", err) {
		return
	}

	intent, err := NewIntent(IntentConfig{Store: tempo.NewMemoryStore(), RPC: rpc, FeePayerPrivateKey: feePayerKey})
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
		if !assert.Falsef(t, err == nil || !strings.Contains(err.Error(), "sponsored transaction already used"),
			"second Verify() error = %v, want reused envelope rejection", err) {
			return
		}
	}
	if !assert.Lenf(t, rpc.sentRawTxs, 1,
		"expected no second broadcast, got %d", len(rpc.sentRawTxs)) {
		return
	}

}

func TestChargeFlow_FeePayerDistinctPayersShareChallenge(t *testing.T) {
	ctx := context.Background()
	request := buildRequest(t, true, nil)
	rpc := newMockRPC(request)
	// Give every broadcast its own hash so the post-receipt hash reservation
	// does not conflate the two payments.
	rpc.onSend = func(raw string) (string, map[string]any, error) {
		tx, err := tempotx.Deserialize(raw)
		if err != nil {
			return "", nil, err
		}
		sender, err := tempotx.VerifySignature(tx)
		if err != nil {
			return "", nil, err
		}
		hash := crypto.Keccak256Hash(common.FromHex(raw)).Hex()
		return hash, buildReceipt(raw, request, sender), nil
	}
	challenge := buildChallenge(t, request)

	firstPayer := newClientMethod(t, rpc, tempo.CredentialTypeTransaction)
	firstCredential, err := firstPayer.CreateCredential(ctx, challenge)
	require.NoError(t, err)

	secondPayer, err := chargeclient.New(chargeclient.Config{
		PaymentPolicy:  func(context.Context, tempo.ChargeRequest) error { return nil },
		PrivateKey:     secondPayerPrivateKey,
		RPC:            rpc,
		ChainID:        42431,
		CredentialType: tempo.CredentialTypeTransaction,
	})
	require.NoError(t, err)
	secondCredential, err := secondPayer.CreateCredential(ctx, challenge)
	require.NoError(t, err)
	require.NotEqual(t, firstCredential, secondCredential)

	store := newRecordingStore()
	intent, err := NewIntent(IntentConfig{RPC: rpc, Store: store, FeePayerPrivateKey: feePayerKey})
	require.NoError(t, err)

	// Two payers answering the same challenge each get sponsored.
	_, err = intent.Verify(ctx, firstCredential, request.Map())
	require.NoError(t, err)
	_, err = intent.Verify(ctx, secondCredential, request.Map())
	require.NoError(t, err)
	require.Len(t, rpc.sentRawTxs, 2)

	// Resubmitting an envelope is still rejected before any co-signing.
	_, err = intent.Verify(ctx, firstCredential, request.Map())
	require.ErrorContains(t, err, "sponsored transaction already used")
	require.Len(t, rpc.sentRawTxs, 2)
}

func TestChargeFlow_FeePayerCanonicalReplay(t *testing.T) {
	for _, firstFeePayerForm := range []bool{false, true} {
		t.Run(fmt.Sprint(firstFeePayerForm), func(t *testing.T) {
			ctx := context.Background()
			request := buildRequest(t, true, nil)
			rpc := newMockRPC(request)
			credential, err := newClientMethod(t, rpc, tempo.CredentialTypeTransaction).CreateCredential(ctx, buildChallenge(t, request))
			require.NoError(t, err)
			normal := credential.Payload["signature"].(string)
			tx, err := tempotx.Deserialize(normal)
			require.NoError(t, err)
			sender, err := tempotx.VerifySignature(tx)
			require.NoError(t, err)
			alternate, err := tempotx.Serialize(tx, &tempotx.SerializeOptions{Format: tempotx.FormatFeePayer, Sender: sender})
			require.NoError(t, err)
			forms := []string{normal, alternate}
			if firstFeePayerForm {
				forms[0], forms[1] = forms[1], forms[0]
			}
			intent, err := NewIntent(IntentConfig{RPC: rpc, Store: tempo.NewMemoryStore(), FeePayerPrivateKey: feePayerKey})
			require.NoError(t, err)
			credential.Payload["signature"] = forms[0]
			_, err = intent.Verify(ctx, credential, request.Map())
			require.NoError(t, err)
			credential.Payload["signature"] = forms[1]
			_, err = intent.Verify(ctx, credential, request.Map())
			require.ErrorContains(t, err, "sponsored transaction already used")
			require.Len(t, rpc.sentRawTxs, 1)
		})
	}
}

func TestChargeFlow_FeePayerHonorsLegacyReservation(t *testing.T) {
	ctx := context.Background()
	request := buildRequest(t, true, nil)
	rpc := newMockRPC(request)
	challenge := buildChallenge(t, request)
	credential, err := newClientMethod(t, rpc, tempo.CredentialTypeTransaction).CreateCredential(ctx, challenge)
	require.NoError(t, err)
	store := tempo.NewMemoryStore()
	require.NoError(t, store.Put(ctx, legacySponsoredChallengeStoreKey(challenge.ID), challenge.ID))
	intent, err := NewIntent(IntentConfig{RPC: rpc, Store: store, FeePayerPrivateKey: feePayerKey})
	require.NoError(t, err)
	_, err = intent.Verify(ctx, credential, request.Map())
	require.ErrorContains(t, err, "fee payer challenge already used")
	require.Empty(t, rpc.sentRawTxs)
}

func TestChargeFlow_TransactionReplayAfterStateChangeIsTerminal(t *testing.T) {
	for _, tt := range []struct {
		name     string
		feePayer bool
		reason   string
	}{
		{name: "unsponsored", reason: "transaction hash already used"},
		{name: "sponsored", feePayer: true, reason: "sponsored transaction already used"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			request := buildRequest(t, tt.feePayer, []tempo.ChargeMode{tempo.ChargeModePull})
			rpc := newMockRPC(request)
			credential, err := newClientMethod(t, rpc, tempo.CredentialTypeTransaction).CreateCredential(ctx, buildChallenge(t, request))
			require.NoError(t, err)
			config := IntentConfig{RPC: rpc, Store: tempo.NewMemoryStore()}
			if tt.feePayer {
				config.FeePayerPrivateKey = feePayerKey
			}
			intent, err := NewIntent(config)
			require.NoError(t, err)
			_, err = intent.Verify(ctx, credential, request.Map())
			require.NoError(t, err)

			// The first payment spent the payer's balance, so a replayed
			// simulation would now revert.
			rpc.callRequests = nil
			rpc.onCall = func(...interface{}) (*temporpc.JSONRPCResponse, error) {
				return temporpc.NewJSONRPCErrorResponse(1, temporpc.InvalidTransactionType, "execution reverted", nil), nil
			}
			_, err = intent.Verify(ctx, credential, request.Map())
			var replayed *mpp.PaymentError
			require.ErrorAs(t, err, &replayed)
			assert.Equal(t, http.StatusForbidden, replayed.Status)
			assert.Equal(t, tt.reason, replayed.Detail)
			assert.Empty(t, rpc.callRequests)
			assert.Len(t, rpc.sentRawTxs, 1)
		})
	}
}

func TestChargeFlow_FeePayerTransactionFailsPreflightBeforeBroadcast(t *testing.T) {
	ctx := context.Background()
	request := buildRequest(t, true, nil)
	rpc := newMockRPC(request)
	rpc.onCall = func(params ...interface{}) (*temporpc.JSONRPCResponse, error) {
		callObject, ok := params[0].(map[string]any)
		if !assert.Truef(t, ok,
			"eth_call params[0] type = %T, want map[string]any", params[0]) {
			return *new(*temporpc.JSONRPCResponse), *new(error)
		}

		if _, finalEnvelope := callObject["feePayer"]; !finalEnvelope {
			assert.Equal(t, map[string]any{
				"calls": callObject["calls"],
				"from":  callObject["from"],
			}, callObject)
			return &temporpc.JSONRPCResponse{Result: rpc.callResult}, nil
		}
		if !assert.NotEqual(t, "", callObject["from"],
			"eth_call object missing from") {
			return *new(*temporpc.JSONRPCResponse), *new(error)
		}
		wantFeeToken := common.HexToAddress(tempo.PathUSDAddress).Hex()
		if !assert.Equalf(t, wantFeeToken, callObject["feeToken"],
			"eth_call feeToken = %v, want %s", callObject["feeToken"], wantFeeToken) {
			return *new(*temporpc.JSONRPCResponse), *new(error)
		}

		calls, ok := callObject["calls"].([]map[string]any)
		if !assert.Falsef(t, !ok || len(calls) == 0,
			"eth_call calls = %#v, want non-empty call batch", callObject["calls"]) {
			return *new(*temporpc.JSONRPCResponse), *new(error)
		}
		{

			_, ok := callObject["nonceKey"]
			if !assert.True(t, ok,
				"eth_call object missing nonceKey") {
				return *new(*temporpc.JSONRPCResponse), *new(error)
			}
		}
		{

			_, ok := callObject["validBefore"]
			if !assert.True(t, ok,
				"eth_call object missing validBefore") {
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

	store := newRecordingStore()
	intent, err := NewIntent(IntentConfig{RPC: rpc, Store: store, FeePayerPrivateKey: feePayerKey})
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
	if !assert.Lenf(t, rpc.callRequests, 2,
		"expected sender and final-envelope simulations, got %d", len(rpc.callRequests)) {
		return
	}
	assert.Equal(t, 1, store.putIfAbsentCalls)
	assert.Equal(t, 1, store.deleteCalls)

}

func TestChargeFlow_LocalFeePayerSponsorsCurrencyWithoutFeePolicy(t *testing.T) {
	ctx := context.Background()
	request, err := tempo.NormalizeChargeRequest(tempo.ChargeRequestParams{
		Amount:    "0.50",
		Currency:  "0x20c0000000000000000000000000000000000002",
		Recipient: testRecipient,
		Decimals:  6,
		ChainID:   42431,
		FeePayer:  true,
	})
	require.NoError(t, err)

	rpc := newMockRPC(request)
	credential, err := newClientMethod(t, rpc, tempo.CredentialTypeTransaction).CreateCredential(ctx, buildChallenge(t, request))
	require.NoError(t, err)
	intent, err := NewIntent(IntentConfig{Store: tempo.NewMemoryStore(), RPC: rpc, FeePayerPrivateKey: feePayerKey})
	require.NoError(t, err)

	// The charge currency has no fee-payer policy; the local fee payer pays
	// gas in its allowed fee token instead.
	_, err = intent.Verify(ctx, credential, request.Map())
	require.NoError(t, err)
	require.Len(t, rpc.sentRawTxs, 1)
	broadcast, err := tempotx.Deserialize(rpc.sentRawTxs[0])
	require.NoError(t, err)
	assert.Equal(t, common.HexToAddress(tempo.PathUSDAddress), broadcast.FeeToken)
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

	intent, err := NewIntent(IntentConfig{Store: tempo.NewMemoryStore(),
		RPC:                rpc,
		FeePayerPrivateKey: feePayerKey,
		AllowedFeeTokens:   []string{request.Currency},
		FeePayerPolicy: &FeePayerPolicy{
			MaxFeePerGas:         big.NewInt(10),
			MaxPriorityFeePerGas: big.NewInt(10),
			MaxTotalFee:          big.NewInt(1_000_000),
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

func TestChargeFlow_TransactionCredentialRecoversConfirmedChargeAfterSubmissionError(t *testing.T) {
	for _, feePayer := range []bool{false, true} {
		t.Run(fmt.Sprintf("feePayer=%t", feePayer), func(t *testing.T) {
			ctx := context.Background()
			request := buildRequest(t, feePayer, nil)
			rpc := newMockRPC(request)
			var broadcastHash string
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
				// The node included the transaction, but its RPC reply failed.
				broadcastHash = hash.Hex()
				rpc.receipts[broadcastHash] = buildReceipt(raw, request, sender)
				return "", nil, fmt.Errorf("connection reset")
			}
			credential, err := newClientMethod(t, rpc, tempo.CredentialTypeTransaction).CreateCredential(ctx, buildChallenge(t, request))
			require.NoError(t, err)
			intent, err := NewIntent(IntentConfig{Store: tempo.NewMemoryStore(), RPC: rpc, FeePayerPrivateKey: feePayerKey})
			require.NoError(t, err)

			receipt, err := intent.Verify(ctx, credential, request.Map())
			require.NoError(t, err)
			assert.Equal(t, broadcastHash, receipt.Reference)
			require.Len(t, rpc.sentRawTxs, 1)

			_, err = intent.Verify(ctx, credential, request.Map())
			require.Error(t, err)
			assert.Len(t, rpc.sentRawTxs, 1, "replay must not rebroadcast")
		})
	}
}

func TestChargeFlow_TransactionCredentialKeepsRevertedChargeAfterSubmissionError(t *testing.T) {
	for _, feePayer := range []bool{false, true} {
		t.Run(fmt.Sprintf("feePayer=%t", feePayer), func(t *testing.T) {
			ctx := context.Background()
			request := buildRequest(t, feePayer, nil)
			rpc := newMockRPC(request)
			rpc.onSend = func(raw string) (string, map[string]any, error) {
				hash, err := tempotx.ComputeHash(raw)
				if err != nil {
					return "", nil, err
				}
				rpc.receipts[hash.Hex()] = map[string]any{"status": "0x0", "logs": []any{}}
				return "", nil, fmt.Errorf("connection reset")
			}
			credential, err := newClientMethod(t, rpc, tempo.CredentialTypeTransaction).CreateCredential(ctx, buildChallenge(t, request))
			require.NoError(t, err)
			config := IntentConfig{Store: tempo.NewMemoryStore(), RPC: rpc}
			if feePayer {
				config.FeePayerPrivateKey = feePayerKey
			}
			intent, err := NewIntent(config)
			require.NoError(t, err)

			// The node included the transaction, which reverted: it can never
			// pay the challenge, so its claim stays.
			_, err = intent.Verify(ctx, credential, request.Map())
			require.ErrorContains(t, err, "transaction reverted")
			require.Len(t, rpc.sentRawTxs, 1)

			_, err = intent.Verify(ctx, credential, request.Map())
			require.ErrorContains(t, err, "already used")
			assert.Len(t, rpc.sentRawTxs, 1)
		})
	}
}

func TestChargeFlow_TransactionCredentialRecoversPendingChargeAfterSubmissionError(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	request := buildRequest(t, false, nil)
	rpc := newMockRPC(request)
	var mined map[string]any
	rpc.onSend = func(raw string) (string, map[string]any, error) {
		tx, err := tempotx.Deserialize(raw)
		if err != nil {
			return "", nil, err
		}
		sender, err := tempotx.VerifySignature(tx)
		if err != nil {
			return "", nil, err
		}
		mined = buildReceipt(raw, request, sender)
		// The send timed out with the caller's context while the transaction
		// was still pending in the mempool.
		cancel()
		return "", nil, context.Canceled
	}
	lookups := 0
	rpc.onGetReceipt = func(string) (*temporpc.JSONRPCResponse, error) {
		lookups++
		if lookups < 2 {
			return &temporpc.JSONRPCResponse{}, nil
		}
		return &temporpc.JSONRPCResponse{Result: mined}, nil
	}
	credential, err := newClientMethod(t, rpc, tempo.CredentialTypeTransaction).CreateCredential(ctx, buildChallenge(t, request))
	require.NoError(t, err)
	intent, err := NewIntent(IntentConfig{Store: tempo.NewMemoryStore(), RPC: rpc})
	require.NoError(t, err)

	_, err = intent.Verify(ctx, credential, request.Map())
	require.NoError(t, err)
	assert.Equal(t, 2, lookups)
}

func TestChargeFlow_TransactionCredentialChecksOnceAfterRejectedSubmission(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	request := buildRequest(t, false, nil)
	rpc := newMockRPC(request)
	rpc.onSend = func(string) (string, map[string]any, error) {
		return "", nil, fmt.Errorf("eth_sendRawTransaction: %w", &temporpc.JSONRPCError{Code: -32000, Message: "nonce too low"})
	}
	lookups := 0
	rpc.onGetReceipt = func(string) (*temporpc.JSONRPCResponse, error) {
		if len(rpc.sentRawTxs) > 0 {
			lookups++
		}
		return &temporpc.JSONRPCResponse{}, nil
	}
	credential, err := newClientMethod(t, rpc, tempo.CredentialTypeTransaction).CreateCredential(ctx, buildChallenge(t, request))
	require.NoError(t, err)
	intent, err := NewIntent(IntentConfig{Store: tempo.NewMemoryStore(), RPC: rpc})
	require.NoError(t, err)

	_, err = intent.Verify(ctx, credential, request.Map())
	require.ErrorContains(t, err, "transaction submission failed")
	assert.Equal(t, 1, lookups)
}

func TestChargeFlow_TransactionCredentialWaitsAfterDuplicateSubmission(t *testing.T) {
	t.Parallel()
	for _, mined := range []bool{true, false} {
		t.Run(fmt.Sprintf("mined=%t", mined), func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			request := buildRequest(t, false, nil)
			rpc := newMockRPC(request)
			var receipt map[string]any
			// The node still holds the transaction an earlier attempt
			// broadcast and rejects the re-broadcast as a duplicate.
			rpc.onSend = func(raw string) (string, map[string]any, error) {
				tx, err := tempotx.Deserialize(raw)
				if err != nil {
					return "", nil, err
				}
				sender, err := tempotx.VerifySignature(tx)
				if err != nil {
					return "", nil, err
				}
				receipt = buildReceipt(raw, request, sender)
				return "", nil, fmt.Errorf("eth_sendRawTransaction: %w", &temporpc.JSONRPCError{Code: -32000, Message: "already known"})
			}
			lookups := 0
			rpc.onGetReceipt = func(string) (*temporpc.JSONRPCResponse, error) {
				if len(rpc.sentRawTxs) == 0 {
					return &temporpc.JSONRPCResponse{}, nil
				}
				lookups++
				if mined && lookups >= 3 {
					return &temporpc.JSONRPCResponse{Result: receipt}, nil
				}
				return &temporpc.JSONRPCResponse{}, nil
			}
			credential, err := newClientMethod(t, rpc, tempo.CredentialTypeTransaction).CreateCredential(ctx, buildChallenge(t, request))
			require.NoError(t, err)
			intent, err := NewIntent(IntentConfig{Store: tempo.NewMemoryStore(), RPC: rpc})
			require.NoError(t, err)

			got, err := intent.Verify(ctx, credential, request.Map())
			if mined {
				// The pending transaction lands while the verifier polls.
				require.NoError(t, err)
				assert.Equal(t, mustTxHash(rpc.sentRawTxs[0]), got.Reference)
				assert.Equal(t, 3, lookups)
				return
			}
			require.ErrorContains(t, err, "outcome is unknown")
			assert.NotContains(t, err.Error(), "transaction submission failed")
			assert.Greater(t, lookups, 1)
		})
	}
}

func TestChargeFlow_FeePayerRetryFindsReceiptForEarlierFeeToken(t *testing.T) {
	ctx := context.Background()
	request := buildRequest(t, true, nil)
	rpc := newMockRPC(request)
	// The fee payer holds only USDC.e at first, so the first attempt pays gas
	// in it.
	rpc.balances = map[common.Address]*big.Int{usdceToken: big.NewInt(1)}
	failReceiptFetch := true
	rpc.onGetReceipt = func(hash string) (*temporpc.JSONRPCResponse, error) {
		if failReceiptFetch && len(rpc.sentRawTxs) > 0 {
			failReceiptFetch = false
			return nil, fmt.Errorf("temporary receipt rpc failure")
		}
		return &temporpc.JSONRPCResponse{Result: rpc.receipts[hash]}, nil
	}
	credential, err := newClientMethod(t, rpc, tempo.CredentialTypeTransaction).CreateCredential(ctx, buildChallenge(t, request))
	require.NoError(t, err)
	intent, err := NewIntent(IntentConfig{
		Store:              tempo.NewMemoryStore(),
		RPC:                rpc,
		FeePayerPrivateKey: feePayerKey,
		AllowedFeeTokens:   []string{pathUSDAddress, usdceAddress},
	})
	require.NoError(t, err)

	_, err = intent.Verify(ctx, credential, request.Map())
	require.ErrorContains(t, err, "outcome is unknown")
	require.Len(t, rpc.sentRawTxs, 1)
	first, err := tempotx.Deserialize(rpc.sentRawTxs[0])
	require.NoError(t, err)
	require.Equal(t, usdceToken, first.FeeToken)

	// The included transaction spent the fee payer's USDC.e and the payer's
	// balance, so the retry picks pathUSD and its simulation would fail. It
	// still finds the USDC.e envelope's receipt and settles from it.
	rpc.balances = nil
	rpc.onCall = drainedBalanceCall
	receipt, err := intent.Verify(ctx, credential, request.Map())
	require.NoError(t, err)
	require.Len(t, rpc.sentRawTxs, 1)
	assert.Equal(t, mustTxHash(rpc.sentRawTxs[0]), receipt.Reference)

	_, err = intent.Verify(ctx, credential, request.Map())
	require.ErrorContains(t, err, "already used")
	assert.Len(t, rpc.sentRawTxs, 1)
}

// ctxStore fails store calls whose context has ended, like a network store.
type ctxStore struct{ *tempo.MemoryStore }

func (s ctxStore) PutIfAbsent(ctx context.Context, key, value string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	return s.MemoryStore.PutIfAbsent(ctx, key, value)
}

func (s ctxStore) Delete(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.MemoryStore.Delete(ctx, key)
}

func TestChargeFlow_StoreWritesAfterSubmissionErrorOutliveRequestContext(t *testing.T) {
	for _, recovered := range []bool{false, true} {
		t.Run(fmt.Sprintf("recovered=%t", recovered), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			// Fee-payer charges record the hash after recovery; others release
			// their reservation when nothing is recovered.
			feePayer := recovered
			request := buildRequest(t, feePayer, nil)
			rpc := newMockRPC(request)
			var broadcastHash string
			rpc.onSend = func(raw string) (string, map[string]any, error) {
				hash, err := tempotx.ComputeHash(raw)
				if err != nil {
					return "", nil, err
				}
				broadcastHash = hash.Hex()
				if recovered {
					tx, err := tempotx.Deserialize(raw)
					if err != nil {
						return "", nil, err
					}
					sender, err := tempotx.VerifySignature(tx)
					if err != nil {
						return "", nil, err
					}
					rpc.receipts[broadcastHash] = buildReceipt(raw, request, sender)
				}
				// The request ends while the RPC reply is lost.
				cancel()
				return "", nil, fmt.Errorf("connection reset")
			}
			credential, err := newClientMethod(t, rpc, tempo.CredentialTypeTransaction).CreateCredential(context.Background(), buildChallenge(t, request))
			require.NoError(t, err)
			store := ctxStore{tempo.NewMemoryStore()}
			intent, err := NewIntent(IntentConfig{Store: store, RPC: rpc, FeePayerPrivateKey: feePayerKey})
			require.NoError(t, err)

			receipt, err := intent.Verify(ctx, credential, request.Map())
			_, stored, getErr := store.Get(context.Background(), tempo.ChargeStoreKey(broadcastHash))
			require.NoError(t, getErr)
			if recovered {
				require.NoError(t, err)
				assert.Equal(t, broadcastHash, receipt.Reference)
				assert.True(t, stored, "recovered fee-payer charge must be recorded")
			} else {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "transaction submission failed")
				assert.False(t, stored, "failed charge must release its reservation")
			}
		})
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

func TestChargeFlow_TransactionCredentialBroadcastsCanonicalRecoveryID(t *testing.T) {
	for _, electrumFirst := range []bool{false, true} {
		t.Run(fmt.Sprintf("electrumFirst=%v", electrumFirst), func(t *testing.T) {
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
			credential, err := newClientMethod(t, rpc, tempo.CredentialTypeTransaction).CreateCredential(ctx, buildChallenge(t, request))
			require.NoError(t, err)

			rawForm := credential.Payload["signature"].(string)
			recoveryID := rawForm[len(rawForm)-2:]
			require.Contains(t, []string{"00", "01"}, recoveryID)
			canonical := rawForm[:len(rawForm)-2] + map[string]string{"00": "1b", "01": "1c"}[recoveryID]
			forms := []string{rawForm, canonical}
			if electrumFirst {
				forms[0], forms[1] = forms[1], forms[0]
			}

			intent, err := NewIntent(IntentConfig{Store: tempo.NewMemoryStore(), RPC: rpc})
			require.NoError(t, err)

			credential.Payload["signature"] = forms[0]
			receipt, err := intent.Verify(ctx, credential, request.Map())
			require.NoError(t, err)
			require.Equal(t, []string{canonical}, rpc.sentRawTxs)
			require.Equal(t, crypto.Keccak256Hash(common.FromHex(canonical)).Hex(), receipt.Reference)

			credential.Payload["signature"] = forms[1]
			_, err = intent.Verify(ctx, credential, request.Map())
			require.ErrorContains(t, err, "transaction hash already used")
			require.Len(t, rpc.sentRawTxs, 1)
		})
	}
}

func TestChargeFlow_KeychainTransactionCredentialBroadcastsCanonicalRecoveryID(t *testing.T) {
	for _, legacyFirst := range []bool{false, true} {
		t.Run(fmt.Sprintf("legacyFirst=%v", legacyFirst), func(t *testing.T) {
			ctx := context.Background()
			request := buildRequest(t, false, nil)
			rpc := newMockRPC(request)
			rootSigner, err := temposigner.NewSigner(testPrivateKey)
			require.NoError(t, err)
			rpc.onSend = func(raw string) (string, map[string]any, error) {
				hash, err := tempotx.ComputeHash(raw)
				if err != nil {
					return "", nil, err
				}
				return hash.Hex(), buildReceipt(raw, request, rootSigner.Address()), nil
			}

			credential, err := newClientMethod(t, rpc, tempo.CredentialTypeTransaction).CreateCredential(ctx, buildChallenge(t, request))
			require.NoError(t, err)
			tx, err := tempotx.Deserialize(credential.Payload["signature"].(string))
			require.NoError(t, err)
			accessSigner, err := temposigner.NewSigner(accessKey)
			require.NoError(t, err)
			tx.Signature = nil
			tx.From = common.Address{}
			authorization := keychain.NewKeyAuthorization(42431, keychain.SignatureTypeSecp256k1, accessSigner.Address()).
				WithExpiry(uint64(time.Now().Add(5 * time.Minute).Unix()))
			require.NoError(t, authorization.SignAndAttach(tx, rootSigner))
			require.NoError(t, keychain.SignWithAccessKey(tx, accessSigner, rootSigner.Address()))
			rawForm, err := tempotx.Serialize(tx, nil)
			require.NoError(t, err)

			recoveryID := rawForm[len(rawForm)-2:]
			require.Contains(t, []string{"00", "01"}, recoveryID)
			canonical := rawForm[:len(rawForm)-2] + map[string]string{"00": "1b", "01": "1c"}[recoveryID]
			forms := []string{rawForm, canonical}
			if legacyFirst {
				forms[0], forms[1] = forms[1], forms[0]
			}

			intent, err := NewIntent(IntentConfig{Store: tempo.NewMemoryStore(), RPC: rpc})
			require.NoError(t, err)

			credential.Payload["signature"] = forms[0]
			receipt, err := intent.Verify(ctx, credential, request.Map())
			require.NoError(t, err)
			require.Equal(t, []string{canonical}, rpc.sentRawTxs)
			require.Equal(t, crypto.Keccak256Hash(common.FromHex(canonical)).Hex(), receipt.Reference)

			credential.Payload["signature"] = forms[1]
			_, err = intent.Verify(ctx, credential, request.Map())
			require.ErrorContains(t, err, "transaction hash already used")
			require.Len(t, rpc.sentRawTxs, 1)
		})
	}
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
	method, err := chargeclient.New(chargeclient.Config{PaymentPolicy: func(context.Context, tempo.ChargeRequest) error { return nil },
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
	return mpptest.NewChallenge(
		"secret-key",
		testRealm,
		tempo.MethodName,
		tempo.IntentCharge,
		request.Map(),
		mpp.WithExpires(mpp.ExpiresIn(5*time.Minute)),
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
		return mustTxHash(raw), buildReceipt(raw, request, sender), nil
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

func TestChargeFlow_TransactionCredentialSourceMustMatchSenderAndChain(t *testing.T) {
	ctx := context.Background()
	// No chainId in the challenge: the source must match the RPC's chain.
	request, err := tempo.NormalizeChargeRequest(tempo.ChargeRequestParams{
		Amount:    "0.50",
		Currency:  testCurrency,
		Recipient: testRecipient,
		Decimals:  6,
	})
	require.NoError(t, err)
	require.Nil(t, request.MethodDetails.ChainID)
	rpc := newMockRPC(request)
	credential, err := newClientMethod(t, rpc, tempo.CredentialTypeTransaction).CreateCredential(ctx, buildChallenge(t, request))
	require.NoError(t, err)
	sender, err := parseSourceDID(credential.Source)
	require.NoError(t, err)
	require.NotNil(t, sender)

	intent, err := NewIntent(IntentConfig{Store: tempo.NewMemoryStore(), RPC: rpc})
	require.NoError(t, err)

	cases := []struct {
		name   string
		source string
		want   string
	}{
		{
			name:   "different sender",
			source: tempo.ProofSource(int64(rpc.chainID), common.HexToAddress("0x00000000000000000000000000000000000000a2")),
			want:   "credential source does not match transaction signer",
		},
		{
			name:   "different chain",
			source: tempo.ProofSource(int64(rpc.chainID)+1, common.HexToAddress(sender.address)),
			want:   "credential source chain id does not match the challenge",
		},
		{
			name:   "malformed",
			source: "invalid-source",
			want:   "credential source is invalid",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tampered := *credential
			tampered.Source = tc.source
			_, err := intent.Validate(ctx, &tampered, request.Map())
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
	assert.Empty(t, rpc.sentRawTxs)

	_, err = intent.Validate(ctx, credential, request.Map())
	require.NoError(t, err)
}

func TestChargeFlow_ConcurrentTransactionReplay(t *testing.T) {
	ctx := context.Background()
	request := buildRequest(t, false, nil)
	credential, err := newClientMethod(t, newMockRPC(request), tempo.CredentialTypeTransaction).CreateCredential(ctx, buildChallenge(t, request))
	require.NoError(t, err)
	raw := credential.Payload["signature"].(string)
	hash, err := tempotx.ComputeHash(raw)
	require.NoError(t, err)
	tx, err := tempotx.Deserialize(raw)
	require.NoError(t, err)
	sender, err := tempotx.VerifySignature(tx)
	require.NoError(t, err)
	store := tempo.NewMemoryStore()
	start := make(chan struct{})
	results := make(chan error, 8)
	for range 8 {
		rpc := newMockRPC(request)
		rpc.receipts[hash.Hex()] = buildReceipt(raw, request, sender)
		intent, err := NewIntent(IntentConfig{RPC: rpc, Store: store})
		require.NoError(t, err)
		go func() {
			<-start
			_, err := intent.Verify(ctx, credential, request.Map())
			results <- err
		}()
	}
	close(start)
	successes := 0
	for range 8 {
		if err := <-results; err == nil {
			successes++
		} else {
			assert.ErrorContains(t, err, "transaction hash already used")
		}
	}
	assert.Equal(t, 1, successes)
}

func TestChargeFlow_HashReceiptReportsFundingCurrency(t *testing.T) {
	for _, scenario := range []string{"direct", "single", "unrelated", "missing", "sender"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			request := buildRequest(t, false, nil)
			rpc := newMockRPC(request)
			credential, err := newClientMethod(t, rpc, tempo.CredentialTypeHash).CreateCredential(ctx, buildChallenge(t, request))
			if !assert.NoError(t, err) || !assert.Len(t, rpc.sentRawTxs, 1) {
				return
			}
			tx, err := tempotx.Deserialize(rpc.sentRawTxs[0])
			if !assert.NoError(t, err) {
				return
			}
			sender, err := tempotx.VerifySignature(tx)
			if !assert.NoError(t, err) {
				return
			}
			calls := make([]any, 0, len(tx.Calls)+1)
			for _, call := range tx.Calls {
				calls = append(calls, map[string]any{"to": call.To.Hex(), "data": hexutil.Encode(call.Data), "value": "0x0"})
			}
			transaction := map[string]any{"hash": testReceiptHash, "from": sender.Hex(), "type": "0x76", "calls": calls}
			switch scenario {
			case "single":
				if !assert.Len(t, calls, 1) {
					return
				}
				call := calls[0].(map[string]any)
				transaction = map[string]any{"hash": testReceiptHash, "from": sender.Hex(), "type": "0x2", "to": call["to"], "input": call["data"]}
			case "unrelated":
				transaction["calls"] = append(calls, map[string]any{
					"to":   tempo.PathUSDAddress,
					"data": hexutil.Encode(common.FromHex(tempo.TransferSelector + strings.Repeat("0", 64) + fmt.Sprintf("%064x", 1))),
				})
			case "sender":
				transaction["from"] = common.HexToAddress("0x3333333333333333333333333333333333333333").Hex()
			}
			rpc.onGetTransaction = func(hash string) (*temporpc.JSONRPCResponse, error) {
				if scenario == "missing" {
					return &temporpc.JSONRPCResponse{}, nil
				}
				return &temporpc.JSONRPCResponse{Result: transaction}, nil
			}
			intent, err := NewIntent(IntentConfig{Store: tempo.NewMemoryStore(), RPC: rpc})
			if !assert.NoError(t, err) {
				return
			}

			receipt, err := intent.Verify(ctx, credential, request.Map())
			if !assert.NoError(t, err) {
				return
			}
			want := ""
			if scenario == "direct" || scenario == "single" {
				want = request.Currency
			}
			assert.Equal(t, want, receipt.FundingCurrency)
		})
	}
}

func TestChargeFlow_TransactionReceiptReportsFundingCurrency(t *testing.T) {
	ctx := context.Background()
	request := buildRequest(t, false, nil)
	rpc := newMockRPC(request)
	credential, err := newClientMethod(t, rpc, tempo.CredentialTypeTransaction).CreateCredential(ctx, buildChallenge(t, request))
	if !assert.NoError(t, err) {
		return
	}
	intent, err := NewIntent(IntentConfig{Store: tempo.NewMemoryStore(), RPC: rpc})
	if !assert.NoError(t, err) {
		return
	}

	receipt, err := intent.Verify(ctx, credential, request.Map())
	if !assert.NoError(t, err) {
		return
	}
	assert.Equal(t, request.Currency, receipt.FundingCurrency)
}

func TestChargeFlow_HashFundingLookupRunsBeforeReplayCommit(t *testing.T) {
	ctx := context.Background()
	request := buildRequest(t, false, nil)
	rpc := newMockRPC(request)
	credential, err := newClientMethod(t, rpc, tempo.CredentialTypeHash).CreateCredential(ctx, buildChallenge(t, request))
	if !assert.NoError(t, err) {
		return
	}
	store := tempo.NewMemoryStore()
	lookups := 0
	rpc.onGetTransaction = func(hash string) (*temporpc.JSONRPCResponse, error) {
		lookups++
		_, used, err := store.Get(ctx, tempo.ChargeStoreKey(hash))
		assert.NoError(t, err)
		assert.False(t, used, "transaction hash was consumed before the funding-currency lookup")
		return &temporpc.JSONRPCResponse{}, nil
	}
	intent, err := NewIntent(IntentConfig{Store: store, RPC: rpc})
	if !assert.NoError(t, err) {
		return
	}

	_, err = intent.Verify(ctx, credential, request.Map())
	assert.NoError(t, err)
	assert.Equal(t, 1, lookups)
}

// mustTxHash returns the hash a node derives for a raw Tempo transaction.
func mustTxHash(raw string) string {
	hash, err := tempotx.ComputeHash(raw)
	if err != nil {
		panic(err)
	}
	return hash.Hex()
}
