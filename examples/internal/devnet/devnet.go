package devnet

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/tempoxyz/mpp-go/pkg/tempo"
)

const (
	defaultRPCURL = "http://127.0.0.1:8545"

	PayerPrivateKey    = "0x59c6995e998f97a5a0044966f0945389dc9e86dae88c7a8412f4603b6b78690d"
	FeePayerPrivateKey = "0xdd83cd66cd98801a07e0b7c1a5b02364b369e696da7c0ab444acffea5cca86fc"
	Currency           = "0x20c0000000000000000000000000000000000000"
	Recipient          = "0x70997970c51812dc3a010c7d01b50e0d17dc79c8"
	Realm              = "api.example.com"

	rpcReadinessTimeout    = 30 * time.Second
	rpcReadinessPollPeriod = 500 * time.Millisecond
	receiptWaitTimeout     = 30 * time.Second
	receiptWaitPollPeriod  = 500 * time.Millisecond
)

func RPCURL() string {
	if rpcURL := os.Getenv("TEMPO_RPC_URL"); rpcURL != "" {
		return rpcURL
	}
	return defaultRPCURL
}

func WaitForRPC(ctx context.Context, rpc tempo.RPCClient) (int64, error) {
	deadline := time.Now().Add(rpcReadinessTimeout)
	for time.Now().Before(deadline) {
		chainID, err := rpc.GetChainID(ctx)
		if err == nil && chainID != 0 {
			return int64(chainID), nil
		}
		time.Sleep(rpcReadinessPollPeriod)
	}
	return 0, fmt.Errorf("tempo rpc at %s did not become ready; start the local devnet with `docker compose up -d` or set TEMPO_RPC_URL", RPCURL())
}

func FundAddress(ctx context.Context, rpc tempo.RPCClient, address common.Address) error {
	response, err := rpc.SendRequest(ctx, "tempo_fundAddress", address.Hex())
	if err != nil {
		return err
	}
	if err := response.CheckError(); err != nil {
		return err
	}

	switch result := response.Result.(type) {
	case string:
		if result == "" {
			return nil
		}
		_, err = waitForReceipt(ctx, rpc, result)
		return err
	case []any:
		for _, item := range result {
			hash, ok := item.(string)
			if !ok || hash == "" {
				continue
			}
			if _, err := waitForReceipt(ctx, rpc, hash); err != nil {
				return err
			}
		}
	}

	return nil
}

func waitForReceipt(ctx context.Context, rpc tempo.RPCClient, hash string) (map[string]any, error) {
	deadline := time.Now().Add(receiptWaitTimeout)
	for time.Now().Before(deadline) {
		response, err := rpc.SendRequest(ctx, "eth_getTransactionReceipt", hash)
		if err == nil {
			if err := response.CheckError(); err == nil {
				if receipt, ok := response.Result.(map[string]any); ok && len(receipt) > 0 {
					return receipt, nil
				}
			}
		}
		time.Sleep(receiptWaitPollPeriod)
	}
	return nil, fmt.Errorf("transaction receipt not found for %s", hash)
}
