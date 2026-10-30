//go:build integration
// +build integration

package tests

import (
	"context"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	temposigner "github.com/tempoxyz/tempo-go/pkg/signer"
	tempotx "github.com/tempoxyz/tempo-go/pkg/transaction"

	"github.com/tempoxyz/mpp-go/pkg/client"
	"github.com/tempoxyz/mpp-go/pkg/tempo"
	chargeclient "github.com/tempoxyz/mpp-go/pkg/tempo/client"
	chargeserver "github.com/tempoxyz/mpp-go/pkg/tempo/server"
)

// hostedSponsor is a hosted fee payer that co-signs, with a funded key, the
// transaction an eth_fillTransaction request describes.
func hostedSponsor(t *testing.T, chainID uint64, sponsor *temposigner.Signer, feeToken common.Address) (*httptest.Server, func() []http.Header) {
	t.Helper()
	var mu sync.Mutex
	var headers []http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Method string           `json:"method"`
			Params []map[string]any `json:"params"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		require.Equal(t, "eth_fillTransaction", body.Method)
		mu.Lock()
		headers = append(headers, r.Header.Clone())
		mu.Unlock()
		params := body.Params[0]
		quantity := func(name string) *big.Int {
			if value, ok := params[name].(string); ok {
				return hexutil.MustDecodeBig(value)
			}
			return new(big.Int)
		}
		tx := tempotx.New()
		tx.ChainID = new(big.Int).SetUint64(chainID)
		tx.Gas = quantity("gas").Uint64()
		tx.MaxFeePerGas = quantity("maxFeePerGas")
		tx.MaxPriorityFeePerGas = quantity("maxPriorityFeePerGas")
		tx.Nonce = quantity("nonce").Uint64()
		tx.NonceKey = quantity("nonceKey")
		tx.ValidBefore = quantity("validBefore").Uint64()
		tx.ValidAfter = quantity("validAfter").Uint64()
		tx.FeeToken = feeToken
		for _, raw := range params["calls"].([]any) {
			call := raw.(map[string]any)
			to := common.HexToAddress(call["to"].(string))
			value := new(big.Int)
			if v := call["value"].(string); v != "0x" {
				value = hexutil.MustDecodeBig(v)
			}
			data, _ := call["data"].(string)
			tx.Calls = append(tx.Calls, tempotx.Call{To: &to, Value: value, Data: common.FromHex(data)})
		}
		hash, err := tempotx.GetFeePayerSignPayload(tx, common.HexToAddress(params["from"].(string)))
		require.NoError(t, err)
		signature, err := sponsor.Sign(hash)
		require.NoError(t, err)
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{"tx": map[string]any{
			"feePayerSignature": map[string]any{
				"r": hexutil.EncodeBig(signature.R), "s": hexutil.EncodeBig(signature.S),
				"yParity": hexutil.EncodeUint64(uint64(signature.YParity)),
			},
			"feeToken": feeToken.Hex(),
		}}})
	}))
	t.Cleanup(srv.Close)
	return srv, func() []http.Header {
		mu.Lock()
		defer mu.Unlock()
		return headers
	}
}

// A hosted fee payer configured on the server sponsors a route by default:
// the server builds the sponsored transaction from the sponsor's
// eth_fillTransaction signature and the node accepts it.
func TestIntegrationChargeFlow_HostedFeePayer(t *testing.T) {
	rpcURL := integrationRPCURL(t)
	rpc := tempo.NewRPCClient(rpcURL)
	ctx := context.Background()
	chainID := waitForRPC(t, ctx, rpc)
	payer := newSigner(t)
	sponsor := newSigner(t)
	fundAddress(t, ctx, rpc, payer.Address())
	fundAddress(t, ctx, rpc, sponsor.Address())

	hosted, headers := hostedSponsor(t, chainID, sponsor, common.HexToAddress(integrationCurrency))
	method, err := chargeserver.New(chargeserver.Config{
		Store:      tempo.NewMemoryStore(),
		RPCURL:     rpcURL,
		Currencies: []string{integrationCurrency},
		Recipient:  integrationRecipient,
		ChainID:    int64(chainID),
		HostedFeePayer: &chargeserver.HostedFeePayer{
			URL: hosted.URL, Headers: map[string]string{"Authorization": "Bearer sponsor"},
		},
	})
	require.NoError(t, err)
	payment := newTestServer(t, method, integrationRealm, integrationSecretKey)
	mux := http.NewServeMux()
	mux.HandleFunc("/paid", paidHandler(t, payment, true))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	clientMethod, err := chargeclient.New(chargeclient.Config{
		PaymentPolicy: func(context.Context, tempo.ChargeRequest) error { return nil },
		Signer:        payer,
		RPCURL:        rpcURL,
		ChainID:       int64(chainID),
	})
	require.NoError(t, err)
	response, err := client.New([]client.Method{clientMethod}).Get(ctx, srv.URL+"/paid")
	require.NoError(t, err)
	defer response.Body.Close()
	require.Equal(t, http.StatusOK, response.StatusCode)
	require.NotEmpty(t, response.Header.Get("Payment-Receipt"))
	sent := headers()
	require.Len(t, sent, 1)
	assert.Equal(t, "Bearer sponsor", sent[0].Get("Authorization"))
}
