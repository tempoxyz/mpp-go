// Package localnet waits until a Tempo node is ready to accept transactions.
// The examples and the live tests (the nested tests module) share it.
package localnet

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/tempoxyz/mpp-go/pkg/tempo"
)

const (
	// MaxClockSkew is how far the latest block's timestamp may be from the
	// local clock for the node to count as ready. Clients set an expiring
	// nonce's valid_before from the local clock, and the node rejects one
	// more than 300s past its latest block, so the bound stays below that
	// limit with room for valid_before's own offset. It is wide enough for a
	// node whose clock drifts from the host's (a Docker Desktop VM, a remote
	// node at TEMPO_RPC_URL) while still rejecting genesis, at timestamp 0.
	MaxClockSkew = 240 * time.Second

	pollInterval = 250 * time.Millisecond
)

// WaitReady polls rpc until the node reports a chain ID and its latest block
// is within MaxClockSkew of the local clock, or ctx ends, and returns the chain
// ID. A fresh localnet answers eth_chainId while its latest block is still
// genesis, whose timestamp is 0, so a transaction sent then fails with
// "valid_before ... too far in the future". The next block it builds carries
// the current time.
func WaitReady(ctx context.Context, rpc tempo.RPCClient) (uint64, error) {
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for {
		chainID, err := Ready(ctx, rpc, time.Now())
		if err == nil {
			return chainID, nil
		}
		select {
		case <-ctx.Done():
			return 0, fmt.Errorf("tempo node not ready: %w", errors.Join(ctx.Err(), err))
		case <-ticker.C:
		}
	}
}

// Ready checks once whether the node at rpc is ready at time now, as WaitReady
// does, and returns its chain ID.
func Ready(ctx context.Context, rpc tempo.RPCClient, now time.Time) (uint64, error) {
	chainID, err := rpc.GetChainID(ctx)
	if err != nil {
		return 0, fmt.Errorf("eth_chainId: %w", err)
	}
	if chainID == 0 {
		return 0, errors.New("eth_chainId: chain ID 0")
	}
	number, timestamp, err := latestBlock(ctx, rpc)
	if err != nil {
		return 0, err
	}
	blockTime := time.Unix(int64(timestamp), 0) // #nosec G115 -- a block timestamp in seconds fits int64
	if skew := now.Sub(blockTime); skew > MaxClockSkew || skew < -MaxClockSkew {
		return 0, fmt.Errorf("latest block %d has timestamp %d, %s from the local clock (more than %s)", number, timestamp, skew.Round(time.Second), MaxClockSkew)
	}
	return chainID, nil
}

func latestBlock(ctx context.Context, rpc tempo.RPCClient) (number, timestamp uint64, err error) {
	response, err := rpc.SendRequest(ctx, "eth_getBlockByNumber", "latest", false)
	if err != nil {
		return 0, 0, fmt.Errorf("eth_getBlockByNumber: %w", err)
	}
	if err := response.CheckError(); err != nil {
		return 0, 0, fmt.Errorf("eth_getBlockByNumber: %w", err)
	}
	block, ok := response.Result.(map[string]any)
	if !ok {
		return 0, 0, fmt.Errorf("eth_getBlockByNumber: unexpected result %v", response.Result)
	}
	numberHex, _ := block["number"].(string)
	timestampHex, _ := block["timestamp"].(string)
	if number, err = tempo.ParseHexUint64(numberHex); err != nil {
		return 0, 0, fmt.Errorf("eth_getBlockByNumber: number: %w", err)
	}
	if timestamp, err = tempo.ParseHexUint64(timestampHex); err != nil {
		return 0, 0, fmt.Errorf("eth_getBlockByNumber: timestamp: %w", err)
	}
	return number, timestamp, nil
}
