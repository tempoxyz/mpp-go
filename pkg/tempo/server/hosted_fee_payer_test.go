package chargeserver

import (
	"context"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	temporpc "github.com/tempoxyz/tempo-go/pkg/client"
	temposigner "github.com/tempoxyz/tempo-go/pkg/signer"
	tempotx "github.com/tempoxyz/tempo-go/pkg/transaction"

	"github.com/tempoxyz/mpp-go/pkg/mpp"
	mppserver "github.com/tempoxyz/mpp-go/pkg/server"
	"github.com/tempoxyz/mpp-go/pkg/tempo"
)

// fakeHostedFeePayer is a hosted fee payer that co-signs, with a local key,
// the transaction an eth_fillTransaction request describes, as mppx's and
// pympp's tests do, so the request shape and the sponsor recovery are checked
// against each other.
type fakeHostedFeePayer struct {
	t        *testing.T
	chainID  int64
	signer   *temposigner.Signer
	feeToken common.Address
	// keyAuthorization is the client's, which the fake cannot rebuild from
	// its JSON-RPC rendering.
	keyAuthorization []interface{}
	// mutate changes the rebuilt transaction before signing.
	mutate func(*tempotx.Tx)

	mu       sync.Mutex
	requests []map[string]any
	headers  []http.Header
}

func (f *fakeHostedFeePayer) handler(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Method string           `json:"method"`
		Params []map[string]any `json:"params"`
	}
	require.NoError(f.t, json.NewDecoder(r.Body).Decode(&body))
	require.Equal(f.t, "eth_fillTransaction", body.Method)
	require.Len(f.t, body.Params, 1)
	params := body.Params[0]
	f.mu.Lock()
	f.requests = append(f.requests, params)
	f.headers = append(f.headers, r.Header.Clone())
	f.mu.Unlock()

	quantity := func(name string) *big.Int {
		value, ok := params[name].(string)
		if !ok {
			return new(big.Int)
		}
		parsed, err := hexutil.DecodeBig(value)
		require.NoError(f.t, err)
		return parsed
	}
	tx := tempotx.New()
	tx.ChainID = big.NewInt(f.chainID)
	tx.Gas = quantity("gas").Uint64()
	tx.MaxFeePerGas = quantity("maxFeePerGas")
	tx.MaxPriorityFeePerGas = quantity("maxPriorityFeePerGas")
	tx.Nonce = quantity("nonce").Uint64()
	tx.NonceKey = quantity("nonceKey")
	tx.ValidBefore = quantity("validBefore").Uint64()
	tx.ValidAfter = quantity("validAfter").Uint64()
	tx.FeeToken = f.feeToken
	tx.KeyAuthorization = f.keyAuthorization
	for _, raw := range params["calls"].([]any) {
		call := raw.(map[string]any)
		to := common.HexToAddress(call["to"].(string))
		value := new(big.Int)
		if v := call["value"].(string); v != "0x" {
			value = hexutil.MustDecodeBig(v)
		}
		data, _ := call["data"].(string)
		tx.Calls = append(tx.Calls, tempotx.Call{To: &to, Value: value, Data: common.FromHex(data)})
	}
	if f.mutate != nil {
		f.mutate(tx)
	}
	hash, err := tempotx.GetFeePayerSignPayload(tx, common.HexToAddress(params["from"].(string)))
	require.NoError(f.t, err)
	signature, err := f.signer.Sign(hash)
	require.NoError(f.t, err)
	_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{"tx": map[string]any{
		"feePayerSignature": map[string]any{
			"r":       hexutil.EncodeBig(signature.R),
			"s":       hexutil.EncodeBig(signature.S),
			"yParity": hexutil.EncodeUint64(uint64(signature.YParity)),
		},
		"feeToken": f.feeToken.Hex(),
	}}})
}

func newFakeHostedFeePayer(t *testing.T, chainID int64, feeToken common.Address) (*fakeHostedFeePayer, *httptest.Server) {
	t.Helper()
	signer, err := temposigner.NewSigner(feePayerKey)
	require.NoError(t, err)
	fake := &fakeHostedFeePayer{t: t, chainID: chainID, signer: signer, feeToken: feeToken}
	server := httptest.NewServer(http.HandlerFunc(fake.handler))
	t.Cleanup(server.Close)
	return fake, server
}

// sponsoredBroadcastRPC records broadcasts and answers each with a receipt
// for the sender.
func sponsoredBroadcastRPC(request tempo.ChargeRequest, sender common.Address) *mockRPC {
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
		return hash.Hex(), receiptForCalls(tx.Calls, request, sender), nil
	}
	return rpc
}

func TestHostedFeePayerSponsorsAndServerBroadcasts(t *testing.T) {
	for _, tt := range []struct {
		name          string
		config        func(url string) IntentConfig
		authorization string
	}{
		{name: "url", config: func(url string) IntentConfig { return IntentConfig{FeePayerURL: url} }},
		{name: "url and headers", authorization: "Bearer static", config: func(url string) IntentConfig {
			return IntentConfig{HostedFeePayer: &HostedFeePayer{URL: url, Headers: map[string]string{
				"Authorization": "Bearer static", "Content-Type": "text/plain",
			}}}
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			payer, err := temposigner.NewSigner(testPrivateKey)
			require.NoError(t, err)
			request := buildRequest(t, true, nil)
			rpc := sponsoredBroadcastRPC(request, payer.Address())
			credential, err := newClientMethod(t, rpc, tempo.CredentialTypeTransaction).CreateCredential(ctx, buildChallenge(t, request))
			require.NoError(t, err)
			client, _, err := deserializeTransactionCredential(credential.Payload["signature"].(string))
			require.NoError(t, err)
			rpc.callRequests = nil

			feeToken := common.HexToAddress("0x20c0000000000000000000000000000000000002")
			fake, sponsor := newFakeHostedFeePayer(t, 42431, feeToken)
			config := tt.config(sponsor.URL)
			config.RPC, config.Store = rpc, tempo.NewMemoryStore()
			intent, err := NewIntent(config)
			require.NoError(t, err)

			receipt, err := intent.Verify(ctx, credential, request.Map())
			require.NoError(t, err)

			// As mppx: simulate from the sender, ask the sponsor, simulate the
			// final sponsored transaction, then broadcast it here.
			require.Len(t, fake.requests, 1)
			assert.Equal(t, tt.authorization, fake.headers[0].Get("Authorization"))
			assert.Equal(t, "application/json", fake.headers[0].Get("Content-Type"))
			call := client.Calls[0]
			assert.Equal(t, map[string]any{
				"calls":                []any{map[string]any{"to": strings.ToLower(call.To.Hex()), "data": hexutil.Encode(call.Data), "value": "0x"}},
				"feePayer":             true,
				"from":                 strings.ToLower(payer.Address().Hex()),
				"gas":                  hexutil.EncodeUint64(client.Gas),
				"maxFeePerGas":         hexutil.EncodeBig(client.MaxFeePerGas),
				"maxPriorityFeePerGas": hexutil.EncodeBig(client.MaxPriorityFeePerGas),
				"nonce":                "0x0",
				"nonceKey":             hexutil.EncodeBig(tempo.ExpiringNonceKey),
				"type":                 "0x76",
				"validBefore":          hexutil.EncodeUint64(client.ValidBefore),
			}, fake.requests[0])
			require.Len(t, rpc.callRequests, 2)
			senderSimulation, sponsoredSimulation := rpc.callRequests[0], rpc.callRequests[1]
			assert.NotContains(t, senderSimulation, "feePayer")
			assert.NotContains(t, senderSimulation, "gas")
			assert.Equal(t, fake.signer.Address().Hex(), sponsoredSimulation["feePayer"])
			assert.Equal(t, feeToken.Hex(), sponsoredSimulation["feeToken"])

			require.Len(t, rpc.sentRawTxs, 1)
			assert.Equal(t, mustTxHash(rpc.sentRawTxs[0]), receipt.Reference)
			broadcast, _, err := deserializeTransactionCredential(rpc.sentRawTxs[0])
			require.NoError(t, err)
			assert.Equal(t, feeToken, broadcast.FeeToken)
			assert.Equal(t, client.Calls, broadcast.Calls)
			assert.Equal(t, client.ValidBefore, broadcast.ValidBefore)
			sponsorAddress, err := tempotx.VerifyFeePayerSignature(broadcast, payer.Address())
			require.NoError(t, err)
			assert.Equal(t, fake.signer.Address(), sponsorAddress)
		})
	}
}

// A sponsor that signs other fields than the client committed to names
// another (unfunded) fee payer: the server simulates with the recovered
// address and broadcasts the client's fields, never the sponsor's.
func TestHostedFeePayerCannotChangeSenderCommittedFields(t *testing.T) {
	ctx := context.Background()
	payer, err := temposigner.NewSigner(testPrivateKey)
	require.NoError(t, err)
	request := buildRequest(t, true, nil)
	rpc := sponsoredBroadcastRPC(request, payer.Address())
	credential, err := newClientMethod(t, rpc, tempo.CredentialTypeTransaction).CreateCredential(ctx, buildChallenge(t, request))
	require.NoError(t, err)
	client, _, err := deserializeTransactionCredential(credential.Payload["signature"].(string))
	require.NoError(t, err)
	rpc.callRequests = nil
	fake, sponsor := newFakeHostedFeePayer(t, 42431, common.HexToAddress(tempo.PathUSDAddress))
	fake.mutate = func(tx *tempotx.Tx) { tx.Gas++ }
	intent, err := NewIntent(IntentConfig{RPC: rpc, Store: tempo.NewMemoryStore(), FeePayerURL: sponsor.URL})
	require.NoError(t, err)

	_, err = intent.Verify(ctx, credential, request.Map())
	require.NoError(t, err)
	require.Len(t, rpc.callRequests, 2)
	assert.NotEqual(t, fake.signer.Address().Hex(), rpc.callRequests[1]["feePayer"])
	broadcast, _, err := deserializeTransactionCredential(rpc.sentRawTxs[0])
	require.NoError(t, err)
	assert.Equal(t, client.Gas, broadcast.Gas)
}

func TestHostedFeeTokenRespectsAllowlist(t *testing.T) {
	ctx := context.Background()
	payer, err := temposigner.NewSigner(testPrivateKey)
	require.NoError(t, err)
	request := buildRequest(t, true, nil)
	rpc := sponsoredBroadcastRPC(request, payer.Address())
	credential, err := newClientMethod(t, rpc, tempo.CredentialTypeTransaction).CreateCredential(ctx, buildChallenge(t, request))
	require.NoError(t, err)
	_, sponsor := newFakeHostedFeePayer(t, 42431, common.HexToAddress("0x20c0000000000000000000000000000000000002"))
	intent, err := NewIntent(IntentConfig{
		RPC: rpc, Store: tempo.NewMemoryStore(), FeePayerURL: sponsor.URL,
		AllowedFeeTokens: []string{tempo.PathUSDAddress},
	})
	require.NoError(t, err)

	_, err = intent.Verify(ctx, credential, request.Map())
	require.ErrorContains(t, err, "fee token is not supported")
	assert.Empty(t, rpc.sentRawTxs)
	// The failed sponsorship released its claim: a retry asks the sponsor again.
	_, err = intent.Verify(ctx, credential, request.Map())
	require.ErrorContains(t, err, "fee token is not supported")
}

func TestHostedFeePayerFailures(t *testing.T) {
	for _, tt := range []struct {
		name  string
		reply any
		want  string
	}{
		{name: "rpc error", reply: map[string]any{"jsonrpc": "2.0", "id": 1, "error": map[string]any{"code": -32000, "message": "no"}}, want: "hosted fee payer failed to sponsor transaction"},
		{name: "no signature", reply: map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{"tx": map[string]any{"feeToken": tempo.PathUSDAddress}}}, want: "hosted fee payer failed to sponsor transaction"},
		{name: "no fee token", reply: map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{"tx": map[string]any{"feePayerSignature": map[string]any{"r": "0x1", "s": "0x1", "yParity": "0x0"}}}}, want: "did not return a feeToken"},
		{name: "invalid signature", reply: map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{"tx": map[string]any{"feePayerSignature": map[string]any{"r": "0x0", "s": "0x1", "yParity": "0x2"}, "feeToken": tempo.PathUSDAddress}}}, want: "invalid feePayerSignature"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			request := buildRequest(t, true, nil)
			rpc := newMockRPC(request)
			credential, err := newClientMethod(t, rpc, tempo.CredentialTypeTransaction).CreateCredential(ctx, buildChallenge(t, request))
			require.NoError(t, err)
			sponsor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_ = json.NewEncoder(w).Encode(tt.reply)
			}))
			defer sponsor.Close()
			intent, err := NewIntent(IntentConfig{RPC: rpc, Store: tempo.NewMemoryStore(), FeePayerURL: sponsor.URL})
			require.NoError(t, err)
			_, err = intent.Verify(ctx, credential, request.Map())
			require.ErrorContains(t, err, tt.want)
			assert.Empty(t, rpc.sentRawTxs)
		})
	}
}

// Policy violations and reverting calls never reach the hosted fee payer.
func TestHostedFeePayerIsNotAskedToSponsorRejectedTransactions(t *testing.T) {
	ctx := context.Background()
	request := buildRequest(t, true, nil)
	rpc := newMockRPC(request)
	credential, err := newClientMethod(t, rpc, tempo.CredentialTypeTransaction).CreateCredential(ctx, buildChallenge(t, request))
	require.NoError(t, err)
	fake, sponsor := newFakeHostedFeePayer(t, 42431, common.HexToAddress(tempo.PathUSDAddress))

	strict, err := NewIntent(IntentConfig{RPC: rpc, Store: tempo.NewMemoryStore(), FeePayerURL: sponsor.URL,
		FeePayerPolicy: &FeePayerPolicy{MaxFeePerGas: big.NewInt(1), MaxPriorityFeePerGas: big.NewInt(1), MaxTotalFee: big.NewInt(1)}})
	require.NoError(t, err)
	_, err = strict.Verify(ctx, credential, request.Map())
	require.ErrorContains(t, err, "sponsor policy")

	rpc.onCall = func(...interface{}) (*temporpc.JSONRPCResponse, error) {
		return temporpc.NewJSONRPCErrorResponse(1, temporpc.InvalidTransactionType, "execution reverted", nil), nil
	}
	reverting, err := NewIntent(IntentConfig{RPC: rpc, Store: tempo.NewMemoryStore(), FeePayerURL: sponsor.URL})
	require.NoError(t, err)
	_, err = reverting.Verify(ctx, credential, request.Map())
	require.ErrorContains(t, err, "transaction preflight failed")

	validation, err := reverting.Validate(ctx, credential, request.Map())
	require.NoError(t, err)
	assert.Equal(t, "pull", validation.Details["mode"])
	assert.Empty(t, fake.requests)
	assert.Empty(t, rpc.sentRawTxs)
}

// A key authorization reaches the sponsor and the final simulation as its
// JSON-RPC object, and the broadcast unchanged.
func TestHostedFeePayerCarriesKeyAuthorization(t *testing.T) {
	ctx := context.Background()
	payer, err := temposigner.NewSigner(testPrivateKey)
	require.NoError(t, err)
	request := buildRequest(t, true, nil)
	rpc := sponsoredBroadcastRPC(request, payer.Address())
	credential, err := newClientMethod(t, rpc, tempo.CredentialTypeTransaction).CreateCredential(ctx, buildChallenge(t, request))
	require.NoError(t, err)
	tx, _, err := deserializeTransactionCredential(credential.Payload["signature"].(string))
	require.NoError(t, err)
	accessKey := common.HexToAddress("0x00000000000000000000000000000000000000ab")
	signatureBytes := make([]byte, 65)
	signatureBytes[31], signatureBytes[63], signatureBytes[64] = 1, 2, 28
	tx.KeyAuthorization = []interface{}{
		[]interface{}{[]byte{0xa5, 0xbf}, []byte{}, accessKey.Bytes(), []byte{0x01}, []interface{}{[]interface{}{common.HexToAddress(testCurrency).Bytes(), []byte{0x10}}}},
		signatureBytes,
	}
	tx.Signature = nil
	require.NoError(t, tempotx.SignTransaction(tx, payer))
	tx.From = common.Address{}
	serialized, err := tempotx.Serialize(tx, nil)
	require.NoError(t, err)
	credential.Payload["signature"] = serialized
	rpc.callRequests = nil

	fake, sponsor := newFakeHostedFeePayer(t, 42431, common.HexToAddress(tempo.PathUSDAddress))
	fake.keyAuthorization = tx.KeyAuthorization
	intent, err := NewIntent(IntentConfig{RPC: rpc, Store: tempo.NewMemoryStore(), FeePayerURL: sponsor.URL})
	require.NoError(t, err)
	_, err = intent.Verify(ctx, credential, request.Map())
	require.NoError(t, err)

	want := map[string]any{
		"chainId":   "0xa5bf",
		"expiry":    "0x1",
		"keyId":     strings.ToLower(accessKey.Hex()),
		"keyType":   "secp256k1",
		"limits":    []any{map[string]any{"token": strings.ToLower(testCurrency), "limit": "0x10"}},
		"signature": map[string]any{"r": hex32([]byte{1}), "s": hex32([]byte{2}), "yParity": "0x1", "type": "secp256k1"},
	}
	require.Len(t, fake.requests, 1)
	assert.Equal(t, want, fake.requests[0]["keyAuthorization"])
	require.Len(t, rpc.callRequests, 2)
	rendered, err := json.Marshal(rpc.callRequests[1]["keyAuthorization"])
	require.NoError(t, err)
	var simulated map[string]any
	require.NoError(t, json.Unmarshal(rendered, &simulated))
	assert.Equal(t, want, simulated)
	assert.Equal(t, fake.signer.Address().Hex(), rpc.callRequests[1]["feePayer"])
	broadcast, _, err := deserializeTransactionCredential(rpc.sentRawTxs[0])
	require.NoError(t, err)
	assert.Equal(t, tx.KeyAuthorization, broadcast.KeyAuthorization)
}

// Servers no longer read methodDetails.feePayerUrl from a challenge: it is
// never contacted, and without a configured fee payer the charge fails.
func TestChallengeFeePayerURLIsIgnored(t *testing.T) {
	ctx := context.Background()
	called := false
	sponsor := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	defer sponsor.Close()
	request := buildRequest(t, true, nil)
	request.MethodDetails.FeePayerURL = sponsor.URL //nolint:staticcheck // An old challenge carrying the URL.
	rpc := newMockRPC(request)
	credential, err := newClientMethod(t, rpc, tempo.CredentialTypeTransaction).CreateCredential(ctx, buildChallenge(t, request))
	require.NoError(t, err)

	intent, err := NewIntent(IntentConfig{RPC: rpc, Store: tempo.NewMemoryStore()})
	require.NoError(t, err)
	_, err = intent.Verify(ctx, credential, request.Map())
	require.ErrorContains(t, err, "requires a configured fee payer")

	local, err := NewIntent(IntentConfig{RPC: rpc, Store: tempo.NewMemoryStore(), FeePayerPrivateKey: feePayerKey})
	require.NoError(t, err)
	_, err = local.Verify(ctx, credential, request.Map())
	require.NoError(t, err)
	assert.False(t, called)
}

// Like mppx's charge request(), a method with a fee payer sponsors every
// route unless the route opts out, and never advertises one without it.
func TestRoutesSponsorByDefaultWithAFeePayer(t *testing.T) {
	t.Parallel()

	for _, config := range []IntentConfig{
		{FeePayerPrivateKey: feePayerKey},
		{FeePayerURL: "https://sponsor.example.test/rpc"},
		{HostedFeePayer: &HostedFeePayer{URL: "https://sponsor.example.test/rpc", Headers: map[string]string{"Authorization": "Bearer x"}}},
	} {
		config.Store = tempo.NewMemoryStore()
		intent, err := NewIntent(config)
		require.NoError(t, err)
		method := newMethod(methodConfig{Intent: intent, Recipient: testRecipient, ChainID: 42431})
		sponsored, err := method.BuildChargeRequest(mppserver.ChargeParams{Amount: "0.50"})
		require.NoError(t, err)
		request, err := tempo.ParseChargeRequest(sponsored)
		require.NoError(t, err)
		assert.True(t, request.MethodDetails.FeePayer)
		assert.Empty(t, request.MethodDetails.FeePayerURL) //nolint:staticcheck // Challenges no longer carry it.

		unsponsored, err := method.BuildChargeRequest(mppserver.ChargeParams{Amount: "0.50", DisableFeePayer: true})
		require.NoError(t, err)
		request, err = tempo.ParseChargeRequest(unsponsored)
		require.NoError(t, err)
		assert.False(t, request.MethodDetails.FeePayer)

		// Splits combine with sponsorship.
		_, err = method.BuildChargeRequest(mppserver.ChargeParams{Amount: "0.50", Splits: []tempo.SplitParams{{Amount: "0.10", Recipient: "0x3c44cdddb6a900fa2b585dd299e03d12fa4293bc"}}})
		require.NoError(t, err)
	}

	plain := newMethod(methodConfig{Intent: testStoredIntent(), Recipient: testRecipient, ChainID: 42431})
	request, err := plain.BuildChargeRequest(mppserver.ChargeParams{Amount: "0.50"})
	require.NoError(t, err)
	assert.NotContains(t, request, "methodDetails.feePayer")
	_, err = plain.BuildChargeRequest(mppserver.ChargeParams{Amount: "0.50", FeePayer: true})
	require.ErrorContains(t, err, "requires a fee payer")
	_, err = plain.BuildChargeRequest(mppserver.ChargeParams{Amount: "0.50", FeePayerURL: "https://sponsor.example.test"}) //nolint:staticcheck // The deprecated field is refused.
	require.ErrorContains(t, err, "FeePayerURL is no longer supported")

	_, err = New(Config{Store: tempo.NewMemoryStore(), Recipient: testRecipient, FeePayer: true})
	require.ErrorContains(t, err, "FeePayer requires a fee payer")
	_, err = New(Config{Store: tempo.NewMemoryStore(), Recipient: testRecipient, FeePayerURL: "https://a.test", FeePayerPrivateKey: feePayerKey})
	require.ErrorContains(t, err, "not both")
	_, err = NewIntent(IntentConfig{Store: tempo.NewMemoryStore(), FeePayerURL: "https://a.test", HostedFeePayer: &HostedFeePayer{URL: "https://b.test"}})
	require.ErrorContains(t, err, "not both")
	method, err := New(Config{Store: tempo.NewMemoryStore(), Recipient: testRecipient, ChainID: 42431, FeePayerURL: "https://sponsor.example.test/rpc"})
	require.NoError(t, err)
	offers, err := method.BuildChargeRequests(mppserver.ChargeParams{Amount: "0.50"})
	require.NoError(t, err)
	for _, offer := range offers {
		parsed, err := tempo.ParseChargeRequest(offer)
		require.NoError(t, err)
		assert.True(t, parsed.MethodDetails.FeePayer)
	}
	_ = mpp.IntentCharge
}
