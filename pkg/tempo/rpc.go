package tempo

import (
	"context"
	"fmt"
	"math/big"
	"strconv"
	"strings"

	temporpc "github.com/tempoxyz/tempo-go/pkg/client"
)

// RPCClient is the subset of Tempo JSON-RPC used by the charge flow.
type RPCClient interface {
	GetChainID(ctx context.Context) (uint64, error)
	GetTransactionCount(ctx context.Context, address string) (uint64, error)
	SendRawTransaction(ctx context.Context, serializedTx string) (string, error)
	SendRequest(ctx context.Context, method string, params ...interface{}) (*temporpc.JSONRPCResponse, error)
}

// TODO(tempo-go): promote higher-level tx-param, gas-estimation, and
// receipt-polling helpers into tempo-go/client; mpp-go and pympp currently
// duplicate this Tempo RPC glue.

// NewRPCClient constructs a Tempo JSON-RPC client.
func NewRPCClient(rpcURL string) RPCClient {
	return temporpc.New(rpcURL)
}

// ParseHexUint64 decodes a JSON-RPC hex integer into uint64. A bare "0x" (or an
// empty string) decodes to 0, matching ParseHexBigInt; some JSON-RPC nodes
// return "0x" for a zero-valued quantity.
func ParseHexUint64(value string) (uint64, error) {
	trimmed := strings.TrimPrefix(value, "0x")
	if trimmed == "" {
		return 0, nil
	}
	return strconv.ParseUint(trimmed, 16, 64)
}

// ParseHexBigInt decodes a JSON-RPC hex integer into a big.Int.
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
