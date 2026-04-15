package tempo

import (
	"context"
	"fmt"
	"math/big"
	"strconv"
	"strings"

	temporpc "github.com/tempoxyz/tempo-go/pkg/client"
)

type RPCClient interface {
	GetChainID(ctx context.Context) (uint64, error)
	GetTransactionCount(ctx context.Context, address string) (uint64, error)
	SendRawTransaction(ctx context.Context, serializedTx string) (string, error)
	SendRequest(ctx context.Context, method string, params ...interface{}) (*temporpc.JSONRPCResponse, error)
}

// TODO: Promote higher-level tx-param, gas-estimation, and receipt-polling helpers into
// tempo-go/client; mpp-go and pympp currently duplicate this Tempo RPC glue.

func NewRPCClient(rpcURL string) RPCClient {
	return temporpc.New(rpcURL)
}

func ParseHexUint64(value string) (uint64, error) {
	return strconv.ParseUint(strings.TrimPrefix(value, "0x"), 16, 64)
}

func ParseHexBigInt(value string) (*big.Int, error) {
	trimmed := strings.TrimPrefix(value, "0x")
	if trimmed == "" {
		return big.NewInt(0), nil
	}
	parsed, ok := new(big.Int).SetString(trimmed, 16)
	if !ok {
		return nil, fmt.Errorf("tempo: invalid hex integer %q", value)
	}
	return parsed, nil
}
