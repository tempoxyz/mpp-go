package tempo

import (
	"encoding/hex"
	"fmt"
	"math/big"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

var (
	// TransferTopic is the TIP-20 Transfer event topic hash.
	TransferTopic = crypto.Keccak256Hash([]byte("Transfer(address,address,uint256)"))
	// TransferWithMemoTopic is the Tempo TransferWithMemo event topic hash.
	TransferWithMemoTopic = crypto.Keccak256Hash([]byte("TransferWithMemo(address,address,uint256,bytes32)"))
)

// TODO(tempo-go): move shared TIP-20 transfer encoding and calldata/log matching
// helpers into tempo-go. Both mpp-go and pympp now carry nearly identical
// Tempo-specific logic here.

// EncodeTransfer builds TIP-20 transfer calldata for the supplied recipient and amount.
func EncodeTransfer(recipient string, amount *big.Int) string {
	return fmt.Sprintf("0x%s%s%s", TransferSelector, padAddress(recipient), padBigInt(amount))
}

// EncodeTransferWithMemo builds Tempo transfer-with-memo calldata for the supplied values.
func EncodeTransferWithMemo(recipient string, amount *big.Int, memo string) (string, error) {
	memo = strings.TrimPrefix(strings.ToLower(memo), "0x")
	if len(memo) != 64 {
		return "", fmt.Errorf("tempo: memo must be exactly 32 bytes")
	}
	if _, err := hex.DecodeString(memo); err != nil {
		return "", fmt.Errorf("tempo: memo must be hex encoded")
	}
	return fmt.Sprintf("0x%s%s%s%s", TransferWithMemoSelector, padAddress(recipient), padBigInt(amount), memo), nil
}

// MatchTransferCalldata reports whether calldata satisfies the canonical
// Tempo charge transfer: a transfer of request.Amount to request.Recipient.
// When request.MethodDetails.Memo is set, it must be a transferWithMemo
// carrying exactly that memo. Otherwise the memo is optional: a plain
// transfer or any memo is accepted, except an attribution memo for another
// realm or challenge.
func MatchTransferCalldata(dataHex string, request ChargeRequest, realm, challengeID string) bool {
	dataHex = strings.TrimPrefix(strings.ToLower(dataHex), "0x")
	if len(dataHex) < 8 {
		return false
	}
	selector := dataHex[:8]
	switch {
	case selector == TransferWithMemoSelector && len(dataHex) == TransferWithMemoCalldataLength*2:
	case selector == TransferSelector && len(dataHex) == TransferCalldataLength*2:
	default:
		return false
	}
	// Every field must be hex: the memo is compared as text below.
	if _, err := hex.DecodeString(dataHex); err != nil {
		return false
	}
	toAddress := "0x" + dataHex[8+24:8+64]
	amount, ok := new(big.Int).SetString(dataHex[72:136], 16)
	if !ok {
		return false
	}
	if !strings.EqualFold(toAddress, request.Recipient) || amount.String() != request.Amount {
		return false
	}
	if selector == TransferSelector {
		return request.MethodDetails.Memo == ""
	}
	memo := "0x" + dataHex[136:200]
	if request.MethodDetails.Memo != "" {
		return strings.EqualFold(memo, request.MethodDetails.Memo)
	}
	if IsAttributionMemo(memo) {
		return VerifyAttributionServer(memo, realm) && VerifyAttributionChallenge(memo, challengeID)
	}
	return true
}

func padAddress(value string) string {
	return fmt.Sprintf("%064s", strings.TrimPrefix(strings.ToLower(value), "0x"))
}

func padBigInt(value *big.Int) string {
	if value == nil {
		return strings.Repeat("0", 64)
	}
	return fmt.Sprintf("%064s", value.Text(16))
}

// ParseTopicAddress extracts the address stored in an indexed log topic.
func ParseTopicAddress(topic string) string {
	topic = strings.TrimPrefix(strings.ToLower(topic), "0x")
	if len(topic) < 40 {
		return ""
	}
	return common.HexToAddress("0x" + topic[len(topic)-40:]).Hex()
}
