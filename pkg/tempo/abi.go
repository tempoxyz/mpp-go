package tempo

import (
	"fmt"
	"math/big"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

var (
	TransferTopic         = crypto.Keccak256Hash([]byte("Transfer(address,address,uint256)"))
	TransferWithMemoTopic = crypto.Keccak256Hash([]byte("TransferWithMemo(address,address,uint256,bytes32)"))
)

// TODO: Move shared TIP-20 transfer encoding and calldata/log matching helpers into tempo-go.
// Both mpp-go and pympp now carry nearly identical Tempo-specific logic here.

func EncodeTransfer(recipient string, amount *big.Int) string {
	return fmt.Sprintf("0x%s%s%s", TransferSelector, padAddress(recipient), padBigInt(amount))
}

func EncodeTransferWithMemo(recipient string, amount *big.Int, memo string) (string, error) {
	memo = strings.TrimPrefix(strings.ToLower(memo), "0x")
	if len(memo) != 64 {
		return "", fmt.Errorf("tempo: memo must be exactly 32 bytes")
	}
	return fmt.Sprintf("0x%s%s%s%s", TransferWithMemoSelector, padAddress(recipient), padBigInt(amount), memo), nil
}

func MatchTransferCalldata(dataHex string, request ChargeRequest, realm, challengeID string) bool {
	dataHex = strings.TrimPrefix(strings.ToLower(dataHex), "0x")
	if len(dataHex) < 8+64+64 {
		return false
	}
	selector := dataHex[:8]
	toAddress := "0x" + dataHex[8+24:8+64]
	amount := new(big.Int)
	amount.SetString(dataHex[72:136], 16)
	if !strings.EqualFold(toAddress, request.Recipient) || amount.String() != request.Amount {
		return false
	}
	expectedMemo := request.MethodDetails.Memo
	switch selector {
	case TransferSelector:
		return false
	case TransferWithMemoSelector:
		if len(dataHex) < 8+64+64+64 {
			return false
		}
		memo := "0x" + dataHex[136:200]
		if expectedMemo != "" {
			return strings.EqualFold(memo, expectedMemo)
		}
		return VerifyAttributionServer(memo, realm) && VerifyAttributionChallenge(memo, challengeID)
	default:
		return false
	}
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

func ParseTopicAddress(topic string) string {
	topic = strings.TrimPrefix(strings.ToLower(topic), "0x")
	if len(topic) < 40 {
		return ""
	}
	return common.HexToAddress("0x" + topic[len(topic)-40:]).Hex()
}
