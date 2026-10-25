package devnet

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/tempoxyz/mpp-go/pkg/tempo"
)

func TestWaitForRPCHonorsContext(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "booting", http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err := WaitForRPC(ctx, tempo.NewRPCClient(srv.URL))
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("WaitForRPC returned after %s, want it to stop when ctx ends", elapsed)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("WaitForRPC() error = %v, want context.DeadlineExceeded", err)
	}
	if errors.Unwrap(err) == nil || err.Error() == context.DeadlineExceeded.Error() {
		t.Fatalf("WaitForRPC() error = %v, want the last RPC error wrapped too", err)
	}
}

func TestWaitForReceiptHonorsContext(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":null}`))
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := waitForReceipt(ctx, tempo.NewRPCClient(srv.URL), "0x01")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("waitForReceipt() error = %v, want context.Canceled", err)
	}
}
