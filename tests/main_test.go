//go:build integration

package tests

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/tempoxyz/mpp-go/pkg/tempo"
	temposigner "github.com/tempoxyz/tempo-go/pkg/signer"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

const chainIDRequest = `{"jsonrpc":"2.0","method":"eth_chainId","params":[],"id":1}`

// sharedPayer is funded once in TestMain and pays in the tests that only
// need a funded account. Tests that use it must not run in parallel.
var sharedPayer *temposigner.Signer

// TestMain starts the docker-compose.yml localnet with testcontainers when
// TEMPO_RPC_URL is unset, so `go test -tags integration ./...` needs only
// Docker. Set TEMPO_RPC_URL to use a running node (docker compose, Moderato).
func TestMain(m *testing.M) {
	os.Exit(run(m))
}

func run(m *testing.M) int {
	ctx := context.Background()
	if os.Getenv("TEMPO_RPC_URL") == "" {
		rpcURL, stop, err := startLocalnet(ctx)
		if err != nil {
			fmt.Fprintf(os.Stderr, "start Tempo localnet: %v\n", err)
			return 1
		}
		defer stop()
		os.Setenv("TEMPO_RPC_URL", rpcURL)
	}

	rpc := tempo.NewRPCClient(os.Getenv("TEMPO_RPC_URL"))
	readyCtx, cancel := context.WithTimeout(ctx, rpcReadinessTimeout)
	defer cancel()
	if _, err := waitForChainID(readyCtx, rpc); err != nil {
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

// startLocalnet runs the docker-compose.yml tempo-node image and command and
// returns its RPC URL once eth_chainId answers.
func startLocalnet(ctx context.Context) (string, func(), error) {
	spec, err := readLocalnetSpec(composeFile)
	if err != nil {
		return "", nil, err
	}
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        spec.Image,
			Cmd:          spec.Command,
			ExposedPorts: []string{"8545/tcp"},
			// docker-compose.yml publishes the node on 127.0.0.1 only; so do we.
			HostConfigModifier: bindLoopback("8545/tcp"),
			WaitingFor: wait.ForHTTP("/").
				WithPort("8545/tcp").
				WithMethod(http.MethodPost).
				WithHeaders(map[string]string{"Content-Type": "application/json"}).
				WithBody(strings.NewReader(chainIDRequest)).
				WithResponseMatcher(func(body io.Reader) bool {
					data, _ := io.ReadAll(body)
					return bytes.Contains(data, []byte(`"result"`))
				}).
				WithStartupTimeout(2 * time.Minute),
		},
		Started: true,
	})
	stop := func() {
		if container != nil {
			_ = testcontainers.TerminateContainer(container)
		}
	}
	if err != nil {
		stop()
		return "", nil, err
	}
	endpoint, err := container.PortEndpoint(ctx, "8545/tcp", "http")
	if err != nil {
		stop()
		return "", nil, err
	}
	return endpoint, stop, nil
}

// bindLoopback publishes each of ports on an ephemeral 127.0.0.1 port instead
// of Docker's default, every host interface.
func bindLoopback(ports ...string) func(*container.HostConfig) {
	return func(hostConfig *container.HostConfig) {
		if hostConfig.PortBindings == nil {
			hostConfig.PortBindings = network.PortMap{}
		}
		for _, port := range ports {
			hostConfig.PortBindings[network.MustParsePort(port)] = []network.PortBinding{
				{HostIP: netip.AddrFrom4([4]byte{127, 0, 0, 1}), HostPort: "0"},
			}
		}
	}
}

// waitForChainID polls eth_chainId until it answers or ctx ends.
func waitForChainID(ctx context.Context, rpc tempo.RPCClient) (uint64, error) {
	ticker := time.NewTicker(rpcReadinessPollInterval)
	defer ticker.Stop()
	var lastErr error
	for {
		chainID, err := rpc.GetChainID(ctx)
		if err == nil && chainID != 0 {
			return chainID, nil
		}
		if err != nil {
			lastErr = err
		}
		select {
		case <-ctx.Done():
			return 0, fmt.Errorf("not ready: %w (last error: %v)", ctx.Err(), lastErr)
		case <-ticker.C:
		}
	}
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
