//go:build integration

package main

import (
	"context"
	"testing"
	"time"

	"github.com/tempoxyz/mpp-go/examples/internal/devnet"
)

// TestSmoke runs the example's server and client against the node at
// TEMPO_RPC_URL (see tests/ for running it with only Docker).
func TestSmoke(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	rpcURL, chainID, err := devnet.Prepare(ctx, devnet.PayerPrivateKey, devnet.FeePayerPrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	api, err := startServer(rpcURL, chainID)
	if err != nil {
		t.Fatal(err)
	}
	defer api.Close()

	result, err := runClient(ctx, api.url, rpcURL, chainID)
	if err != nil {
		t.Fatal(err)
	}
	if result.Receipt == "" || result.Tx == "" {
		t.Fatalf("result = %+v, want a receipt and transaction", result)
	}
}
