package temposim

import (
	"context"
	"fmt"
	"math/big"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/tempoxyz/mpp-go/pkg/tempo"
	temporpc "github.com/tempoxyz/tempo-go/pkg/client"
	tempotx "github.com/tempoxyz/tempo-go/pkg/transaction"
)

const (
	// ChainID is Tempo Moderato, the test network used in examples.
	ChainID int64 = 42431
	// PayerPrivateKey is a fixed demo key for the example client.
	PayerPrivateKey = "0x59c6995e998f97a5a0044966f0945389dc9e86dae88c7a8412f4603b6b78690d"
	// FeePayerPrivateKey is the demo co-signer key for sponsored examples.
	FeePayerPrivateKey = "0xdd83cd66cd98801a07e0b7c1a5b02364b369e696da7c0ab444acffea5cca86fc"
	// Currency is a sample TIP-20 token address used across the examples.
	Currency = "0x20c0000000000000000000000000000000000001"
	// Recipient is the payment recipient used across the examples.
	Recipient = "0x70997970c51812dc3a010c7d01b50e0d17dc79c8"
	// Realm is the host string embedded in example Challenges.
	Realm = "api.example.com"
	// ReceiptHash is the deterministic hash returned by the mock RPC.
	ReceiptHash = "0xabc123"
)

// RPC is a lightweight Tempo RPC stub that makes the examples runnable without a devnet.
type RPC struct {
	chainID     uint64
	nonce       uint64
	gasPrice    string
	estimateGas string
	request     tempo.ChargeRequest
	receipts    map[string]map[string]any
	sentRawTxs  []string
}

// NewRPC constructs a demo RPC client that synthesizes successful transfer receipts.
func NewRPC(request tempo.ChargeRequest) *RPC {
	return &RPC{
		chainID:     uint64(ChainID),
		nonce:       7,
		gasPrice:    "0x1",
		estimateGas: "0x5208",
		request:     request,
		receipts:    map[string]map[string]any{},
	}
}

func (r *RPC) GetChainID(context.Context) (uint64, error) {
	return r.chainID, nil
}

func (r *RPC) GetTransactionCount(context.Context, string) (uint64, error) {
	return r.nonce, nil
}

func (r *RPC) SendRawTransaction(_ context.Context, serialized string) (string, error) {
	r.sentRawTxs = append(r.sentRawTxs, serialized)
	tx, err := tempotx.Deserialize(serialized)
	if err != nil {
		return "", err
	}
	sender, err := tempotx.VerifySignature(tx)
	if err != nil {
		return "", err
	}
	r.receipts[ReceiptHash] = buildReceipt(serialized, r.request, sender)
	return ReceiptHash, nil
}

func (r *RPC) SendRequest(_ context.Context, method string, params ...interface{}) (*temporpc.JSONRPCResponse, error) {
	switch method {
	case "eth_gasPrice":
		return &temporpc.JSONRPCResponse{Result: r.gasPrice}, nil
	case "eth_estimateGas":
		return &temporpc.JSONRPCResponse{Result: r.estimateGas}, nil
	case "eth_getTransactionReceipt":
		hash := params[0].(string)
		return &temporpc.JSONRPCResponse{Result: r.receipts[hash]}, nil
	default:
		return nil, fmt.Errorf("unexpected rpc method %q", method)
	}
}

func buildReceipt(raw string, request tempo.ChargeRequest, sender common.Address) map[string]any {
	tx, _ := tempotx.Deserialize(raw)
	callData := common.Bytes2Hex(tx.Calls[0].Data)
	amount, _ := new(big.Int).SetString(request.Amount, 10)
	topics := []any{
		tempo.TransferWithMemoTopic.Hex(),
		addressTopic(sender.Hex()),
		addressTopic(request.Recipient),
		"0x" + callData[136:200],
	}
	return map[string]any{
		"status": "0x1",
		"logs": []any{
			map[string]any{
				"address": request.Currency,
				"topics":  topics,
				"data":    fmt.Sprintf("0x%064x", amount),
			},
		},
	}
}

func addressTopic(address string) string {
	return fmt.Sprintf("0x%064s", strings.TrimPrefix(strings.ToLower(address), "0x"))
}
