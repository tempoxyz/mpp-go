package tempo

import (
	"math/big"
	"time"

	"github.com/ethereum/go-ethereum/common"
	tempotx "github.com/tempoxyz/tempo-go/pkg/transaction"
)

const (
	MethodName               = "tempo"
	IntentCharge             = "charge"
	DefaultDecimals          = 6
	DefaultGasLimit          = uint64(1_000_000)
	FeePayerWindow           = 25 * time.Second
	ReplayKeyPrefix          = "mpp:charge:"
	TransferSelector         = "a9059cbb"
	TransferWithMemoSelector = "95777d59"
)

var ExpiringNonceKey = new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 256), big.NewInt(1))

var mainnetUSDCAddress = common.HexToAddress("0x20C000000000000000000000b9537d11c60E8b50")

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

func DefaultCurrencyForChain(chainID int64) string {
	switch chainID {
	case tempotx.ChainIdMainnet:
		return mainnetUSDCAddress.Hex()
	default:
		return tempotx.AlphaUSDAddress.Hex()
	}
}
