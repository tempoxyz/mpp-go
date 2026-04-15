package tempo

import (
	tempotx "github.com/tempoxyz/tempo-go/pkg/transaction"
	"math/big"
)

// TODO(tempo-go): promote the expiring nonce key and chain default lookup
// helpers below into tempo-go so SDKs share one source of Tempo defaults.

// ExpiringNonceKey is the reserved nonce key used for fee-payer transactions.
var ExpiringNonceKey = new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 256), big.NewInt(1))

// DefaultRPCURLForChain returns the canonical Tempo RPC URL for a chain ID.
func DefaultRPCURLForChain(chainID int64) string {
	switch chainID {
	case tempotx.ChainIdModerato:
		return tempotx.RpcUrlModerato
	case tempotx.ChainIdMainnet:
		fallthrough
	default:
		return tempotx.RpcUrlMainnet
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
