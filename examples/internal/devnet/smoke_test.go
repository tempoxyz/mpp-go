//go:build integration

package devnet

import (
	"context"
	"testing"
	"time"

	"github.com/tempoxyz/mpp-go/pkg/tempo"
	temposigner "github.com/tempoxyz/tempo-go/pkg/signer"
)

func TestSmoke(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	rpcURL, chainID, err := Prepare(ctx, FeePayerPrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	if rpcURL != RPCURL() || chainID == 0 {
		t.Fatalf("Prepare() = %q, %d", rpcURL, chainID)
	}
	signer, err := temposigner.NewSigner(FeePayerPrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	balance, err := tempo.NewRPCClient(rpcURL).SendRequest(ctx, "eth_getBalance", signer.Address().Hex(), "latest")
	if err != nil {
		t.Fatal(err)
	}
	if err := balance.CheckError(); err != nil {
		t.Fatal(err)
	}
}
