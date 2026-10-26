//go:build integration

package tests

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	temposigner "github.com/tempoxyz/tempo-go/pkg/signer"

	"github.com/tempoxyz/mpp-go/internal/localnet"
	"github.com/tempoxyz/mpp-go/pkg/tempo"
	"github.com/tempoxyz/mpp-go/tests/tempotest"
)

// sharedPayer is funded once in TestMain and pays in the tests that only
// need a funded account. Tests that use it must not run in parallel.
var sharedPayer *temposigner.Signer

// TestMain starts the docker-compose.yml localnet with tempotest when
// TEMPO_RPC_URL is unset, so `go test -tags integration ./...` needs only
// Docker. Set TEMPO_RPC_URL to use a running node (docker compose, Moderato).
func TestMain(m *testing.M) {
	os.Exit(run(m))
}

func run(m *testing.M) int {
	ctx := context.Background()
	if os.Getenv("TEMPO_RPC_URL") == "" {
		node, err := tempotest.Start(ctx)
		if err != nil {
			fmt.Fprintf(os.Stderr, "start Tempo localnet: %v\n", err)
			return 1
		}
		defer func() {
			if err := node.Terminate(context.Background()); err != nil {
				fmt.Fprintf(os.Stderr, "terminate Tempo localnet: %v\n", err)
			}
		}()
		os.Setenv("TEMPO_RPC_URL", node.RPCURL)
	}

	// tempotest.Start already waited; a node from TEMPO_RPC_URL (such as a
	// docker compose localnet started just before) may still be at genesis.
	rpc := tempo.NewRPCClient(os.Getenv("TEMPO_RPC_URL"))
	readyCtx, cancel := context.WithTimeout(ctx, rpcReadinessTimeout)
	defer cancel()
	if _, err := localnet.WaitReady(readyCtx, rpc); err != nil {
		fmt.Fprintf(os.Stderr, "Tempo RPC at %s: %v\n", os.Getenv("TEMPO_RPC_URL"), err)
		return 1
	}

	key, err := crypto.GenerateKey()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	sharedPayer = temposigner.NewSignerFromKey(key)
	fundCtx, cancelFund := context.WithTimeout(ctx, 2*receiptPollingTimeout)
	defer cancelFund()
	if err := fundAccount(fundCtx, rpc, sharedPayer.Address()); err != nil {
		fmt.Fprintf(os.Stderr, "fund shared payer: %v\n", err)
		return 1
	}

	return m.Run()
}

// fundAccount funds address from the localnet faucet and waits until its
// token balance arrives.
func fundAccount(ctx context.Context, rpc tempo.RPCClient, address common.Address) error {
	response, err := rpc.SendRequest(ctx, "tempo_fundAddress", address.Hex())
	if err != nil {
		return err
	}
	if err := response.CheckError(); err != nil {
		return err
	}
	ticker := time.NewTicker(receiptPollingInterval)
	defer ticker.Stop()
	var lastErr error
	for {
		balance, err := tokenBalanceOf(ctx, rpc, address)
		if err == nil && balance.Cmp(integrationFundingAmount) >= 0 {
			return nil
		}
		if err != nil {
			lastErr = err
		} else {
			lastErr = fmt.Errorf("balance %s below %s", balance, integrationFundingAmount)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("fund %s: %w (last error: %v)", address.Hex(), ctx.Err(), lastErr)
		case <-ticker.C:
		}
	}
}
