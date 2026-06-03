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
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
	if !assert.NoErrorf(t, err,
		"tempo/client.New() error = %v", err) {
		return
	}

	transport := &captureTransport{inner: http.DefaultTransport}
	client := mppclient.New(
		[]mppclient.Method{clientMethod},
		mppclient.WithHTTPClient(&http.Client{Transport: transport}),
	)
	response, err := client.Get(ctx, server.URL+"/paid")
	if !assert.NoErrorf(t, err,
		"client.Get() error = %v", err) {
		return
	}

	defer response.Body.Close()
	if !assert.Equalf(t, http.StatusOK, response.StatusCode,
		"status = %d, want %d", response.StatusCode, http.StatusOK) {
		return
	}

	receiptHeader := response.Header.Get("Payment-Receipt")
	if !assert.NotEqual(t, "", receiptHeader,
		"Payment-Receipt header missing") {
		return
	}

	receipt, err := mpp.ParsePaymentReceipt(receiptHeader)
	if !assert.NoErrorf(t, err,
		"ParsePaymentReceipt() error = %v", err) {
		return
	}
	if !assert.Equalf(t, "success", receipt.Status,
		"receipt.Status = %q, want success", receipt.Status) {
		return
	}
	if !assert.Equalf(t, tempo.MethodName, receipt.Method,
		"receipt.Method = %q, want %q", receipt.Method, tempo.MethodName) {
		return
	}
	if !assert.NotEqual(t, "", receipt.Reference,
		"receipt.Reference is empty") {
		return
	}

	var body struct {
		Payer string `json:"payer"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		assert.Failf(t, "", "Decode(response.Body) error = %v", err)
		return
	}
	if !assert.NotEqual(t, "", body.Payer,
		"response payer is empty") {
		return
	}

	authorization := transport.Authorization()
	if !assert.NotEqual(t, "", authorization,
		"retry authorization header missing") {
		return
	}

	credential, err := mpp.ParseCredential(authorization)
	if !assert.NoErrorf(t, err,
		"ParseCredential() error = %v", err) {
		return
	}
	if !assert.Equalf(t, string(tempo.CredentialTypeTransaction), credential.Payload["type"],
		"credential payload type = %#v, want transaction", credential.Payload["type"]) {
		return
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
	if !assert.NoErrorf(t, err,
		"tempo/client.New() error = %v", err) {
		return
	}

	transport := &captureTransport{inner: http.DefaultTransport}
	client := mppclient.New(
		[]mppclient.Method{clientMethod},
		mppclient.WithHTTPClient(&http.Client{Transport: transport}),
	)
	response, err := client.Get(ctx, server.URL+"/paid-fee-payer")
	if !assert.NoErrorf(t, err,
		"client.Get() error = %v", err) {
		return
	}

	defer response.Body.Close()
	if !assert.Equalf(t, http.StatusOK, response.StatusCode,
		"status = %d, want %d", response.StatusCode, http.StatusOK) {
		return
	}
	if !assert.NotEqual(t, "", response.Header.Get("Payment-Receipt"),
		"Payment-Receipt header missing") {
		return
	}

	authorization := transport.Authorization()
	if !assert.NotEqual(t, "", authorization,
		"retry authorization header missing") {
		return
	}

	credential, err := mpp.ParseCredential(authorization)
	if !assert.NoErrorf(t, err,
		"ParseCredential() error = %v", err) {
		return
	}
	if !assert.Equalf(t, string(tempo.CredentialTypeTransaction), credential.Payload["type"],
		"credential payload type = %#v, want transaction", credential.Payload["type"]) {
		return
	}

	signature, _ := credential.Payload["signature"].(string)
	if !assert.Containsf(t, signature, "feefeefeefee",
		"expected fee payer marker in credential payload, got %q", signature) {
		return
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
	if !assert.NoErrorf(t, err,
		"tempo/client.New() error = %v", err) {
		return
	}

	transport := &captureTransport{inner: http.DefaultTransport}
	client := mppclient.New(
		[]mppclient.Method{clientMethod},
		mppclient.WithHTTPClient(&http.Client{Transport: transport}),
	)
	response, err := client.Get(ctx, server.URL+"/paid-hash")
	if !assert.NoErrorf(t, err,
		"client.Get() error = %v", err) {
		return
	}

	defer response.Body.Close()
	if !assert.Equalf(t, http.StatusOK, response.StatusCode,
		"status = %d, want %d", response.StatusCode, http.StatusOK) {
		return
	}
	if !assert.NotEqual(t, "", response.Header.Get("Payment-Receipt"),
		"Payment-Receipt header missing") {
		return
	}

	authorization := transport.Authorization()
	if !assert.NotEqual(t, "", authorization,
		"retry authorization header missing") {
		return
	}

	credential, err := mpp.ParseCredential(authorization)
	if !assert.NoErrorf(t, err,
		"ParseCredential() error = %v", err) {
		return
	}
	if !assert.Equalf(t, string(tempo.CredentialTypeHash), credential.Payload["type"],
		"credential payload type = %#v, want hash", credential.Payload["type"]) {
		return
	}

	if hash, _ := credential.Payload["hash"].(string); hash == "" {
		assert.Fail(t, "hash credential payload is empty")
		return
	}

	replayRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/paid-hash", nil)
	if !assert.NoErrorf(t, err,
		"NewRequestWithContext() error = %v", err) {
		return
	}

	replayRequest.Header.Set("Authorization", authorization)
	replayResponse, err := http.DefaultClient.Do(replayRequest)
	if !assert.NoErrorf(t, err,
		"replay request error = %v", err) {
		return
	}

	defer replayResponse.Body.Close()
	if !assert.Equalf(t, http.StatusPaymentRequired, replayResponse.StatusCode,
		"replay status = %d, want %d", replayResponse.StatusCode, http.StatusPaymentRequired) {
		return
	}

	body, err := io.ReadAll(replayResponse.Body)
	if !assert.NoErrorf(t, err,
		"ReadAll(replayResponse.Body) error = %v", err) {
		return
	}
	if !assert.Containsf(t, string(body), "already used",
		"replay response body = %q, want replay protection error", string(body)) {
		return
	}

}

func TestIntegrationChargeFlow_HashCredentialRequiresSource(t *testing.T) {
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
	if !assert.NoErrorf(t, err,
		"tempo/client.New() error = %v", err) {
		return
	}

	transport := &captureTransport{inner: http.DefaultTransport}
	client := mppclient.New(
		[]mppclient.Method{clientMethod},
		mppclient.WithHTTPClient(&http.Client{Transport: transport}),
	)
	response, err := client.Get(ctx, server.URL+"/paid-hash")
	if !assert.NoErrorf(t, err,
		"client.Get() error = %v", err) {
		return
	}

	defer response.Body.Close()
	if !assert.Equalf(t, http.StatusOK, response.StatusCode,
		"status = %d, want %d", response.StatusCode, http.StatusOK) {
		return
	}

	authorization := transport.Authorization()
	credential, err := mpp.ParseCredential(authorization)
	if !assert.NoErrorf(t, err,
		"ParseCredential() error = %v", err) {
		return
	}

	credential.Source = ""
	replayRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/paid-hash", nil)
	if !assert.NoErrorf(t, err,
		"NewRequestWithContext() error = %v", err) {
		return
	}

	replayRequest.Header.Set("Authorization", credential.ToAuthorization())
	replayResponse, err := http.DefaultClient.Do(replayRequest)
	if !assert.NoErrorf(t, err,
		"sourceless request error = %v", err) {
		return
	}

	defer replayResponse.Body.Close()
	if !assert.NotEqual(t, http.StatusOK, replayResponse.StatusCode,
		"expected hash credential without source to be rejected") {
		return
	}

	body, err := io.ReadAll(replayResponse.Body)
	if !assert.NoErrorf(t, err,
		"ReadAll(replayResponse.Body) error = %v", err) {
		return
	}
	if !assert.Containsf(t, string(body), "source",
		"response body = %q, want source-related error", string(body)) {
		return
	}

}

func TestIntegrationChargeFlow_ProofCredentialZeroAmount(t *testing.T) {
	rpcURL := integrationRPCURL(t)
	rpc := tempo.NewRPCClient(rpcURL)
	ctx := context.Background()
	chainID := waitForRPC(t, ctx, rpc)
	payerSigner := newSigner(t)

	server := newPaidServer(t, rpcURL, chainID, nil)
	defer server.Close()

	clientMethod, err := chargeclient.New(chargeclient.Config{
		Signer:  payerSigner,
		RPCURL:  rpcURL,
		ChainID: int64(chainID),
	})
	if !assert.NoErrorf(t, err,
		"tempo/client.New() error = %v", err) {
		return
	}

	transport := &captureTransport{inner: http.DefaultTransport}
	client := mppclient.New(
		[]mppclient.Method{clientMethod},
		mppclient.WithHTTPClient(&http.Client{Transport: transport}),
	)
	response, err := client.Get(ctx, server.URL+"/paid-proof")
	if !assert.NoErrorf(t, err,
		"client.Get() error = %v", err) {
		return
	}

	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		assert.Failf(t, "", "status = %d, want %d, body = %s", response.StatusCode, http.StatusOK, body)
		return
	}
	receiptHeader := response.Header.Get("Payment-Receipt")
	if !assert.NotEqual(t, "", receiptHeader,
		"Payment-Receipt header missing") {
		return
	}

	receipt, err := mpp.ParsePaymentReceipt(receiptHeader)
	if !assert.NoErrorf(t, err,
		"ParsePaymentReceipt() error = %v", err) {
		return
	}
	if !assert.Equalf(t, "success", receipt.Status,
		"receipt.Status = %q, want success", receipt.Status) {
		return
	}

	authorization := transport.Authorization()
	credential, err := mpp.ParseCredential(authorization)
	if !assert.NoErrorf(t, err,
		"ParseCredential() error = %v", err) {
		return
	}
	if !assert.Equalf(t, string(tempo.CredentialTypeProof), credential.Payload["type"],
		"credential payload type = %#v, want proof", credential.Payload["type"]) {
		return
	}

}

func TestIntegrationChargeFlow_ProofCredentialReplayProtected(t *testing.T) {
	rpcURL := integrationRPCURL(t)
	rpc := tempo.NewRPCClient(rpcURL)
	ctx := context.Background()
	chainID := waitForRPC(t, ctx, rpc)
	payerSigner := newSigner(t)

	server := newPaidServer(t, rpcURL, chainID, nil)
	defer server.Close()

	clientMethod, err := chargeclient.New(chargeclient.Config{
		Signer:  payerSigner,
		RPCURL:  rpcURL,
		ChainID: int64(chainID),
	})
	if !assert.NoErrorf(t, err,
		"tempo/client.New() error = %v", err) {
		return
	}

	transport := &captureTransport{inner: http.DefaultTransport}
	client := mppclient.New(
		[]mppclient.Method{clientMethod},
		mppclient.WithHTTPClient(&http.Client{Transport: transport}),
	)
	response, err := client.Get(ctx, server.URL+"/paid-proof")
	if !assert.NoErrorf(t, err,
		"client.Get() error = %v", err) {
		return
	}

	defer response.Body.Close()
	if !assert.Equalf(t, http.StatusOK, response.StatusCode,
		"status = %d, want %d", response.StatusCode, http.StatusOK) {
		return
	}

	authorization := transport.Authorization()
	if !assert.NotEqual(t, "", authorization,
		"retry authorization header missing") {
		return
	}

	replayRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/paid-proof", nil)
	if !assert.NoErrorf(t, err,
		"NewRequestWithContext() error = %v", err) {
		return
	}

	replayRequest.Header.Set("Authorization", authorization)
	replayResponse, err := http.DefaultClient.Do(replayRequest)
	if !assert.NoErrorf(t, err,
		"replay request error = %v", err) {
		return
	}

	defer replayResponse.Body.Close()
	if !assert.Equalf(t, http.StatusPaymentRequired, replayResponse.StatusCode,
		"replay status = %d, want %d", replayResponse.StatusCode, http.StatusPaymentRequired) {
		return
	}

	body, err := io.ReadAll(replayResponse.Body)
	if !assert.NoErrorf(t, err,
		"ReadAll(replayResponse.Body) error = %v", err) {
		return
	}
	if !assert.Containsf(t, string(body), "already used",
		"replay response body = %q, want replay protection error", string(body)) {
		return
	}

}

func TestIntegrationChargeFlow_TransactionCredentialWithSplits(t *testing.T) {
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
	if !assert.NoErrorf(t, err,
		"tempo/client.New() error = %v", err) {
		return
	}

	transport := &captureTransport{inner: http.DefaultTransport}
	client := mppclient.New(
		[]mppclient.Method{clientMethod},
		mppclient.WithHTTPClient(&http.Client{Transport: transport}),
	)
	response, err := client.Get(ctx, server.URL+"/paid-splits")
	if !assert.NoErrorf(t, err,
		"client.Get() error = %v", err) {
		return
	}

	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		assert.Failf(t, "", "status = %d, want %d, body = %s", response.StatusCode, http.StatusOK, body)
		return
	}
	receiptHeader := response.Header.Get("Payment-Receipt")
	if !assert.NotEqual(t, "", receiptHeader,
		"Payment-Receipt header missing") {
		return
	}

	receipt, err := mpp.ParsePaymentReceipt(receiptHeader)
	if !assert.NoErrorf(t, err,
		"ParsePaymentReceipt() error = %v", err) {
		return
	}
	if !assert.Equalf(t, "success", receipt.Status,
		"receipt.Status = %q, want success", receipt.Status) {
		return
	}
	if !assert.NotEqual(t, "", receipt.Reference,
		"receipt.Reference is empty") {
		return
	}

	var body struct {
		ExternalID string `json:"externalId"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		assert.Failf(t, "", "Decode(response.Body) error = %v", err)
		return
	}
	if !assert.Equalf(t, "ext-splits-123", body.ExternalID,
		"externalId = %q, want ext-splits-123", body.ExternalID) {
		return
	}

}

func TestIntegrationChargeFlow_ReceiptPropagatesExternalID(t *testing.T) {
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
	if !assert.NoErrorf(t, err,
		"tempo/client.New() error = %v", err) {
		return
	}

	transport := &captureTransport{inner: http.DefaultTransport}
	client := mppclient.New(
		[]mppclient.Method{clientMethod},
		mppclient.WithHTTPClient(&http.Client{Transport: transport}),
	)
	response, err := client.Get(ctx, server.URL+"/paid-external-id")
	if !assert.NoErrorf(t, err,
		"client.Get() error = %v", err) {
		return
	}

	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		assert.Failf(t, "", "status = %d, want %d, body = %s", response.StatusCode, http.StatusOK, body)
		return
	}
	receiptHeader := response.Header.Get("Payment-Receipt")
	if !assert.NotEqual(t, "", receiptHeader,
		"Payment-Receipt header missing") {
		return
	}

	receipt, err := mpp.ParsePaymentReceipt(receiptHeader)
	if !assert.NoErrorf(t, err,
		"ParsePaymentReceipt() error = %v", err) {
		return
	}
	if !assert.Equalf(t, "ext-integration-123", receipt.ExternalID,
		"receipt.ExternalID = %q, want ext-integration-123", receipt.ExternalID) {
		return
	}
	if !assert.NotEqual(t, "", receipt.Reference,
		"receipt.Reference is empty") {
		return
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
	require.Failf(t, "", "Tempo RPC at %s did not become ready before timeout; start the local devnet with `docker compose up -d` in mpp-go or set TEMPO_RPC_URL", integrationRPCURL(t))
	return *new(uint64)
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
	if !assert.NoErrorf(t, err,
		"NewSigner(dev key) error = %v", err) {
		return
	}

	chainID, err := rpc.GetChainID(ctx)
	if !assert.NoErrorf(t, err,
		"GetChainID(funding tx) error = %v", err) {
		return
	}

	gasPrice := mustGasPrice(t, ctx, rpc)
	nonce, err := rpc.GetTransactionCount(ctx, devSigner.Address().Hex())
	if !assert.NoErrorf(t, err,
		"GetTransactionCount(dev signer) error = %v", err) {
		return
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
	if !assert.NoErrorf(t, err,
		"BuildAndValidate(funding tx) error = %v", err) {
		return
	}

	if err := tempotx.SignTransaction(tx, devSigner); err != nil {
		assert.Failf(t, "", "SignTransaction(funding tx) error = %v", err)
		return
	}
	serialized, err := tempotx.Serialize(tx, nil)
	if !assert.NoErrorf(t, err,
		"Serialize(funding tx) error = %v", err) {
		return
	}

	hash, err := rpc.SendRawTransaction(ctx, serialized)
	if !assert.NoErrorf(t, err,
		"SendRawTransaction(funding tx) error = %v", err) {
		return
	}

	waitForReceipt(t, ctx, rpc, hash)
	waitForTokenBalance(t, ctx, rpc, address, integrationFundingAmount)
}

func mustGasPrice(t *testing.T, ctx context.Context, rpc tempo.RPCClient) *big.Int {
	t.Helper()

	response, err := rpc.SendRequest(ctx, "eth_gasPrice")
	if !assert.NoErrorf(t, err,
		"eth_gasPrice error = %v", err) {
		return *new(*big.Int)
	}

	if err := response.CheckError(); err != nil {
		assert.Failf(t, "", "eth_gasPrice rpc error = %v", err)
		return *new(*big.Int)

	}
	value, ok := response.Result.(string)
	if !assert.Truef(t, ok,
		"eth_gasPrice result type = %T, want string", response.Result) {
		return *new(*big.Int)
	}

	parsed, err := tempo.ParseHexBigInt(value)
	if !assert.NoErrorf(t, err,
		"ParseHexBigInt(eth_gasPrice) error = %v", err) {
		return *new(*big.Int)
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
	if !assert.NoErrorf(t, err,
		"eth_estimateGas error = %v", err) {
		return *new(uint64)
	}

	if err := response.CheckError(); err != nil {
		assert.Failf(t, "", "eth_estimateGas rpc error = %v", err)
		return *new(uint64)

	}
	value, ok := response.Result.(string)
	if !assert.Truef(t, ok,
		"eth_estimateGas result type = %T, want string", response.Result) {
		return *new(uint64)
	}

	estimated, err := tempo.ParseHexUint64(value)
	if !assert.NoErrorf(t, err,
		"ParseHexUint64(eth_estimateGas) error = %v", err) {
		return *new(uint64)
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
	assert.Failf(t, "", "token balance for %s did not reach %s", address.Hex(), minimum.String())
	return
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
					if !assert.Equalf(t, "0x1", receipt["status"],
						"receipt status for %s = %#v, want 0x1", hash, receipt["status"]) {
						return *new(map[string]any)
					}

					return receipt
				}
			}
		}
		time.Sleep(receiptPollingInterval)
	}
	assert.Failf(t, "", "transaction receipt not found for %s", hash)
	return *new(map[string]any)

	return nil
}

func newSigner(t *testing.T) *temposigner.Signer {
	t.Helper()
	privateKey, err := crypto.GenerateKey()
	if !assert.NoErrorf(t, err,
		"GenerateKey() error = %v", err) {
		return *new(*temposigner.Signer)
	}

	return temposigner.NewSignerFromKey(privateKey)
}

func newPaidServer(t *testing.T, rpcURL string, chainID uint64, feePayerSigner *temposigner.Signer) *httptest.Server {
	t.Helper()

	intent, err := chargeserver.NewIntent(chargeserver.IntentConfig{
		RPCURL:         rpcURL,
		FeePayerSigner: feePayerSigner,
	})
	if !assert.NoErrorf(t, err,
		"NewIntent() error = %v", err) {
		return *new(*httptest.Server)
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

	proofMethod := chargeserver.NewMethod(chargeserver.MethodConfig{
		Intent:    intent,
		Currency:  integrationCurrency,
		Recipient: integrationRecipient,
		ChainID:   int64(chainID),
	})
	splitsMethod := chargeserver.NewMethod(chargeserver.MethodConfig{
		Intent:    intent,
		Currency:  integrationCurrency,
		Recipient: integrationRecipient,
		ChainID:   int64(chainID),
	})
	proof := mppserver.New(proofMethod, integrationRealm, integrationSecretKey)
	splits := mppserver.New(splitsMethod, integrationRealm, integrationSecretKey)

	mux := http.NewServeMux()
	mux.HandleFunc("/paid", paidHandler(t, basic, false))
	mux.HandleFunc("/paid-fee-payer", paidHandler(t, feePayer, true))
	mux.HandleFunc("/paid-hash", paidHandler(t, hash, false))
	mux.HandleFunc("/paid-proof", paidHandlerWithAmount(t, proof, "0"))
	mux.HandleFunc("/paid-splits", paidHandlerWithSplitsAndExternalID(t, splits))
	mux.HandleFunc("/paid-external-id", paidHandlerWithExternalID(t, basic, "1.00", "ext-integration-123"))
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

func paidHandlerWithAmount(t *testing.T, payment *mppserver.Mpp, amount string) http.HandlerFunc {
	t.Helper()

	return func(w http.ResponseWriter, r *http.Request) {
		result, err := payment.Charge(r.Context(), mppserver.ChargeParams{
			Authorization: r.Header.Get("Authorization"),
			Amount:        amount,
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

func paidHandlerWithSplitsAndExternalID(t *testing.T, payment *mppserver.Mpp) http.HandlerFunc {
	t.Helper()

	return func(w http.ResponseWriter, r *http.Request) {
		result, err := payment.Charge(r.Context(), mppserver.ChargeParams{
			Authorization: r.Header.Get("Authorization"),
			Amount:        "1.00",
			ExternalID:    "ext-splits-123",
			Splits: []tempo.SplitParams{
				{Amount: "0.10", Recipient: integrationRecipient},
			},
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
			"payer":      result.Credential.Source,
			"tx":         result.Receipt.Reference,
			"externalId": result.Receipt.ExternalID,
		}); err != nil {
			http.Error(w, fmt.Sprintf("encode response: %v", err), http.StatusInternalServerError)
		}
	}
}

func paidHandlerWithExternalID(t *testing.T, payment *mppserver.Mpp, amount, externalID string) http.HandlerFunc {
	t.Helper()

	return func(w http.ResponseWriter, r *http.Request) {
		result, err := payment.Charge(r.Context(), mppserver.ChargeParams{
			Authorization: r.Header.Get("Authorization"),
			Amount:        amount,
			ExternalID:    externalID,
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
			"payer":      result.Credential.Source,
			"tx":         result.Receipt.Reference,
			"externalId": result.Receipt.ExternalID,
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
