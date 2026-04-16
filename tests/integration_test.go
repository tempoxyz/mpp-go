//go:build integration
// +build integration

package tests

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	mppclient "github.com/tempoxyz/mpp-go/pkg/client"
	"github.com/tempoxyz/mpp-go/pkg/mpp"
	mppserver "github.com/tempoxyz/mpp-go/pkg/server"
	"github.com/tempoxyz/mpp-go/pkg/tempo"
	chargeclient "github.com/tempoxyz/mpp-go/pkg/tempo/client"
	chargeserver "github.com/tempoxyz/mpp-go/pkg/tempo/server"
	temposigner "github.com/tempoxyz/tempo-go/pkg/signer"
	tempotx "github.com/tempoxyz/tempo-go/pkg/transaction"
)

const (
	defaultIntegrationRPCURL = "http://localhost:8545"

	integrationRealm     = "mpp-go.local"
	integrationSecretKey = "integration-secret"
	// integrationCurrency is the fixed TIP-20 token used in the local devnet tests.
	integrationCurrency = "0x20c0000000000000000000000000000000000000"
	// integrationRecipient is the account that receives paid transfers during tests.
	integrationRecipient = "0x70997970c51812dc3a010c7d01b50e0d17dc79c8"
	// integrationDevPrivateKey is Anvil's default funded dev key used for fallback funding.
	integrationDevPrivateKey = "0xac0974bec39a17e36ba4a6b4d238ff944bacb478cbed5efcae784d7bf4f2ff80"
	receiptPollingTimeout    = 45 * time.Second
	receiptPollingInterval   = 500 * time.Millisecond
	rpcReadinessTimeout      = 45 * time.Second
	rpcReadinessPollInterval = 500 * time.Millisecond
)

var integrationFundingAmount = big.NewInt(100_000_000_000)

type captureTransport struct {
	inner http.RoundTripper
	mu    sync.Mutex
	auth  string
}

func (t *captureTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if auth := req.Header.Get("Authorization"); auth != "" {
		t.mu.Lock()
		t.auth = auth
		t.mu.Unlock()
	}
	inner := t.inner
	if inner == nil {
		inner = http.DefaultTransport
	}
	return inner.RoundTrip(req)
}

func (t *captureTransport) Authorization() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.auth
}

func TestIntegrationChargeFlow_LocalNode(t *testing.T) {
	rpcURL := integrationRPCURL(t)
	rpc := tempo.NewRPCClient(rpcURL)
	ctx := context.Background()
	chainID := waitForRPC(t, ctx, rpc)
	payerSigner := newSigner(t)
	fundAddress(t, ctx, rpc, payerSigner.Address())

	server := newPaidServer(t, rpcURL, chainID, nil)
	defer server.Close()

	clientMethod, err := chargeclient.New(chargeclient.Config{
		Signer:  payerSigner,
		RPCURL:  rpcURL,
		ChainID: int64(chainID),
	})
	if err != nil {
		t.Fatalf("tempo/client.New() error = %v", err)
	}

	transport := &captureTransport{inner: http.DefaultTransport}
	client := mppclient.New(
		[]mppclient.Method{clientMethod},
		mppclient.WithHTTPClient(&http.Client{Transport: transport}),
	)
	response, err := client.Get(ctx, server.URL+"/paid")
	if err != nil {
		t.Fatalf("client.Get() error = %v", err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.StatusCode, http.StatusOK)
	}
	receiptHeader := response.Header.Get("Payment-Receipt")
	if receiptHeader == "" {
		t.Fatal("Payment-Receipt header missing")
	}
	receipt, err := mpp.ParsePaymentReceipt(receiptHeader)
	if err != nil {
		t.Fatalf("ParsePaymentReceipt() error = %v", err)
	}
	if receipt.Status != "success" {
		t.Fatalf("receipt.Status = %q, want success", receipt.Status)
	}
	if receipt.Method != tempo.MethodName {
		t.Fatalf("receipt.Method = %q, want %q", receipt.Method, tempo.MethodName)
	}
	if receipt.Reference == "" {
		t.Fatal("receipt.Reference is empty")
	}

	var body struct {
		Payer string `json:"payer"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("Decode(response.Body) error = %v", err)
	}
	if body.Payer == "" {
		t.Fatal("response payer is empty")
	}

	authorization := transport.Authorization()
	if authorization == "" {
		t.Fatal("retry authorization header missing")
	}
	credential, err := mpp.ParseCredential(authorization)
	if err != nil {
		t.Fatalf("ParseCredential() error = %v", err)
	}
	if credential.Payload["type"] != string(tempo.CredentialTypeTransaction) {
		t.Fatalf("credential payload type = %#v, want transaction", credential.Payload["type"])
	}
}

func TestIntegrationChargeFlow_LocalNodeFeePayer(t *testing.T) {
	rpcURL := integrationRPCURL(t)
	rpc := tempo.NewRPCClient(rpcURL)
	ctx := context.Background()
	chainID := waitForRPC(t, ctx, rpc)
	payerSigner := newSigner(t)
	feePayerSigner := newSigner(t)
	fundAddress(t, ctx, rpc, payerSigner.Address())
	fundAddress(t, ctx, rpc, feePayerSigner.Address())

	server := newPaidServer(t, rpcURL, chainID, feePayerSigner)
	defer server.Close()

	clientMethod, err := chargeclient.New(chargeclient.Config{
		Signer:  payerSigner,
		RPCURL:  rpcURL,
		ChainID: int64(chainID),
	})
	if err != nil {
		t.Fatalf("tempo/client.New() error = %v", err)
	}

	transport := &captureTransport{inner: http.DefaultTransport}
	client := mppclient.New(
		[]mppclient.Method{clientMethod},
		mppclient.WithHTTPClient(&http.Client{Transport: transport}),
	)
	response, err := client.Get(ctx, server.URL+"/paid-fee-payer")
	if err != nil {
		t.Fatalf("client.Get() error = %v", err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.StatusCode, http.StatusOK)
	}
	if response.Header.Get("Payment-Receipt") == "" {
		t.Fatal("Payment-Receipt header missing")
	}

	authorization := transport.Authorization()
	if authorization == "" {
		t.Fatal("retry authorization header missing")
	}
	credential, err := mpp.ParseCredential(authorization)
	if err != nil {
		t.Fatalf("ParseCredential() error = %v", err)
	}
	if credential.Payload["type"] != string(tempo.CredentialTypeTransaction) {
		t.Fatalf("credential payload type = %#v, want transaction", credential.Payload["type"])
	}
	signature, _ := credential.Payload["signature"].(string)
	if !strings.Contains(signature, "feefeefeefee") {
		t.Fatalf("expected fee payer marker in credential payload, got %q", signature)
	}
}

func TestIntegrationChargeFlow_LocalNodeHashReplayProtected(t *testing.T) {
	rpcURL := integrationRPCURL(t)
	rpc := tempo.NewRPCClient(rpcURL)
	ctx := context.Background()
	chainID := waitForRPC(t, ctx, rpc)
	payerSigner := newSigner(t)
	fundAddress(t, ctx, rpc, payerSigner.Address())

	server := newPaidServer(t, rpcURL, chainID, nil)
	defer server.Close()

	clientMethod, err := chargeclient.New(chargeclient.Config{
		Signer:         payerSigner,
		RPCURL:         rpcURL,
		ChainID:        int64(chainID),
		CredentialType: tempo.CredentialTypeHash,
	})
	if err != nil {
		t.Fatalf("tempo/client.New() error = %v", err)
	}

	transport := &captureTransport{inner: http.DefaultTransport}
	client := mppclient.New(
		[]mppclient.Method{clientMethod},
		mppclient.WithHTTPClient(&http.Client{Transport: transport}),
	)
	response, err := client.Get(ctx, server.URL+"/paid-hash")
	if err != nil {
		t.Fatalf("client.Get() error = %v", err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.StatusCode, http.StatusOK)
	}
	if response.Header.Get("Payment-Receipt") == "" {
		t.Fatal("Payment-Receipt header missing")
	}

	authorization := transport.Authorization()
	if authorization == "" {
		t.Fatal("retry authorization header missing")
	}
	credential, err := mpp.ParseCredential(authorization)
	if err != nil {
		t.Fatalf("ParseCredential() error = %v", err)
	}
	if credential.Payload["type"] != string(tempo.CredentialTypeHash) {
		t.Fatalf("credential payload type = %#v, want hash", credential.Payload["type"])
	}
	if hash, _ := credential.Payload["hash"].(string); hash == "" {
		t.Fatal("hash credential payload is empty")
	}

	replayRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/paid-hash", nil)
	if err != nil {
		t.Fatalf("NewRequestWithContext() error = %v", err)
	}
	replayRequest.Header.Set("Authorization", authorization)
	replayResponse, err := http.DefaultClient.Do(replayRequest)
	if err != nil {
		t.Fatalf("replay request error = %v", err)
	}
	defer replayResponse.Body.Close()

	if replayResponse.StatusCode != http.StatusPaymentRequired {
		t.Fatalf("replay status = %d, want %d", replayResponse.StatusCode, http.StatusPaymentRequired)
	}
	body, err := io.ReadAll(replayResponse.Body)
	if err != nil {
		t.Fatalf("ReadAll(replayResponse.Body) error = %v", err)
	}
	if !strings.Contains(string(body), "already used") {
		t.Fatalf("replay response body = %q, want replay protection error", string(body))
	}
}

func integrationRPCURL(t *testing.T) string {
	t.Helper()
	rpcURL := os.Getenv("TEMPO_RPC_URL")
	if rpcURL == "" {
		return defaultIntegrationRPCURL
	}
	return rpcURL
}

func waitForRPC(t *testing.T, ctx context.Context, rpc tempo.RPCClient) uint64 {
	t.Helper()

	deadline := time.Now().Add(rpcReadinessTimeout)
	for time.Now().Before(deadline) {
		chainID, err := rpc.GetChainID(ctx)
		if err == nil && chainID != 0 {
			return chainID
		}
		time.Sleep(rpcReadinessPollInterval)
	}
	t.Fatalf("Tempo RPC at %s did not become ready before timeout; start the local devnet with `docker compose up -d` in mpp-go or set TEMPO_RPC_URL", integrationRPCURL(t))
	return 0
}

func fundAddress(t *testing.T, ctx context.Context, rpc tempo.RPCClient, address common.Address) {
	t.Helper()

	response, err := rpc.SendRequest(ctx, "tempo_fundAddress", address.Hex())
	if err == nil {
		if err := response.CheckError(); err == nil {
			switch result := response.Result.(type) {
			case string:
				if result != "" {
					waitForReceipt(t, ctx, rpc, result)
					waitForTokenBalance(t, ctx, rpc, address, integrationFundingAmount)
					return
				}
			case []any:
				for _, item := range result {
					hash, ok := item.(string)
					if ok && hash != "" {
						waitForReceipt(t, ctx, rpc, hash)
					}
				}
				waitForTokenBalance(t, ctx, rpc, address, integrationFundingAmount)
				return
			}
		}
	}

	devSigner, err := temposigner.NewSigner(integrationDevPrivateKey)
	if err != nil {
		t.Fatalf("NewSigner(dev key) error = %v", err)
	}
	chainID, err := rpc.GetChainID(ctx)
	if err != nil {
		t.Fatalf("GetChainID(funding tx) error = %v", err)
	}
	gasPrice := mustGasPrice(t, ctx, rpc)
	nonce, err := rpc.GetTransactionCount(ctx, devSigner.Address().Hex())
	if err != nil {
		t.Fatalf("GetTransactionCount(dev signer) error = %v", err)
	}
	transferData := common.FromHex(tempo.EncodeTransfer(address.Hex(), integrationFundingAmount))
	gasLimit := mustEstimateGas(t, ctx, rpc, devSigner.Address(), common.HexToAddress(integrationCurrency), transferData)
	tx, err := tempotx.NewBuilder(new(big.Int).SetUint64(chainID)).
		SetMaxFeePerGas(gasPrice).
		SetMaxPriorityFeePerGas(new(big.Int).Set(gasPrice)).
		SetGas(gasLimit).
		SetNonceKey(big.NewInt(0)).
		SetNonce(nonce).
		SetFeeToken(common.HexToAddress(integrationCurrency)).
		AddCall(common.HexToAddress(integrationCurrency), big.NewInt(0), transferData).
		BuildAndValidate()
	if err != nil {
		t.Fatalf("BuildAndValidate(funding tx) error = %v", err)
	}
	if err := tempotx.SignTransaction(tx, devSigner); err != nil {
		t.Fatalf("SignTransaction(funding tx) error = %v", err)
	}
	serialized, err := tempotx.Serialize(tx, nil)
	if err != nil {
		t.Fatalf("Serialize(funding tx) error = %v", err)
	}
	hash, err := rpc.SendRawTransaction(ctx, serialized)
	if err != nil {
		t.Fatalf("SendRawTransaction(funding tx) error = %v", err)
	}
	waitForReceipt(t, ctx, rpc, hash)
	waitForTokenBalance(t, ctx, rpc, address, integrationFundingAmount)
}

func mustGasPrice(t *testing.T, ctx context.Context, rpc tempo.RPCClient) *big.Int {
	t.Helper()

	response, err := rpc.SendRequest(ctx, "eth_gasPrice")
	if err != nil {
		t.Fatalf("eth_gasPrice error = %v", err)
	}
	if err := response.CheckError(); err != nil {
		t.Fatalf("eth_gasPrice rpc error = %v", err)
	}
	value, ok := response.Result.(string)
	if !ok {
		t.Fatalf("eth_gasPrice result type = %T, want string", response.Result)
	}
	parsed, err := tempo.ParseHexBigInt(value)
	if err != nil {
		t.Fatalf("ParseHexBigInt(eth_gasPrice) error = %v", err)
	}
	return parsed
}

func mustEstimateGas(t *testing.T, ctx context.Context, rpc tempo.RPCClient, from, to common.Address, data []byte) uint64 {
	t.Helper()

	response, err := rpc.SendRequest(ctx, "eth_estimateGas", map[string]any{
		"from": from.Hex(),
		"to":   to.Hex(),
		"data": common.Bytes2Hex(data),
	})
	if err != nil {
		t.Fatalf("eth_estimateGas error = %v", err)
	}
	if err := response.CheckError(); err != nil {
		t.Fatalf("eth_estimateGas rpc error = %v", err)
	}
	value, ok := response.Result.(string)
	if !ok {
		t.Fatalf("eth_estimateGas result type = %T, want string", response.Result)
	}
	estimated, err := tempo.ParseHexUint64(value)
	if err != nil {
		t.Fatalf("ParseHexUint64(eth_estimateGas) error = %v", err)
	}
	return estimated + 5_000
}

func waitForTokenBalance(t *testing.T, ctx context.Context, rpc tempo.RPCClient, address common.Address, minimum *big.Int) {
	t.Helper()

	deadline := time.Now().Add(receiptPollingTimeout)
	for time.Now().Before(deadline) {
		balance, err := tokenBalanceOf(ctx, rpc, address)
		if err == nil && balance.Cmp(minimum) >= 0 {
			return
		}
		time.Sleep(receiptPollingInterval)
	}
	t.Fatalf("token balance for %s did not reach %s", address.Hex(), minimum.String())
}

func tokenBalanceOf(ctx context.Context, rpc tempo.RPCClient, address common.Address) (*big.Int, error) {
	response, err := rpc.SendRequest(ctx, "eth_call", map[string]any{
		"to":   integrationCurrency,
		"data": encodeBalanceOf(address),
	}, "latest")
	if err != nil {
		return nil, err
	}
	if err := response.CheckError(); err != nil {
		return nil, err
	}
	value, ok := response.Result.(string)
	if !ok {
		return nil, fmt.Errorf("eth_call result type = %T, want string", response.Result)
	}
	return tempo.ParseHexBigInt(value)
}

func encodeBalanceOf(address common.Address) string {
	return fmt.Sprintf("0x70a08231%064s", strings.TrimPrefix(strings.ToLower(address.Hex()), "0x"))
}

func waitForReceipt(t *testing.T, ctx context.Context, rpc tempo.RPCClient, hash string) map[string]any {
	t.Helper()

	deadline := time.Now().Add(receiptPollingTimeout)
	for time.Now().Before(deadline) {
		response, err := rpc.SendRequest(ctx, "eth_getTransactionReceipt", hash)
		if err == nil {
			if err := response.CheckError(); err == nil {
				if receipt, ok := response.Result.(map[string]any); ok && len(receipt) > 0 {
					if receipt["status"] != "0x1" {
						t.Fatalf("receipt status for %s = %#v, want 0x1", hash, receipt["status"])
					}
					return receipt
				}
			}
		}
		time.Sleep(receiptPollingInterval)
	}
	t.Fatalf("transaction receipt not found for %s", hash)
	return nil
}

func newSigner(t *testing.T) *temposigner.Signer {
	t.Helper()
	privateKey, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey() error = %v", err)
	}
	return temposigner.NewSignerFromKey(privateKey)
}

func newPaidServer(t *testing.T, rpcURL string, chainID uint64, feePayerSigner *temposigner.Signer) *httptest.Server {
	t.Helper()

	intent, err := chargeserver.NewIntent(chargeserver.IntentConfig{
		RPCURL:         rpcURL,
		FeePayerSigner: feePayerSigner,
	})
	if err != nil {
		t.Fatalf("NewIntent() error = %v", err)
	}

	basicMethod := chargeserver.NewMethod(chargeserver.MethodConfig{
		Intent:    intent,
		Currency:  integrationCurrency,
		Recipient: integrationRecipient,
		ChainID:   int64(chainID),
	})
	feePayerMethod := chargeserver.NewMethod(chargeserver.MethodConfig{
		Intent:    intent,
		Currency:  integrationCurrency,
		Recipient: integrationRecipient,
		ChainID:   int64(chainID),
		FeePayer:  true,
	})
	hashMethod := chargeserver.NewMethod(chargeserver.MethodConfig{
		Intent:         intent,
		Currency:       integrationCurrency,
		Recipient:      integrationRecipient,
		ChainID:        int64(chainID),
		SupportedModes: []tempo.ChargeMode{tempo.ChargeModePush},
	})
	basic := mppserver.New(basicMethod, integrationRealm, integrationSecretKey)
	feePayer := mppserver.New(feePayerMethod, integrationRealm, integrationSecretKey)
	hash := mppserver.New(hashMethod, integrationRealm, integrationSecretKey)

	mux := http.NewServeMux()
	mux.HandleFunc("/paid", paidHandler(t, basic, false))
	mux.HandleFunc("/paid-fee-payer", paidHandler(t, feePayer, true))
	mux.HandleFunc("/paid-hash", paidHandler(t, hash, false))
	return httptest.NewServer(mux)
}

func paidHandler(t *testing.T, payment *mppserver.Mpp, feePayer bool) http.HandlerFunc {
	t.Helper()

	return func(w http.ResponseWriter, r *http.Request) {
		result, err := payment.Charge(r.Context(), mppserver.ChargeParams{
			Authorization: r.Header.Get("Authorization"),
			Amount:        "1.00",
			FeePayer:      feePayer,
		})
		if err != nil {
			writeIntegrationPaymentError(w, err)
			return
		}

		if result.IsChallenge() {
			w.Header().Set("WWW-Authenticate", result.Challenge.ToAuthenticate(integrationRealm))
			w.WriteHeader(http.StatusPaymentRequired)
			return
		}

		w.Header().Set("Payment-Receipt", result.Receipt.ToPaymentReceipt())
		if err := json.NewEncoder(w).Encode(map[string]any{
			"payer": result.Credential.Source,
			"tx":    result.Receipt.Reference,
		}); err != nil {
			http.Error(w, fmt.Sprintf("encode response: %v", err), http.StatusInternalServerError)
		}
	}
}

func writeIntegrationPaymentError(w http.ResponseWriter, err error) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.Header().Set("Cache-Control", "no-store")

	var paymentError *mpp.PaymentError
	if errors.As(err, &paymentError) {
		w.WriteHeader(paymentError.Status)
		_ = json.NewEncoder(w).Encode(paymentError.ProblemDetails(""))
		return
	}

	w.WriteHeader(http.StatusInternalServerError)
	_ = json.NewEncoder(w).Encode(map[string]any{"detail": err.Error()})
}
