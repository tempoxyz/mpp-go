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
	// TransferCalldataLength is the exact byte length of transfer(address,uint256).
	TransferCalldataLength = 4 + 32 + 32
	// TransferWithMemoCalldataLength is the exact byte length of transferWithMemo(address,uint256,bytes32).
	TransferWithMemoCalldataLength = 4 + 32 + 32 + 32
	// MainnetUSDCAddress is Circle's USDC contract on Tempo mainnet.
	MainnetUSDCAddress = "0x20C000000000000000000000b9537d11c60E8b50"
	// OUSDAddress is the OpenUSD (OUSD) contract, deployed at the same address
	// on Tempo mainnet and Moderato.
	OUSDAddress = "0x20c0000000000000000000006a37DA5C996874BE"
	// PathUSDAddress is the pathUSD contract on Tempo networks.
	PathUSDAddress = "0x20c0000000000000000000000000000000000000"
)
