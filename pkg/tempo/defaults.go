package tempo

import (
	"fmt"
	"net/url"
	"strings"

	tempotx "github.com/tempoxyz/tempo-go/pkg/transaction"
	"math/big"
)

// TODO(tempo-go): promote the expiring nonce key and chain default lookup
// helpers below into tempo-go so SDKs share one source of Tempo defaults.

// ExpiringNonceKey is the reserved nonce key used for fee-payer transactions.
var ExpiringNonceKey = new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 256), big.NewInt(1))

// RPCURLForChain returns the canonical Tempo RPC URL for a known chain ID.
// Chain ID 0 falls back to mainnet defaults.
func RPCURLForChain(chainID int64) (string, error) {
	switch chainID {
	case 0, tempotx.ChainIdMainnet:
		return tempotx.RpcUrlMainnet, nil
	case tempotx.ChainIdModerato:
		return tempotx.RpcUrlModerato, nil
	default:
		return "", fmt.Errorf("unknown chain id %d", chainID)
	}
}

// DefaultRPCURLForChain returns the canonical Tempo RPC URL for a chain ID.
func DefaultRPCURLForChain(chainID int64) string {
	if rpcURL, err := RPCURLForChain(chainID); err == nil {
		return rpcURL
	}
	return tempotx.RpcUrlMainnet
}

// InferChainIDFromRPCURL maps known Tempo RPC URLs back to their chain IDs.
// It returns 0 when the URL does not match a known Tempo endpoint.
func InferChainIDFromRPCURL(rpcURL string) int64 {
	normalized := normalizeRPCURL(rpcURL)
	switch normalized {
	case normalizeRPCURL(tempotx.RpcUrlModerato):
		return tempotx.ChainIdModerato
	case normalizeRPCURL(tempotx.RpcUrlMainnet):
		return tempotx.ChainIdMainnet
	}
	if normalized == "" {
		return 0
	}
	parsed, err := url.Parse(strings.TrimSpace(rpcURL))
	if err != nil {
		return 0
	}
	switch strings.ToLower(parsed.Hostname()) {
	case "rpc.moderato.tempo.xyz", "rpc.testnet.tempo.xyz":
		return tempotx.ChainIdModerato
	case "rpc.tempo.xyz":
		return tempotx.ChainIdMainnet
	default:
		return 0
	}
}

func normalizeRPCURL(rpcURL string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(rpcURL)), "/")
}

// IsKnownChainID reports whether the chain ID maps to a built-in Tempo network.
func IsKnownChainID(chainID int64) bool {
	switch chainID {
	case tempotx.ChainIdMainnet, tempotx.ChainIdModerato:
		return true
	default:
		return false
	}
}

// DefaultCurrencyForChain returns the default stablecoin used for charges on a chain.
func DefaultCurrencyForChain(chainID int64) string {
	switch chainID {
	case tempotx.ChainIdMainnet:
		return MainnetUSDCAddress
	default:
		return tempotx.AlphaUSDAddress.Hex()
	}
}
