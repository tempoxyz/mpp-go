//go:build integration

package tests

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// TestIntegrationExamples runs the examples' smoke tests (each starts the
// example's server and pays it with its client) against this run's node. The
// examples live in the library module, so they run as a child go test.
func TestIntegrationExamples(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	goBin := filepath.Join(os.Getenv("GOROOT"), "bin", "go")
	if _, err := os.Stat(goBin); err != nil {
		goBin = "go"
	}
	// -p 1: the examples share the devnet payer key, so their payments must
	// not race for its nonce.
	cmd := exec.CommandContext(ctx, goBin, "test", "-count=1", "-p", "1", "-tags", "integration", "./examples/...")
	cmd.Dir = ".."
	cmd.Env = append(os.Environ(), "TEMPO_RPC_URL="+integrationRPCURL(t))
	output, err := cmd.CombinedOutput()
	t.Logf("go test ./examples/...:\n%s", output)
	if err != nil {
		t.Fatalf("example smoke tests failed: %v", err)
	}
}
