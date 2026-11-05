package chargeserver

import (
	"context"
	"encoding/json"
	"math"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	temporpc "github.com/tempoxyz/tempo-go/pkg/client"
	temposigner "github.com/tempoxyz/tempo-go/pkg/signer"
	tempotx "github.com/tempoxyz/tempo-go/pkg/transaction"

	"github.com/tempoxyz/mpp-go/pkg/tempo"
)

// sponsoredSimulationRequest returns the final sponsored eth_call among
// requests (the one naming a feePayer) and its index.
func sponsoredSimulationRequest(t *testing.T, requests []map[string]any) (map[string]any, int) {
	t.Helper()
	for index, request := range requests {
		if _, ok := request["feePayer"]; ok {
			return request, index
		}
	}
	t.Fatalf("no sponsored simulation among %d eth_calls", len(requests))
	return nil, -1
}

// recoverSimulationFeePayer recovers the fee payer from the final sponsored
// eth_call's feePayerSignature over tx with the simulated gas limit.
func recoverSimulationFeePayer(t *testing.T, simulation map[string]any, tx *tempotx.Tx, sender common.Address) common.Address {
	t.Helper()
	rpcSig, ok := simulation["feePayerSignature"].(map[string]any)
	require.True(t, ok, "final sponsored simulation must carry feePayerSignature")
	signature, err := parseRPCSignature(rpcSig)
	require.NoError(t, err)
	signed := tx.Clone()
	signed.Gas = hexutil.MustDecodeUint64(simulation["gas"].(string))
	signed.FeePayerSignature = signature
	feePayer, err := tempotx.VerifyFeePayerSignature(signed, sender)
	require.NoError(t, err)
	return feePayer
}

// A local fee payer signs a simulation-only copy with
// sponsoredSimulationGasBuffer more gas after the sender's simulation passed,
// and the node bills the fee payer recovered from that signature; the
// broadcast transaction keeps its gas.
func TestLocalFeePayerSimulatesWithFeePayerSignature(t *testing.T) {
	ctx := context.Background()
	payer, err := temposigner.NewSigner(testPrivateKey)
	require.NoError(t, err)
	feePayer, err := temposigner.NewSigner(feePayerKey)
	require.NoError(t, err)
	request := buildRequest(t, true, nil)
	rpc := sponsoredBroadcastRPC(request, payer.Address())
	credential, err := newClientMethod(t, rpc, tempo.CredentialTypeTransaction).CreateCredential(ctx, buildChallenge(t, request))
	require.NoError(t, err)
	client, _, err := deserializeTransactionCredential(credential.Payload["signature"].(string))
	require.NoError(t, err)
	rpc.callRequests = nil
	intent, err := NewIntent(IntentConfig{RPC: rpc, Store: tempo.NewMemoryStore(), FeePayerPrivateKey: feePayerKey})
	require.NoError(t, err)

	_, err = intent.Verify(ctx, credential, request.Map())
	require.NoError(t, err)

	simulation, index := sponsoredSimulationRequest(t, rpc.callRequests)
	require.Equal(t, 1, index, "simulate the sender first, then the sponsored transaction")
	senderSimulation := rpc.callRequests[0]
	assert.NotContains(t, senderSimulation, "feePayerSignature")
	assert.NotContains(t, senderSimulation, "signature")
	assert.NotContains(t, simulation, "signature")
	assert.Equal(t, feePayer.Address().Hex(), simulation["feePayer"])

	require.Len(t, rpc.sentRawTxs, 1)
	broadcast, _, err := deserializeTransactionCredential(rpc.sentRawTxs[0])
	require.NoError(t, err)
	assert.Equal(t, client.Gas, broadcast.Gas, "the broadcast transaction keeps its gas")
	assert.Equal(t, hexutil.EncodeUint64(broadcast.Gas+sponsoredSimulationGasBuffer), simulation["gas"])
	assert.Equal(t, broadcast.FeeToken.Hex(), simulation["feeToken"])
	assert.Equal(t, feePayer.Address(), recoverSimulationFeePayer(t, simulation, broadcast, payer.Address()))
	broadcastFeePayer, err := tempotx.VerifyFeePayerSignature(broadcast, payer.Address())
	require.NoError(t, err)
	assert.Equal(t, feePayer.Address(), broadcastFeePayer)
}

// The simulation-only signature is made only once the sender's simulation
// passed: a failing sender simulation ends before any sponsored eth_call.
func TestLocalFeePayerSenderSimulationFailureSkipsSponsoredSimulation(t *testing.T) {
	ctx := context.Background()
	payer, err := temposigner.NewSigner(testPrivateKey)
	require.NoError(t, err)
	request := buildRequest(t, true, nil)
	rpc := sponsoredBroadcastRPC(request, payer.Address())
	credential, err := newClientMethod(t, rpc, tempo.CredentialTypeTransaction).CreateCredential(ctx, buildChallenge(t, request))
	require.NoError(t, err)
	rpc.callRequests = nil
	rpc.onCall = func(...interface{}) (*temporpc.JSONRPCResponse, error) {
		return temporpc.NewJSONRPCErrorResponse(1, temporpc.InvalidTransactionType, "execution reverted", nil), nil
	}
	intent, err := NewIntent(IntentConfig{RPC: rpc, Store: tempo.NewMemoryStore(), FeePayerPrivateKey: feePayerKey})
	require.NoError(t, err)

	_, err = intent.Verify(ctx, credential, request.Map())
	require.ErrorContains(t, err, "transaction preflight failed")
	require.Len(t, rpc.callRequests, 1)
	assert.NotContains(t, rpc.callRequests[0], "feePayer")
	assert.NotContains(t, rpc.callRequests[0], "feePayerSignature")
	assert.Empty(t, rpc.sentRawTxs)
}

// A hosted fee payer's signature is simulated as it signed it: same gas,
// recovering to the sponsor.
func TestHostedFeePayerSimulatesWithFeePayerSignature(t *testing.T) {
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
	fake, sponsor := newFakeHostedFeePayer(t, 42431, common.HexToAddress("0x20c0000000000000000000000000000000000002"))
	intent, err := NewIntent(IntentConfig{RPC: rpc, Store: tempo.NewMemoryStore(), FeePayerURL: sponsor.URL})
	require.NoError(t, err)

	_, err = intent.Verify(ctx, credential, request.Map())
	require.NoError(t, err)

	simulation, index := sponsoredSimulationRequest(t, rpc.callRequests)
	require.Equal(t, 1, index)
	assert.NotContains(t, rpc.callRequests[0], "feePayerSignature")
	assert.NotContains(t, simulation, "signature")
	require.Len(t, rpc.sentRawTxs, 1)
	broadcast, _, err := deserializeTransactionCredential(rpc.sentRawTxs[0])
	require.NoError(t, err)
	assert.Equal(t, client.Gas, broadcast.Gas)
	assert.Equal(t, hexutil.EncodeUint64(broadcast.Gas), simulation["gas"])
	assert.Equal(t, rpcSignature(broadcast.FeePayerSignature), simulation["feePayerSignature"])
	assert.Equal(t, fake.signer.Address(), recoverSimulationFeePayer(t, simulation, broadcast, payer.Address()))
}

// Both signature parities survive the JSON-RPC round trip, including the
// falsy yParity 0 that nodes send as "0x0".
func TestRPCSignatureParities(t *testing.T) {
	signer, err := temposigner.NewSigner(feePayerKey)
	require.NoError(t, err)
	found := map[uint8]*temposigner.Signature{}
	for i := 0; len(found) < 2; i++ {
		signature, err := signer.Sign(crypto.Keccak256Hash(big.NewInt(int64(i)).Bytes()))
		require.NoError(t, err)
		found[signature.YParity] = signature
	}
	for parity, want := range map[uint8]string{0: "0x0", 1: "0x1"} {
		signature := found[parity]
		encoded := rpcSignature(signature)
		assert.Equal(t, want, encoded["yParity"])
		decoded, err := parseRPCSignature(encoded)
		require.NoError(t, err)
		assert.Equal(t, signature.R, decoded.R)
		assert.Equal(t, signature.S, decoded.S)
		assert.Equal(t, parity, decoded.YParity)
		// Numeric yParity, as JSON decodes it with UseNumber.
		numeric := map[string]any{"r": encoded["r"], "s": encoded["s"], "yParity": json.Number([]string{"0", "1"}[parity])}
		decoded, err = parseRPCSignature(numeric)
		require.NoError(t, err)
		assert.Equal(t, parity, decoded.YParity)
	}
}

func TestFeePayerSimulationTransaction(t *testing.T) {
	sender, err := temposigner.NewSigner(testPrivateKey)
	require.NoError(t, err)
	feePayer, err := temposigner.NewSigner(feePayerKey)
	require.NoError(t, err)
	to := common.HexToAddress("0x20c0000000000000000000000000000000000001")
	tx := tempotx.New()
	tx.ChainID = big.NewInt(42431)
	tx.Gas = 67_894
	tx.MaxFeePerGas = big.NewInt(20_000_000_000)
	tx.MaxPriorityFeePerGas = big.NewInt(1)
	tx.FeeToken = common.HexToAddress("0x20c0000000000000000000000000000000000002")
	tx.Calls = []tempotx.Call{{To: &to, Value: new(big.Int), Data: []byte{1}}}
	tx.AwaitingFeePayer = true
	require.NoError(t, tempotx.SignTransaction(tx, sender))
	tx.AwaitingFeePayer = false
	tx.From = sender.Address()
	require.NoError(t, tempotx.AddFeePayerSignature(tx, feePayer))
	broadcastSignature := tx.FeePayerSignature

	simulation, err := feePayerSimulationTransaction(tx, feePayer)
	require.NoError(t, err)
	assert.Equal(t, uint64(67_894), tx.Gas, "the broadcast transaction keeps its gas")
	assert.Same(t, broadcastSignature, tx.FeePayerSignature)
	assert.Equal(t, uint64(67_894+sponsoredSimulationGasBuffer), simulation.Gas)
	assert.NotEqual(t, broadcastSignature, simulation.FeePayerSignature)
	recovered, err := tempotx.VerifyFeePayerSignature(simulation, sender.Address())
	require.NoError(t, err)
	assert.Equal(t, feePayer.Address(), recovered)

	tx.Gas = math.MaxUint64
	_, err = feePayerSimulationTransaction(tx, feePayer)
	require.Error(t, err)
}
