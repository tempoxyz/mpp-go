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

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tempoxyz/mpp-go/pkg/mpp"
	"github.com/tempoxyz/mpp-go/pkg/server"
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
	feePayerKey = "0xdd83cd66cd98801a07e0b7c1a5b02364b369e696da7c0ab444acffea5cca86fc"
	// accessKey is a deterministic test-only key authorized by testPrivateKey.
	accessKey       = "0x0000000000000000000000000000000000000000000000000000000000000003"
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
	callRequests     []map[string]any
	estimateGasCalls []map[string]any
	onCall           func(params ...interface{}) (*temporpc.JSONRPCResponse, error)
	onSend           func(raw string) (string, map[string]any, error)
	onEstimateGas    func(params ...interface{}) (*temporpc.JSONRPCResponse, error)
	onGetReceipt     func(hash string) (*temporpc.JSONRPCResponse, error)
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
	challenge := mpp.NewChallenge(
		"test-secret-key-minimum-32-byte-secret",
		testRealm,
		tempo.MethodName,
		tempo.IntentCharge,
		requestMap,
		mpp.WithExpires(mpp.Expires.Minutes(5)),
	)
	credential, err := newClientMethod(t, rpc, tempo.CredentialTypeTransaction).CreateCredential(ctx, challenge)
	require.NoError(t, err)
	rpc.callRequests = nil
	store := newRecordingStore()
	method, err := MethodFromConfig(Config{RPC: rpc, Store: store})
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
	assert.Equal(t, testReceiptHash, receipt.Reference)
	assert.Len(t, rpc.sentRawTxs, 1)
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
	require.ErrorContains(t, err, "failed to release sponsored replay claim: delete unavailable")
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
				if broadcast.FeeToken != common.HexToAddress(request.Currency) {
					return "", nil, fmt.Errorf("broadcast fee token = %s, want %s", broadcast.FeeToken.Hex(), request.Currency)
				}
				gotParity := broadcast.Signature.Raw[keychain.KeychainSignatureLength-1]
				if tc.legacyYParity && gotParity != 27 && gotParity != 28 {
					return "", nil, fmt.Errorf("broadcast y parity = %d, want unmodified legacy parity", gotParity)
				}
				if !tc.legacyYParity && gotParity != 0 && gotParity != 1 {
					return "", nil, fmt.Errorf("broadcast y parity = %d, want canonical parity", gotParity)
				}
				feePayer, err := tempotx.VerifyFeePayerSignature(broadcast, rootSigner.Address())
				if err != nil {
					return "", nil, err
				}
				if feePayer != feePayerSigner.Address() {
					return "", nil, fmt.Errorf("broadcast fee payer = %s, want %s", feePayer.Hex(), feePayerSigner.Address().Hex())
				}
				return testReceiptHash, buildReceipt(raw, request, rootSigner.Address()), nil
			}

			intent, err := NewIntent(IntentConfig{RPC: rpc, FeePayerPrivateKey: feePayerKey})
			if err != nil {
				t.Fatalf("NewIntent() error = %v", err)
			}
			method := NewMethod(MethodConfig{
				Intent:    intent,
				Currency:  testCurrency,
				Recipient: testRecipient,
				ChainID:   42431,
				FeePayer:  true,
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
			paidRequest.Header.Set("Authorization", credential.ToAuthorization())
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
			name:      "fee token does not match request",
			options:   keychainCredentialOptions{feeToken: common.HexToAddress("0x20c0000000000000000000000000000000000002")},
			wantError: "fee payer transaction fee token does not match the charge request",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			request := buildRequest(t, true, nil)
			rpc := newMockRPC(request)
			challenge := buildChallenge(t, request)
			credential := buildKeychainFeePayerCredential(t, rpc, challenge, tc.options)
			intent, err := NewIntent(IntentConfig{RPC: rpc, FeePayerPrivateKey: feePayerKey})
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
		feeToken = common.HexToAddress(testCurrency)
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

func TestChargeFlow_HashCredentialAcceptsExplicitPrimaryMemo(t *testing.T) {
	ctx := context.Background()
	method := NewMethod(MethodConfig{
		Currency:  testCurrency,
		Recipient: testRecipient,
		Decimals:  6,
		ChainID:   42431,
	})
	requestMap, err := method.BuildChargeRequest(server.ChargeParams{
		Amount:         "0.50",
		Memo:           "0x0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20",
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

	intent, err := NewIntent(IntentConfig{RPC: rpc})
	if !assert.NoErrorf(t, err,
		"NewIntent() error = %v", err) {
		return
	}

	wrongMemoRequest := request
	wrongMemoRequest.MethodDetails.Memo = "0x2020202020202020202020202020202020202020202020202020202020202020"
	if _, err := intent.Verify(ctx, credential, wrongMemoRequest.Map()); err == nil || !strings.Contains(err.Error(), "does not satisfy") {
		assert.Failf(t, "", "Verify() error = %v, want memo mismatch", err)
		return
	}

	receipt, err := intent.Verify(ctx, credential, request.Map())
	if !assert.NoErrorf(t, err, "Verify() error = %v", err) ||
		!assert.Equal(t, testReceiptHash, receipt.Reference) {
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
		if !assert.Equalf(t, request.Currency, callObject["feeToken"],
			"eth_call feeToken = %v, want %s", callObject["feeToken"], request.Currency) {
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
