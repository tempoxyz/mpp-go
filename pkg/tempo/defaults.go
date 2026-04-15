package tempo

import (
	"math/big"
	"time"

	"github.com/ethereum/go-ethereum/common"
	tempotx "github.com/tempoxyz/tempo-go/pkg/transaction"
)

const (
	// MethodName is the Tempo payment method token.
	MethodName = "tempo"
	// IntentCharge is the Tempo charge intent token.
	IntentCharge = "charge"
	// DefaultDecimals is the default TIP-20 decimal precision used for charges.
	DefaultDecimals = 6
	// DefaultGasLimit is the fallback gas limit when estimation is unavailable.
	DefaultGasLimit = uint64(1_000_000)
	// FeePayerWindow is the client-side validity window used for sponsored charges.
	FeePayerWindow = 25 * time.Second
	// ReplayKeyPrefix is the storage namespace for charge replay protection.
	ReplayKeyPrefix = "mpp:charge:"
	// TransferSelector is the TIP-20 transfer selector.
	TransferSelector = "a9059cbb"
	// TransferWithMemoSelector is the Tempo transfer-with-memo selector.
	TransferWithMemoSelector = "95777d59"
)

// ExpiringNonceKey is the reserved nonce key used for fee-payer transactions.
var ExpiringNonceKey = new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 256), big.NewInt(1))

// mainnetUSDCAddress is Circle's USDC contract on Tempo mainnet.
var mainnetUSDCAddress = common.HexToAddress("0x20C000000000000000000000b9537d11c60E8b50")

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
		return mainnetUSDCAddress.Hex()
	default:
		return tempotx.AlphaUSDAddress.Hex()
	}
}
