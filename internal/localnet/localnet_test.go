package localnet

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	temporpc "github.com/tempoxyz/tempo-go/pkg/client"
)

// fakeRPC answers eth_chainId and eth_getBlockByNumber from its fields.
type fakeRPC struct {
	chainID   uint64
	chainErr  error
	block     any
	blocks    []any
	blockCall int
}

func (f *fakeRPC) GetChainID(context.Context) (uint64, error) { return f.chainID, f.chainErr }

func (f *fakeRPC) GetTransactionCount(context.Context, string) (uint64, error) { return 0, nil }

func (f *fakeRPC) SendRawTransaction(context.Context, string) (string, error) { return "", nil }

func (f *fakeRPC) SendRequest(_ context.Context, method string, _ ...interface{}) (*temporpc.JSONRPCResponse, error) {
	if method != "eth_getBlockByNumber" {
		return nil, fmt.Errorf("unexpected method %s", method)
	}
	block := f.block
	if f.blocks != nil {
		block = f.blocks[min(f.blockCall, len(f.blocks)-1)]
		f.blockCall++
	}
	return &temporpc.JSONRPCResponse{Result: block}, nil
}

func block(number uint64, timestamp int64) map[string]any {
	return map[string]any{"number": fmt.Sprintf("0x%x", number), "timestamp": fmt.Sprintf("0x%x", timestamp)}
}

func TestReady(t *testing.T) {
	now := time.Unix(1_793_052_064, 0)
	tests := []struct {
		name    string
		rpc     *fakeRPC
		wantErr string
	}{
		{"current block", &fakeRPC{chainID: 1337, block: block(21, now.Unix())}, ""},
		{"block within skew", &fakeRPC{chainID: 1337, block: block(21, now.Add(-MaxClockSkew).Unix())}, ""},
		{"genesis", &fakeRPC{chainID: 1337, block: block(0, 0)}, "latest block 0 has timestamp 0"},
		{"stale block", &fakeRPC{chainID: 1337, block: block(9, now.Add(-time.Minute).Unix())}, "1m0s from the local clock"},
		{"future block", &fakeRPC{chainID: 1337, block: block(9, now.Add(time.Minute).Unix())}, "-1m0s from the local clock"},
		{"chain ID error", &fakeRPC{chainErr: errors.New("connection refused")}, "eth_chainId: connection refused"},
		{"chain ID 0", &fakeRPC{}, "chain ID 0"},
		{"no block", &fakeRPC{chainID: 1337}, "unexpected result"},
		{"bad timestamp", &fakeRPC{chainID: 1337, block: map[string]any{"number": "0x1", "timestamp": "nope"}}, "timestamp"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			chainID, err := Ready(context.Background(), tt.rpc, now)
			if tt.wantErr == "" {
				if err != nil || chainID != 1337 {
					t.Fatalf("Ready() = %d, %v; want 1337, nil", chainID, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Ready() error = %v; want it to contain %q", err, tt.wantErr)
			}
		})
	}
}

func TestWaitReadyWaitsPastGenesis(t *testing.T) {
	rpc := &fakeRPC{chainID: 1337, blocks: []any{block(0, 0), block(0, 0), block(1, time.Now().Unix())}}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	chainID, err := WaitReady(ctx, rpc)
	if err != nil || chainID != 1337 {
		t.Fatalf("WaitReady() = %d, %v; want 1337, nil", chainID, err)
	}
	if rpc.blockCall != 3 {
		t.Fatalf("polled the latest block %d times, want 3", rpc.blockCall)
	}
}

func TestWaitReadyTimeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	_, err := WaitReady(ctx, &fakeRPC{chainID: 1337, block: block(0, 0)})
	if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "latest block 0 has timestamp 0") {
		t.Fatalf("WaitReady() error = %v; want the deadline and the last probe's error", err)
	}
}
