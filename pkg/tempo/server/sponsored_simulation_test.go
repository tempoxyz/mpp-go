package chargeserver

import (
	"context"
	"encoding/json"
	"errors"
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
// sponsoredSimulationGasBuffer more gas (within the sponsor policy) after the
// sender's simulation at the broadcast gas limit passed,
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
// recovering to the sponsor (TestHostedFeePayerSponsoredSimulationToleratesOnlyOutOfGas
// covers that gas running short).
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
	newTx := func(gas uint64) *tempotx.Tx {
		tx := tempotx.New()
		tx.ChainID = big.NewInt(42431)
		tx.Gas = gas
		tx.MaxFeePerGas = big.NewInt(20_000_000_000)
		tx.MaxPriorityFeePerGas = big.NewInt(1)
		tx.FeeToken = common.HexToAddress("0x20c0000000000000000000000000000000000002")
		tx.Calls = []tempotx.Call{{To: &to, Value: new(big.Int), Data: []byte{1}}}
		tx.AwaitingFeePayer = true
		require.NoError(t, tempotx.SignTransaction(tx, sender))
		tx.AwaitingFeePayer = false
		tx.From = sender.Address()
		require.NoError(t, tempotx.AddFeePayerSignature(tx, feePayer))
		return tx
	}
	policyWithBudget := func(gas uint64) FeePayerPolicy {
		policy := defaultFeePayerPolicy()
		policy.MaxTotalFee = new(big.Int).Mul(new(big.Int).SetUint64(gas), big.NewInt(20_000_000_000))
		return policy
	}

	for _, tt := range []struct {
		name    string
		gas     uint64
		policy  FeePayerPolicy
		wantGas uint64
		wantErr string
	}{
		{name: "full buffer", gas: 67_894, policy: defaultFeePayerPolicy(), wantGas: 67_894 + sponsoredSimulationGasBuffer},
		{name: "clamped to the total fee budget", gas: 67_894, policy: policyWithBudget(67_894 + 3_000), wantGas: 67_894 + 3_000},
		{name: "no headroom in the total fee budget", gas: 67_894, policy: policyWithBudget(67_894), wantGas: 67_894},
		{name: "clamped to the gas cap", gas: feePayerMaxGas - 4_000, policy: defaultFeePayerPolicy(), wantGas: feePayerMaxGas},
		{name: "at the gas cap", gas: feePayerMaxGas, policy: defaultFeePayerPolicy(), wantGas: feePayerMaxGas},
		// The copy is checked against the policy before it is signed: one
		// the policy refuses is never signed.
		{name: "over the total fee budget", gas: 67_894, policy: policyWithBudget(67_000), wantErr: "total fee budget exceeds sponsor policy"},
		{name: "over the gas cap", gas: math.MaxUint64, policy: defaultFeePayerPolicy(), wantErr: "gas exceeds sponsor policy"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			tx := newTx(tt.gas)
			broadcastSignature := tx.FeePayerSignature

			simulation, err := feePayerSimulationTransaction(tx, feePayer, "", tt.policy)
			assert.Equal(t, tt.gas, tx.Gas, "the broadcast transaction keeps its gas")
			assert.Same(t, broadcastSignature, tx.FeePayerSignature)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				assert.Nil(t, simulation)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantGas, simulation.Gas)
			require.NoError(t, validateFeePayerTransaction(simulation, "", tt.policy), "the signed copy stays within the sponsor policy")
			if tt.wantGas != tt.gas {
				assert.NotEqual(t, broadcastSignature, simulation.FeePayerSignature)
			}
			recovered, err := tempotx.VerifyFeePayerSignature(simulation, sender.Address())
			require.NoError(t, err)
			assert.Equal(t, feePayer.Address(), recovered)
		})
	}
}

// outOfGas, feeBalance and reverted are eth_call errors as Tempo v1.18 nodes
// answer them (out of gas and the fee balance check share code -32003).
var (
	outOfGas   = temporpc.NewJSONRPCErrorResponse(1, -32003, "out of gas: gas exhausted during precompiled contract execution: 53602", nil)
	feeBalance = temporpc.NewJSONRPCErrorResponse(1, -32003, "insufficient fee token balance: required 394, but only have 0", nil)
	reverted   = temporpc.NewJSONRPCErrorResponse(1, 3, "execution reverted", nil)
)

// answerSimulations answers the sender's simulation (no feePayer) with
// sender and the final sponsored simulation with sponsored; nil succeeds.
func answerSimulations(sender, sponsored *temporpc.JSONRPCResponse) func(...interface{}) (*temporpc.JSONRPCResponse, error) {
	return func(params ...interface{}) (*temporpc.JSONRPCResponse, error) {
		response := sender
		if _, ok := params[0].(map[string]any)["feePayer"]; ok {
			response = sponsored
		}
		if response == nil {
			return &temporpc.JSONRPCResponse{Result: "0x"}, nil
		}
		return response, nil
	}
}

type sponsoredSimulationCase struct {
	name      string
	sender    *temporpc.JSONRPCResponse
	sponsored *temporpc.JSONRPCResponse
	wantErr   bool
}

func runSponsoredSimulationCase(t *testing.T, tt sponsoredSimulationCase, config func(url string) IntentConfig) {
	t.Helper()
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
	rpc.onCall = answerSimulations(tt.sender, tt.sponsored)
	_, sponsor := newFakeHostedFeePayer(t, 42431, common.HexToAddress("0x20c0000000000000000000000000000000000002"))
	cfg := config(sponsor.URL)
	cfg.RPC, cfg.Store = rpc, tempo.NewMemoryStore()
	intent, err := NewIntent(cfg)
	require.NoError(t, err)

	_, err = intent.Verify(ctx, credential, request.Map())

	// The sender's simulation is the gas-bounded check: it runs at the
	// broadcast gas limit and charges the sender no fee.
	require.NotEmpty(t, rpc.callRequests)
	senderSimulation := rpc.callRequests[0]
	assert.NotContains(t, senderSimulation, "feePayer")
	assert.Equal(t, hexutil.EncodeUint64(client.Gas), senderSimulation["gas"])
	assert.NotContains(t, senderSimulation, "maxFeePerGas")
	assert.NotContains(t, senderSimulation, "maxPriorityFeePerGas")
	assert.NotContains(t, senderSimulation, "feeToken")
	assert.Equal(t, hexutil.EncodeUint64(client.Nonce), senderSimulation["nonce"])
	if tt.wantErr {
		require.ErrorContains(t, err, "transaction preflight failed")
		assert.Empty(t, rpc.sentRawTxs)
		return
	}
	require.NoError(t, err)
	require.Len(t, rpc.sentRawTxs, 1)
	broadcast, _, err := deserializeTransactionCredential(rpc.sentRawTxs[0])
	require.NoError(t, err)
	assert.Equal(t, client.Gas, broadcast.Gas)
}

// A transaction whose gas limit is too low fails the sender's simulation at
// the broadcast gas limit and is never broadcast, though the local fee
// payer's padded sponsored simulation alone would pass.
func TestSponsoredSimulationRejectsLowGas(t *testing.T) {
	for name, config := range map[string]func(string) IntentConfig{
		"local":  func(string) IntentConfig { return IntentConfig{FeePayerPrivateKey: feePayerKey} },
		"hosted": func(url string) IntentConfig { return IntentConfig{FeePayerURL: url} },
	} {
		t.Run(name, func(t *testing.T) {
			runSponsoredSimulationCase(t, sponsoredSimulationCase{sender: outOfGas, wantErr: true}, config)
		})
	}
}

// A hosted fee payer's signature binds the gas limit, so the sponsored
// simulation runs at the broadcast gas and may run out of gas only because
// a sponsored eth_call charges more than the mined transaction: that alone
// passes, after the sender's gas-bounded simulation did. A fee token balance
// error or a revert still rejects.
func TestHostedFeePayerSponsoredSimulationToleratesOnlyOutOfGas(t *testing.T) {
	hosted := func(url string) IntentConfig { return IntentConfig{FeePayerURL: url} }
	for _, tt := range []sponsoredSimulationCase{
		{name: "out of gas", sponsored: outOfGas},
		{name: "intrinsic gas too low", sponsored: temporpc.NewJSONRPCErrorResponse(1, -32000, "intrinsic gas too low", nil)},
		{name: "insufficient fee token balance", sponsored: feeBalance, wantErr: true},
		{name: "reverted", sponsored: reverted, wantErr: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			runSponsoredSimulationCase(t, tt, hosted)
		})
	}
}

// A local fee payer's sponsored simulation gets the full gas buffer, so
// running out of gas there still rejects.
func TestLocalFeePayerPaddedSimulationOutOfGasRejects(t *testing.T) {
	runSponsoredSimulationCase(t, sponsoredSimulationCase{sponsored: outOfGas, wantErr: true}, func(string) IntentConfig {
		return IntentConfig{FeePayerPrivateKey: feePayerKey}
	})
}

func TestIsOutOfGasError(t *testing.T) {
	assert.True(t, isOutOfGasError(outOfGas.Error))
	assert.True(t, isOutOfGasError(temporpc.NewJSONRPCErrorResponse(1, -32000, "intrinsic gas too low", nil).Error))
	assert.False(t, isOutOfGasError(feeBalance.Error))
	assert.False(t, isOutOfGasError(reverted.Error))
	assert.False(t, isOutOfGasError(errors.New("out of gas")), "only a JSON-RPC error from the node counts")
}
