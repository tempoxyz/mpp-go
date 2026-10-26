package devnet

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"os"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/tempoxyz/mpp-go/pkg/tempo"
	temposigner "github.com/tempoxyz/tempo-go/pkg/signer"
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

// TODO(tempo-go): move these local-devnet readiness and faucet helpers into a
// shared tempo-go test support package once multiple Go SDKs depend on them.

// RPCURL returns TEMPO_RPC_URL, or the local devnet URL when it is unset.
func RPCURL() string {
	if rpcURL := os.Getenv("TEMPO_RPC_URL"); rpcURL != "" {
		return rpcURL
	}
	return defaultRPCURL
}

// WaitForRPC polls the node until it reports a chain ID, ctx ends, or the
// readiness timeout passes, and returns the chain ID.
func WaitForRPC(ctx context.Context, rpc tempo.RPCClient) (int64, error) {
	ctx, cancel := context.WithTimeout(ctx, rpcReadinessTimeout)
	defer cancel()
	ticker := time.NewTicker(rpcReadinessPollPeriod)
	defer ticker.Stop()
	var lastErr error
	for {
		chainID, err := rpc.GetChainID(ctx)
		if err == nil && chainID != 0 {
			return int64(chainID), nil
		}
		if err != nil {
			lastErr = err
		}
		select {
		case <-ctx.Done():
			return 0, fmt.Errorf("tempo rpc at %s did not become ready; start the local devnet with `docker compose up -d` or set TEMPO_RPC_URL: %w", RPCURL(), lastErrOr(lastErr, ctx.Err()))
		case <-ticker.C:
		}
	}
}

// FundAddress funds address from the devnet faucet and waits for the funding
// transactions' receipts.
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
	ctx, cancel := context.WithTimeout(ctx, receiptWaitTimeout)
	defer cancel()
	ticker := time.NewTicker(receiptWaitPollPeriod)
	defer ticker.Stop()
	var lastErr error
	for {
		response, err := rpc.SendRequest(ctx, "eth_getTransactionReceipt", hash)
		if err == nil {
			err = response.CheckError()
		}
		if err == nil {
			if receipt, ok := response.Result.(map[string]any); ok && len(receipt) > 0 {
				return receipt, nil
			}
		} else {
			lastErr = err
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("transaction receipt not found for %s: %w", hash, lastErrOr(lastErr, ctx.Err()))
		case <-ticker.C:
		}
	}
}

// lastErrOr returns lastErr when a poll failed, or fallback (the context's
// error) when every poll succeeded without the expected result.
func lastErrOr(lastErr, fallback error) error {
	if lastErr != nil {
		return errors.Join(fallback, lastErr)
	}
	return fallback
}

// PaymentPolicy permits at most one token per payment to the example payee on
// the configured chain. Applications should also enforce their own total budget.
func PaymentPolicy(chainID int64, recipient string) func(context.Context, tempo.ChargeRequest) error {
	return func(_ context.Context, request tempo.ChargeRequest) error {
		amount, ok := new(big.Int).SetString(request.Amount, 10)
		if !ok || amount.Sign() < 0 || amount.Cmp(big.NewInt(1_000_000)) > 0 ||
			!strings.EqualFold(request.Currency, Currency) ||
			!strings.EqualFold(request.Recipient, recipient) ||
			request.MethodDetails.ChainID == nil || *request.MethodDetails.ChainID != chainID ||
			len(request.MethodDetails.Splits) != 0 {
			return fmt.Errorf("payment exceeds the example's amount, currency, recipient, or chain policy")
		}
		return nil
	}
}

// Prepare waits for the node at RPCURL and funds the accounts of the given
// private keys, returning the RPC URL and chain ID for an example run.
func Prepare(ctx context.Context, privateKeys ...string) (string, int64, error) {
	rpcURL := RPCURL()
	rpc := tempo.NewRPCClient(rpcURL)
	chainID, err := WaitForRPC(ctx, rpc)
	if err != nil {
		return "", 0, err
	}
	for _, key := range privateKeys {
		signer, err := temposigner.NewSigner(key)
		if err != nil {
			return "", 0, err
		}
		if err := FundAddress(ctx, rpc, signer.Address()); err != nil {
			return "", 0, fmt.Errorf("fund %s: %w", signer.Address().Hex(), err)
		}
	}
	return rpcURL, chainID, nil
}
