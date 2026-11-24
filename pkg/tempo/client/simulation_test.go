package chargeclient

import (
	"context"
	"errors"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/require"
	temporpc "github.com/tempoxyz/tempo-go/pkg/client"
	temposigner "github.com/tempoxyz/tempo-go/pkg/signer"

	"github.com/tempoxyz/mpp-go/pkg/mpp"
	"github.com/tempoxyz/mpp-go/pkg/tempo"
)

const (
	// SpendingLimitExceeded(), as the AccountKeychain reverts.
	testSpendingLimitExceeded = "0x8a9e71ea"
)

// testInsufficientBalance is TIP-20's
// InsufficientBalance(available, required, token) revert data.
func testInsufficientBalance(available, required int64, token string) string {
	word := func(value []byte) string {
		return common.Bytes2Hex(common.LeftPadBytes(value, 32))
	}
	return "0x832f98b5" +
		word(big.NewInt(available).Bytes()) +
		word(big.NewInt(required).Bytes()) +
		word(common.HexToAddress(token).Bytes())
}

// revertingEstimateRPC answers eth_estimateGas with an execution revert
// carrying data, and records the estimate requests.
type revertingEstimateRPC struct {
	*mockRPC
	data      any
	estimates []map[string]any
}

func (r *revertingEstimateRPC) SendRequest(ctx context.Context, method string, params ...interface{}) (*temporpc.JSONRPCResponse, error) {
	if method == "eth_estimateGas" {
		if len(params) > 0 {
			if request, ok := params[0].(map[string]any); ok {
				r.estimates = append(r.estimates, request)
			}
		}
		return &temporpc.JSONRPCResponse{Error: &temporpc.JSONRPCError{Code: 3, Message: "execution reverted", Data: r.data}}, nil
	}
	return r.mockRPC.SendRequest(ctx, method, params...)
}

func TestSponsoredChargeStopsOnSpendingLimitRevertBeforeSigning(t *testing.T) {
	t.Parallel()

	root, err := temposigner.NewSigner(testPrivateKey)
	require.NoError(t, err)
	key, err := temposigner.NewSigner("0x5de4111afa1a4b94908f83103eb1f1706367c2e68ca870fc3fb9a804cdab365a")
	require.NoError(t, err)
	rpc := &revertingEstimateRPC{mockRPC: &mockRPC{chainID: 42431, gasPrice: "0x1"}, data: testSpendingLimitExceeded}
	method, err := New(Config{
		AccessKey:     &AccessKey{Signer: key, Account: root.Address()},
		RPC:           rpc,
		ChainID:       42431,
		PaymentPolicy: func(context.Context, tempo.ChargeRequest) error { return nil },
	})
	require.NoError(t, err)

	credential, err := method.CreateCredential(context.Background(), sponsoredChallenge(t, mpp.ExpiresIn(5*time.Minute)))
	require.Nil(t, credential)
	var limitErr *SpendingLimitExceededError
	require.ErrorAs(t, err, &limitErr)
	require.Equal(t, root.Address(), limitErr.Account)
	require.NotNil(t, limitErr.AccessKey)
	require.Equal(t, key.Address(), *limitErr.AccessKey)
	var rpcErr *temporpc.JSONRPCError
	require.True(t, errors.As(err, &rpcErr), "the simulation error stays reachable")
	require.Contains(t, err.Error(), "spending limit exceeded")

	// The estimate names the signing key, so the node checks its limits.
	require.Len(t, rpc.estimates, 1)
	require.Equal(t, key.Address().Hex(), rpc.estimates[0]["keyId"])
	require.Equal(t, root.Address().Hex(), rpc.estimates[0]["from"])
}

func TestPushChargeStopsOnInsufficientBalanceRevertBeforeBroadcast(t *testing.T) {
	t.Parallel()

	rpc := &revertingEstimateRPC{
		mockRPC: &mockRPC{chainID: 42431, gasPrice: "0x1"},
		// Some nodes nest the revert data one level down.
		data: map[string]any{"data": testInsufficientBalance(487, 500_000, testCurrency)},
	}
	method, err := New(Config{
		PrivateKey: testPrivateKey, RPC: rpc, ChainID: 42431, CredentialType: tempo.CredentialTypeHash,
		PaymentPolicy: func(context.Context, tempo.ChargeRequest) error { return nil },
	})
	require.NoError(t, err)

	challenge := buildChallenge(t, tempo.ChargeRequestParams{Amount: "0.50", Currency: testCurrency, Recipient: testRecipient, Decimals: 6, ChainID: 42431})
	_, err = method.CreateCredential(context.Background(), challenge)
	var balanceErr *InsufficientBalanceError
	require.ErrorAs(t, err, &balanceErr)
	require.Equal(t, method.account(), balanceErr.Account)
	require.Equal(t, common.HexToAddress(testCurrency), balanceErr.Token)
	require.Equal(t, int64(487), balanceErr.Available.Int64())
	require.Equal(t, int64(500_000), balanceErr.Required.Int64())
	require.Contains(t, err.Error(), "holds 487 of token")
	require.Contains(t, err.Error(), "requires 500000")
	require.Empty(t, rpc.sentRawTxs)
	// The account signs for itself: no keyId.
	require.Len(t, rpc.estimates, 1)
	require.NotContains(t, rpc.estimates[0], "keyId")
}

func TestChargeKeepsDefaultGasOnOtherEstimateFailures(t *testing.T) {
	t.Parallel()

	rpc := &revertingEstimateRPC{mockRPC: &mockRPC{chainID: 42431, gasPrice: "0x1"}, data: "0xdeadbeef"}
	method, err := New(Config{
		PrivateKey: testPrivateKey, RPC: rpc, ChainID: 42431,
		PaymentPolicy: func(context.Context, tempo.ChargeRequest) error { return nil },
	})
	require.NoError(t, err)

	credential, err := method.CreateCredential(context.Background(), sponsoredChallenge(t, mpp.ExpiresIn(5*time.Minute)))
	require.NoError(t, err)
	signature, _ := credential.Payload["signature"].(string)
	tx, err := deserializeTestTx(signature)
	require.NoError(t, err)
	require.Equal(t, tempo.DefaultGasLimit, tx.Gas)
}

func TestSimulationError(t *testing.T) {
	t.Parallel()

	account := common.HexToAddress(testRecipient)
	revert := func(data any) error {
		return &temporpc.JSONRPCError{Code: 3, Message: "execution reverted", Data: data}
	}

	require.Nil(t, simulationError(errors.New("boom"), account, nil))
	require.Nil(t, simulationError(revert("0xdeadbeef"), account, nil))
	require.Nil(t, simulationError(revert(nil), account, nil))
	// Too short to hold InsufficientBalance's arguments.
	require.Nil(t, simulationError(revert("0x832f98b5"+strings.Repeat("00", 32)), account, nil))

	var limitErr *SpendingLimitExceededError
	require.ErrorAs(t, simulationError(revert(testSpendingLimitExceeded), account, nil), &limitErr)
	require.Nil(t, limitErr.AccessKey)
	require.NotContains(t, limitErr.Error(), "key 0x")
}
