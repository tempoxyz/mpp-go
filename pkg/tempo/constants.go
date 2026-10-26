package tempo

import (
	"time"

	"github.com/tempoxyz/mpp-go/pkg/mpp"
)

// TODO(tempo-go): move Tempo chain metadata and TIP-20 selectors into tempo-go;
// these constants are chain-specific primitives rather than MPP-specific state.

// Tempo chain IDs. They have the type of the ChainID fields in the Tempo
// client and server configs.
const (
	// ChainIDMainnet is the chain ID of Tempo mainnet.
	ChainIDMainnet int64 = 4217
	// ChainIDModerato is the chain ID of the Tempo Moderato testnet.
	ChainIDModerato int64 = 42431
)

const (
	// MethodName is the Tempo payment method token.
	MethodName = "tempo"
	// IntentCharge is the Tempo charge intent token. It is mpp.IntentCharge.
	IntentCharge = mpp.IntentCharge
	// DefaultDecimals is the default TIP-20 decimal precision used for charges.
	DefaultDecimals = 6
	// DefaultGasLimit is the fallback gas limit when estimation is unavailable.
	DefaultGasLimit = uint64(1_000_000)
	// SponsoredValidBeforeWindow is how far ahead of now a client sets the
	// expiring-nonce valid_before of a sponsored (fee-payer) charge
	// transaction, capped at FeePayerMinRemainingValidity past the second
	// after the challenge expiry. Servers require FeePayerMinRemainingValidity
	// of validity left when they verify, co-sign and broadcast, so a 60 second
	// window leaves about 45 seconds for clock skew between client and server,
	// signing and round trips. This is deliberately wider than mppx's 25
	// seconds, which leaves only about 10.
	SponsoredValidBeforeWindow = 60 * time.Second
	// FeePayerMinRemainingValidity is the minimum validity a sponsored charge
	// must keep: servers reject sponsored transactions with less remaining
	// before valid_before, refuse to issue sponsored challenges that expire
	// sooner than this after issuance, and clients refuse to sign sponsored
	// challenges that expire sooner than this.
	FeePayerMinRemainingValidity = 15 * time.Second
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
	// USDCEAddress is the bridged USDC (USDC.e) contract on Tempo mainnet.
	USDCEAddress = "0x20C000000000000000000000b9537d11c60E8b50"
	// OUSDAddress is the OpenUSD (OUSD) contract, deployed at the same address
	// on Tempo mainnet and Moderato.
	OUSDAddress = "0x20c0000000000000000000006a37DA5C996874BE"
	// PathUSDAddress is the pathUSD contract on Tempo networks.
	PathUSDAddress = "0x20c0000000000000000000000000000000000000"
)
