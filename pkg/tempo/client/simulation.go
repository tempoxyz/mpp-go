package chargeclient

import (
	"errors"
	"fmt"
	"math/big"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	temporpc "github.com/tempoxyz/tempo-go/pkg/client"
)

// Revert selectors a pre-signing simulation can hit: the AccountKeychain's
// SpendingLimitExceeded() and TIP-20's
// InsufficientBalance(uint256,uint256,address).
const (
	spendingLimitExceededSelector = "8a9e71ea"
	insufficientBalanceSelector   = "832f98b5"
)

// SpendingLimitExceededError reports a payment that the access key signing it
// has too little spending limit left for, as mppx's
// Errors.SpendingLimitExceededError. The client simulates a payment (estimates
// its gas as the signing key) before it signs; the transaction would revert on
// chain with the AccountKeychain's SpendingLimitExceeded(), and a fee payer
// would still pay for it, so the client does not sign it. Raise the key's
// limit, wait for its period to reset, or pay with another key. Reach it with
// errors.As.
type SpendingLimitExceededError struct {
	// Account is the account that pays.
	Account common.Address
	// AccessKey is the access key that signs the payment, or nil when the
	// account signs for itself.
	AccessKey *common.Address
	// Err is the simulation error.
	Err error
}

func (e *SpendingLimitExceededError) Error() string {
	key := ""
	if e.AccessKey != nil {
		key = "key " + e.AccessKey.Hex() + " of "
	}
	return fmt.Sprintf("tempo client: access key spending limit exceeded: %saccount %s cannot pay this amount, so the payment was not signed", key, e.Account.Hex())
}

// Unwrap returns the simulation error.
func (e *SpendingLimitExceededError) Unwrap() error {
	return e.Err
}

// InsufficientBalanceError reports a payment whose account holds too little of
// a token, found by simulating the payment before signing it, as mppx's
// Errors.InsufficientBalanceError from the charge client. The transaction
// would revert on chain with TIP-20's InsufficientBalance, so the client does
// not sign it. Reach it with errors.As.
type InsufficientBalanceError struct {
	// Account is the account that pays.
	Account common.Address
	// Token is the TIP-20 token the account holds too little of.
	Token common.Address
	// Available is the account's balance of Token.
	Available *big.Int
	// Required is the amount of Token the payment needs.
	Required *big.Int
	// Err is the simulation error.
	Err error
}

func (e *InsufficientBalanceError) Error() string {
	return fmt.Sprintf("tempo client: insufficient balance: %s holds %s of token %s, but the payment requires %s", e.Account.Hex(), e.Available, e.Token.Hex(), e.Required)
}

// Unwrap returns the simulation error.
func (e *InsufficientBalanceError) Unwrap() error {
	return e.Err
}

// simulationError maps a failed pre-signing simulation (eth_estimateGas) to a
// SpendingLimitExceededError or InsufficientBalanceError when the node's
// error carries that revert, as mppx's toClientError does, and returns nil
// for any other error.
func simulationError(err error, account common.Address, accessKey *common.Address) error {
	data := revertData(err)
	if len(data) < 4 {
		return nil
	}
	switch common.Bytes2Hex(data[:4]) {
	case spendingLimitExceededSelector:
		return &SpendingLimitExceededError{Account: account, AccessKey: accessKey, Err: err}
	case insufficientBalanceSelector:
		// InsufficientBalance(uint256 available, uint256 required, address token).
		if len(data) < 4+96 {
			return nil
		}
		args := data[4:]
		return &InsufficientBalanceError{
			Account:   account,
			Token:     common.BytesToAddress(args[64:96]),
			Available: new(big.Int).SetBytes(args[:32]),
			Required:  new(big.Int).SetBytes(args[32:64]),
			Err:       err,
		}
	default:
		return nil
	}
}

// revertData returns the revert data of the JSON-RPC error in err's chain:
// its data field, or that field's own data field, as mppx's getRevertData
// reads it.
func revertData(err error) []byte {
	var rpcErr *temporpc.JSONRPCError
	if !errors.As(err, &rpcErr) || rpcErr == nil {
		return nil
	}
	if data := decodeRevertData(rpcErr.Data); data != nil {
		return data
	}
	if nested, ok := rpcErr.Data.(map[string]any); ok {
		return decodeRevertData(nested["data"])
	}
	return nil
}

func decodeRevertData(value any) []byte {
	text, ok := value.(string)
	if !ok || !strings.HasPrefix(text, "0x") {
		return nil
	}
	data, err := hexutil.Decode(text)
	if err != nil || len(data) < 4 {
		return nil
	}
	return data
}
