package tempo

import "time"

// TODO(tempo-go): move Tempo chain metadata and TIP-20 selectors into tempo-go;
// these constants are chain-specific primitives rather than MPP-specific state.

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
	ReplayKeyPrefix = "mppx:charge:"
	// TransferSelector is the TIP-20 transfer selector.
	TransferSelector = "a9059cbb"
	// TransferWithMemoSelector is the Tempo transfer-with-memo selector.
	TransferWithMemoSelector = "95777d59"
	// MainnetUSDCAddress is Circle's USDC contract on Tempo mainnet.
	MainnetUSDCAddress = "0x20C000000000000000000000b9537d11c60E8b50"
)
