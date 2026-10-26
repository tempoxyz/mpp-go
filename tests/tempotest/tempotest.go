// Package tempotest is a thin testcontainers-go wrapper over the Tempo team's
// published localnet image, ghcr.io/tempoxyz/tempo-localnet, at the digest
// docker-compose.yml pins. The image is a deterministic dev node (chain ID
// 1337, RPC on 8545) with prefunded dev accounts, a tempo_fundAddress faucet
// for the canonical TIP-20 tokens, and Fee AMM and DEX liquidity bootstrapped;
// its Docker HEALTHCHECK reports healthy only once that setup completes. See
// https://github.com/tempoxyz/tempo/blob/main/docs/localnet.md for the image,
// its flags and its health check. The package lives in the tests module so the
// library's go.mod stays free of testcontainers-go.
package tempotest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/tempoxyz/mpp-go/internal/localnet"
	"github.com/tempoxyz/mpp-go/pkg/tempo"
)

// StartupTimeout bounds each wait for a started container: for Docker to
// report it healthy, then for its RPC endpoint and node to be ready. Pulling and creating the
// container run under the caller's context alone, so a slow image pull is not
// cut short.
const StartupTimeout = 2 * time.Minute

const rpcPort = "8545/tcp"

var (
	composeImagePattern   = regexp.MustCompile(`(?m)^\s*image:\s*\$\{TEMPO_LOCALNET_IMAGE:-([^}]+)\}\s*$`)
	composeCommandPattern = regexp.MustCompile(`(?m)^\s*command:\s*(\[.*\])\s*$`)
)

// Spec is the tempo-node service of docker-compose.yml.
type Spec struct {
	Image   string
	Command []string
}

// rootModule is the module path the repository root's go.mod declares.
const rootModule = "github.com/tempoxyz/mpp-go"

var goModModulePattern = regexp.MustCompile(`(?m)^\s*module\s+(\S+)\s*$`)

// ComposeFile returns the path of the repository's docker-compose.yml, the
// single source of truth for the pinned localnet image and its command. It
// finds the repository root by walking up from the working directory (the
// package's directory under go test) to the directory whose go.mod declares
// module github.com/tempoxyz/mpp-go, so it works under -trimpath too.
func ComposeFile() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	return findComposeFile(dir)
}

// findComposeFile returns docker-compose.yml in the nearest of dir and its
// parents whose go.mod declares rootModule.
func findComposeFile(dir string) (string, error) {
	for start := dir; ; {
		data, err := os.ReadFile(filepath.Join(dir, "go.mod"))
		if err == nil {
			if m := goModModulePattern.FindSubmatch(data); m != nil && string(m[1]) == rootModule {
				return filepath.Join(dir, "docker-compose.yml"), nil
			}
		} else if !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("no go.mod declaring module %s in %s or its parents", rootModule, start)
		}
		dir = parent
	}
}

// ReadSpec reads the tempo-node image and command from the compose file at
// path. TEMPO_LOCALNET_IMAGE overrides the image, as it does for docker compose.
func ReadSpec(path string) (Spec, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Spec{}, err
	}
	image := composeImagePattern.FindSubmatch(data)
	if image == nil {
		return Spec{}, fmt.Errorf("%s: no image: ${TEMPO_LOCALNET_IMAGE:-...} line", path)
	}
	command := composeCommandPattern.FindSubmatch(data)
	if command == nil {
		return Spec{}, fmt.Errorf("%s: no command: [...] line", path)
	}
	spec := Spec{Image: strings.TrimSpace(string(image[1]))}
	if err := json.Unmarshal(command[1], &spec.Command); err != nil {
		return Spec{}, fmt.Errorf("%s: command: %w", path, err)
	}
	if override := os.Getenv("TEMPO_LOCALNET_IMAGE"); override != "" {
		spec.Image = override
	}
	return spec, nil
}

// Localnet is a running Tempo localnet container.
type Localnet struct {
	// RPCURL is the node's JSON-RPC endpoint on 127.0.0.1.
	RPCURL string
	// ChainID is the chain ID the node reported.
	ChainID uint64

	container testcontainers.Container
}

// Start runs the docker-compose.yml tempo-node image and command, publishes its
// RPC port on an ephemeral 127.0.0.1 port, and returns once Docker reports the
// container healthy (the image's HEALTHCHECK passes only after the localnet's
// setup completes). It then checks with localnet.WaitReady, as for a node from
// TEMPO_RPC_URL, that the node reports a chain ID and builds blocks at the
// current time; for a healthy container that returns at once. Call Terminate
// when done; on error nothing is left running.
func Start(ctx context.Context) (*Localnet, error) {
	composeFile, err := ComposeFile()
	if err != nil {
		return nil, err
	}
	spec, err := ReadSpec(composeFile)
	if err != nil {
		return nil, err
	}
	// Pull and create the container under ctx alone: StartupTimeout bounds
	// only the wait for the started node (the health check below, then
	// WaitReady), not a slow image pull.
	ctr, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        spec.Image,
			Cmd:          spec.Command,
			ExposedPorts: []string{rpcPort},
			// docker-compose.yml publishes the node on 127.0.0.1 only; so do we.
			HostConfigModifier: BindLoopback(rpcPort),
			// The image's HEALTHCHECK reports healthy only after the
			// localnet's setup (faucet tokens, Fee AMM and DEX liquidity)
			// completes. A TEMPO_LOCALNET_IMAGE without one never does.
			WaitingFor: wait.ForHealthCheck().
				WithStartupTimeout(StartupTimeout).
				WithPollInterval(250 * time.Millisecond),
		},
		Started: true,
	})
	l := &Localnet{container: ctr}
	if err != nil {
		return nil, errors.Join(fmt.Errorf("start %s: %w", spec.Image, err), l.Terminate(context.Background()))
	}
	ctx, cancel := context.WithTimeout(ctx, StartupTimeout)
	defer cancel()
	if l.RPCURL, err = ctr.PortEndpoint(ctx, rpcPort, "http"); err != nil {
		return nil, errors.Join(err, l.Terminate(context.Background()))
	}
	if l.ChainID, err = localnet.WaitReady(ctx, tempo.NewRPCClient(l.RPCURL)); err != nil {
		return nil, errors.Join(fmt.Errorf("%s at %s: %w", spec.Image, l.RPCURL, err), l.Terminate(context.Background()))
	}
	return l, nil
}

// Run starts a Localnet for tb (see Start) and terminates it when tb's test
// and its subtests finish. It fails tb if the localnet does not start.
func Run(tb testing.TB) *Localnet {
	tb.Helper()
	l, err := Start(tb.Context())
	if err != nil {
		tb.Fatalf("start Tempo localnet: %v", err)
	}
	tb.Cleanup(func() {
		if err := l.Terminate(context.Background()); err != nil {
			tb.Errorf("terminate Tempo localnet: %v", err)
		}
	})
	return l
}

// Terminate stops and removes the container.
func (l *Localnet) Terminate(ctx context.Context) error {
	if l == nil || l.container == nil {
		return nil
	}
	return testcontainers.TerminateContainer(l.container, testcontainers.StopContext(ctx))
}

// BindLoopback publishes each of ports on an ephemeral 127.0.0.1 port instead
// of Docker's default, every host interface. Pass it as a container request's
// HostConfigModifier.
func BindLoopback(ports ...string) func(*container.HostConfig) {
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
