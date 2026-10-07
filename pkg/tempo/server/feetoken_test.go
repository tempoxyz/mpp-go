package chargeserver

import (
	"context"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tempoxyz/mpp-go/pkg/mpp"
	mppserver "github.com/tempoxyz/mpp-go/pkg/server"
	"github.com/tempoxyz/mpp-go/pkg/tempo"
	chargeclient "github.com/tempoxyz/mpp-go/pkg/tempo/client"
	temporpc "github.com/tempoxyz/tempo-go/pkg/client"
	temposigner "github.com/tempoxyz/tempo-go/pkg/signer"
	tempotx "github.com/tempoxyz/tempo-go/pkg/transaction"
)

var (
	pathUSDToken = common.HexToAddress(pathUSDAddress)
	usdceToken   = common.HexToAddress(usdceAddress)
	ousdToken    = common.HexToAddress(ousdAddress)
)

func TestSponsoredOUSDChargeWithDefaultsEndToEnd(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		chainID      int64
		balances     map[common.Address]*big.Int
		wantOffers   []string
		wantFeeToken common.Address
		wantLookups  []common.Address
	}{
		{
			name:         "mainnet pays gas in funded USDC.e",
			chainID:      tempotx.ChainIdMainnet,
			balances:     map[common.Address]*big.Int{usdceToken: big.NewInt(1)},
			wantOffers:   []string{ousdAddress, usdceAddress},
			wantFeeToken: usdceToken,
			wantLookups:  []common.Address{pathUSDToken, usdceToken},
		},
		{
			name:         "mainnet prefers funded pathUSD",
			chainID:      tempotx.ChainIdMainnet,
			balances:     map[common.Address]*big.Int{pathUSDToken: big.NewInt(1), usdceToken: big.NewInt(1)},
			wantOffers:   []string{ousdAddress, usdceAddress},
			wantFeeToken: pathUSDToken,
			wantLookups:  []common.Address{pathUSDToken},
		},
		{
			name:         "moderato pays gas in funded pathUSD",
			chainID:      tempotx.ChainIdModerato,
			balances:     map[common.Address]*big.Int{pathUSDToken: big.NewInt(5)},
			wantOffers:   []string{ousdAddress, pathUSDAddress},
			wantFeeToken: pathUSDToken,
			wantLookups:  []common.Address{pathUSDToken},
		},
		{
			name:         "moderato falls back to first allowed token when unfunded",
			chainID:      tempotx.ChainIdModerato,
			wantOffers:   []string{ousdAddress, pathUSDAddress},
			wantFeeToken: pathUSDToken,
			wantLookups:  []common.Address{pathUSDToken},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			rpc := newOffersRPC(tt.chainID)
			rpc.balances = tt.balances
			intent, err := NewIntent(IntentConfig{Store: tempo.NewMemoryStore(), RPC: rpc, FeePayerPrivateKey: feePayerKey})
			require.NoError(t, err)
			payment := newTestServer(t, NewMethod(MethodConfig{
				Intent:    intent,
				Recipient: testRecipient,
				ChainID:   tt.chainID,
				FeePayer:  true,
			}), testRealm, offersSecret)

			// Sponsored servers advertise every default offer.
			challenges := issueOffers(t, payment, mppserver.ChargeParams{Amount: "0.50"})
			require.Equal(t, checksummed(tt.wantOffers...), challengeCurrencies(challenges))
			request, err := tempo.ParseChargeRequest(challenges[0].Request)
			require.NoError(t, err)
			require.True(t, request.MethodDetails.FeePayer)

			credential := payWithTransaction(t, rpc, challenges[0])
			result, err := payment.Charge(ctx, mppserver.ChargeParams{Amount: "0.50", Authorization: credential.ToAuthorization()})
			require.NoError(t, err)
			require.False(t, result.IsChallenge())
			assert.Equal(t, "success", result.Receipt.Status)

			require.Len(t, rpc.sentRawTxs, 1)
			broadcast, err := tempotx.Deserialize(rpc.sentRawTxs[0])
			require.NoError(t, err)
			assert.Equal(t, tt.wantFeeToken, broadcast.FeeToken)
			assert.Equal(t, []common.Address{ousdToken}, sentTokens(t, rpc.sentRawTxs[0]))
			sender, err := tempotx.VerifySignature(broadcast)
			require.NoError(t, err)
			feePayer, err := tempotx.VerifyFeePayerSignature(broadcast, sender)
			require.NoError(t, err)
			assert.Equal(t, intent.feePayerSigner.Address(), feePayer)
			// The fee token is resolved once, during validation, in allowlist order.
			assert.Equal(t, tt.wantLookups, rpc.balanceCalls)
		})
	}
}

func TestLocalFeePayerAllowedFeeTokens(t *testing.T) {
	t.Parallel()

	chain := func(id int64) tempo.ChargeRequest {
		return tempo.ChargeRequest{MethodDetails: tempo.MethodDetails{ChainID: &id}}
	}
	defaults, err := NewIntent(IntentConfig{Store: tempo.NewMemoryStore(), FeePayerPrivateKey: feePayerKey})
	require.NoError(t, err)

	tests := []struct {
		name    string
		intent  *Intent
		rpc     *mockRPC
		request tempo.ChargeRequest
		want    []common.Address
	}{
		{name: "mainnet defaults", intent: defaults, request: chain(tempotx.ChainIdMainnet), want: []common.Address{pathUSDToken, usdceToken}},
		{name: "moderato defaults", intent: defaults, request: chain(tempotx.ChainIdModerato), want: []common.Address{pathUSDToken}},
		{name: "unknown chain defaults", intent: defaults, request: chain(999999), want: []common.Address{pathUSDToken}},
		{name: "chain from rpc", intent: defaults, rpc: &mockRPC{chainID: tempotx.ChainIdMainnet}, want: []common.Address{pathUSDToken, usdceToken}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			rpc := tt.rpc
			if rpc == nil {
				rpc = &mockRPC{}
			}
			got, err := tt.intent.allowedFeeTokens(context.Background(), rpc, tt.request)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
			assert.NotContains(t, got, ousdToken)
		})
	}

	custom, err := NewIntent(IntentConfig{Store: tempo.NewMemoryStore(),
		FeePayerPrivateKey: feePayerKey,
		FeePayerPolicies: map[string]FeePayerPolicy{
			usdceAddress:   defaultFeePayerPolicy(),
			ousdAddress:    defaultFeePayerPolicy(),
			pathUSDAddress: defaultFeePayerPolicy(),
		},
	})
	require.NoError(t, err)
	got, err := custom.allowedFeeTokens(context.Background(), &mockRPC{}, chain(tempotx.ChainIdModerato))
	require.NoError(t, err)
	// Configured tokens are used on every chain, in address order.
	assert.Equal(t, []common.Address{pathUSDToken, ousdToken, usdceToken}, got)
}

func TestLocalFeePayerResolveFeeToken(t *testing.T) {
	t.Parallel()

	allowed := []common.Address{pathUSDToken, usdceToken}
	tests := []struct {
		name     string
		feeToken string
		balances map[common.Address]*big.Int
		want     common.Address
		lookups  []common.Address
		wantErr  string
	}{
		{name: "first funded token wins", balances: map[common.Address]*big.Int{pathUSDToken: big.NewInt(1), usdceToken: big.NewInt(1)}, want: pathUSDToken, lookups: []common.Address{pathUSDToken}},
		{name: "skips unfunded tokens", balances: map[common.Address]*big.Int{usdceToken: big.NewInt(1)}, want: usdceToken, lookups: []common.Address{pathUSDToken, usdceToken}},
		{name: "none funded uses first allowed", want: pathUSDToken, lookups: []common.Address{pathUSDToken, usdceToken}},
		{name: "zero balance is unfunded", balances: map[common.Address]*big.Int{pathUSDToken: big.NewInt(0)}, want: pathUSDToken, lookups: []common.Address{pathUSDToken, usdceToken}},
		{name: "configured fee token wins without lookups", feeToken: usdceAddress, balances: map[common.Address]*big.Int{pathUSDToken: big.NewInt(1)}, want: usdceToken},
		{name: "configured fee token outside allowlist", feeToken: ousdAddress, wantErr: "fee payer transaction fee token is not supported"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			intent, err := NewIntent(IntentConfig{Store: tempo.NewMemoryStore(), FeePayerPrivateKey: feePayerKey, FeeToken: tt.feeToken})
			require.NoError(t, err)
			rpc := &mockRPC{balances: tt.balances}
			got, err := intent.resolveFeeToken(context.Background(), rpc, allowed)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
			assert.Equal(t, tt.lookups, rpc.balanceCalls)
		})
	}
}

func TestLocalFeePayerFailedBalanceLookupCountsAsUnfunded(t *testing.T) {
	t.Parallel()

	for _, response := range []string{"0xnothex", ""} {
		assert.False(t, hasTokenBalance(context.Background(), &staticCallRPC{result: response}, pathUSDToken, usdceToken))
	}
	assert.True(t, hasTokenBalance(context.Background(), &staticCallRPC{result: "0x" + "00000000000000000000000000000000000000000000000000000000000f4240"}, pathUSDToken, usdceToken))
}

func TestNewIntentRejectsInvalidFeeToken(t *testing.T) {
	t.Parallel()

	_, err := NewIntent(IntentConfig{Store: tempo.NewMemoryStore(), FeeToken: "pathUSD"})
	require.EqualError(t, err, `tempo server: invalid fee token "pathUSD"`)
}

func TestSponsoredChargeConfiguredFeeTokenWins(t *testing.T) {
	t.Parallel()

	rpc, credential, request := sponsoredCredential(t, tempotx.ChainIdMainnet, ousdAddress)
	rpc.balances = map[common.Address]*big.Int{pathUSDToken: big.NewInt(1)}
	intent, err := NewIntent(IntentConfig{Store: tempo.NewMemoryStore(), RPC: rpc, FeePayerPrivateKey: feePayerKey, FeeToken: usdceAddress})
	require.NoError(t, err)

	_, err = intent.Verify(context.Background(), credential, request.Map())
	require.NoError(t, err)
	assert.Equal(t, usdceToken, broadcastFeeToken(t, rpc))
	assert.Empty(t, rpc.balanceCalls)
}

func TestSponsoredChargeRejectsFeeTokenOutsideAllowlist(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		chainID  int64
		feeToken string
	}{
		{name: "OUSD is not a default fee token", chainID: tempotx.ChainIdMainnet, feeToken: ousdAddress},
		{name: "USDC.e is not a default Moderato fee token", chainID: tempotx.ChainIdModerato, feeToken: usdceAddress},
		{name: "AlphaUSD is not a default Moderato fee token", chainID: tempotx.ChainIdModerato, feeToken: tempotx.AlphaUSDAddress.Hex()},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			rpc, credential, request := sponsoredCredential(t, tt.chainID, ousdAddress)
			intent, err := NewIntent(IntentConfig{Store: tempo.NewMemoryStore(), RPC: rpc, FeePayerPrivateKey: feePayerKey, FeeToken: tt.feeToken})
			require.NoError(t, err)

			_, err = intent.Verify(context.Background(), credential, request.Map())
			require.ErrorContains(t, err, "fee payer transaction fee token is not supported")
			assert.Empty(t, rpc.sentRawTxs)
		})
	}
}

func TestSponsoredChargeCustomFeePayerPolicies(t *testing.T) {
	t.Parallel()

	t.Run("configured token is the fee token", func(t *testing.T) {
		t.Parallel()
		rpc, credential, request := sponsoredCredential(t, tempotx.ChainIdMainnet, ousdAddress)
		rpc.balances = map[common.Address]*big.Int{pathUSDToken: big.NewInt(1), usdceToken: big.NewInt(1)}
		intent, err := NewIntent(IntentConfig{Store: tempo.NewMemoryStore(),
			RPC:                rpc,
			FeePayerPrivateKey: feePayerKey,
			FeePayerPolicies:   map[string]FeePayerPolicy{ousdAddress: defaultFeePayerPolicy()},
		})
		require.NoError(t, err)

		_, err = intent.Verify(context.Background(), credential, request.Map())
		require.NoError(t, err)
		assert.Equal(t, ousdToken, broadcastFeeToken(t, rpc))
	})

	t.Run("funded configured token wins", func(t *testing.T) {
		t.Parallel()
		rpc, credential, request := sponsoredCredential(t, tempotx.ChainIdModerato, ousdAddress)
		rpc.balances = map[common.Address]*big.Int{usdceToken: big.NewInt(1)}
		intent, err := NewIntent(IntentConfig{Store: tempo.NewMemoryStore(),
			RPC:                rpc,
			FeePayerPrivateKey: feePayerKey,
			FeePayerPolicies: map[string]FeePayerPolicy{
				pathUSDAddress: defaultFeePayerPolicy(),
				usdceAddress:   defaultFeePayerPolicy(),
			},
		})
		require.NoError(t, err)

		_, err = intent.Verify(context.Background(), credential, request.Map())
		require.NoError(t, err)
		assert.Equal(t, usdceToken, broadcastFeeToken(t, rpc))
	})

	t.Run("fee token policy limits apply", func(t *testing.T) {
		t.Parallel()
		rpc, credential, request := sponsoredCredential(t, tempotx.ChainIdMainnet, ousdAddress)
		intent, err := NewIntent(IntentConfig{Store: tempo.NewMemoryStore(),
			RPC:                rpc,
			FeePayerPrivateKey: feePayerKey,
			FeePayerPolicies: map[string]FeePayerPolicy{usdceAddress: {
				MaxFeePerGas:         big.NewInt(1),
				MaxPriorityFeePerGas: big.NewInt(1),
				MaxTotalFee:          big.NewInt(1),
			}},
		})
		require.NoError(t, err)

		_, err = intent.Verify(context.Background(), credential, request.Map())
		require.ErrorContains(t, err, "sponsor policy")
		assert.Empty(t, rpc.sentRawTxs)
	})
}

func TestSponsoredDefaultsEmitEveryOffer(t *testing.T) {
	t.Parallel()

	for _, config := range []MethodConfig{
		{ChainID: tempotx.ChainIdMainnet, FeePayer: true},
		{ChainID: tempotx.ChainIdMainnet, FeePayerURL: "https://fee-payer.example.com"},
	} {
		config.Recipient = testRecipient
		method := NewMethod(config)
		assert.Equal(t, checksummed(ousdAddress, usdceAddress), offerCurrencies(t, method, mppserver.ChargeParams{Amount: "1"}, tempotx.ChainIdMainnet))
	}
	moderato := NewMethod(MethodConfig{ChainID: tempotx.ChainIdModerato, FeePayer: true, Recipient: testRecipient})
	assert.Equal(t, checksummed(ousdAddress, pathUSDAddress), offerCurrencies(t, moderato, mppserver.ChargeParams{Amount: "1", FeePayer: true}, tempotx.ChainIdModerato))
}

// sponsoredCredential builds a sponsored charge request for currency and a
// matching client transaction credential.
func sponsoredCredential(t *testing.T, chainID int64, currency string) (*mockRPC, *mpp.Credential, tempo.ChargeRequest) {
	t.Helper()
	request, err := tempo.NormalizeChargeRequest(tempo.ChargeRequestParams{
		Amount:    "0.50",
		Currency:  currency,
		Recipient: testRecipient,
		Decimals:  6,
		ChainID:   chainID,
		FeePayer:  true,
	})
	require.NoError(t, err)
	rpc := newOffersRPC(chainID)
	return rpc, payWithTransaction(t, rpc, buildChallenge(t, request)), request
}

func payWithTransaction(t *testing.T, rpc *mockRPC, challenge *mpp.Challenge) *mpp.Credential {
	t.Helper()
	method, err := chargeclient.New(chargeclient.Config{PaymentPolicy: func(context.Context, tempo.ChargeRequest) error { return nil },
		PrivateKey:     testPrivateKey,
		RPC:            rpc,
		ChainID:        int64(rpc.chainID),
		CredentialType: tempo.CredentialTypeTransaction,
	})
	require.NoError(t, err)
	credential, err := method.CreateCredential(context.Background(), challenge)
	require.NoError(t, err)
	return credential
}

func broadcastFeeToken(t *testing.T, rpc *mockRPC) common.Address {
	t.Helper()
	require.Len(t, rpc.sentRawTxs, 1)
	broadcast, err := tempotx.Deserialize(rpc.sentRawTxs[0])
	require.NoError(t, err)
	return broadcast.FeeToken
}

// staticCallRPC answers every eth_call with a fixed result.
type staticCallRPC struct {
	mockRPC
	result string
}

func (r *staticCallRPC) SendRequest(context.Context, string, ...interface{}) (*temporpc.JSONRPCResponse, error) {
	return &temporpc.JSONRPCResponse{Result: r.result}, nil
}

func TestFeeTokenRequiresLocalFeePayer(t *testing.T) {
	for _, url := range []string{"", "https://sponsor.example.test"} {
		t.Run(url, func(t *testing.T) {
			_, err := MethodFromConfig(Config{FeeToken: usdceAddress, FeePayerURL: url})
			require.ErrorContains(t, err, "FeeToken requires a local fee payer")
		})
	}
}

func TestRemoteSponsoredOUSDUsesIndependentFeeToken(t *testing.T) {
	for _, tt := range []struct {
		name     string
		chainID  int64
		feeToken common.Address
	}{
		{"mainnet USDC.e", tempotx.ChainIdMainnet, usdceToken},
		{"mainnet pathUSD", tempotx.ChainIdMainnet, pathUSDToken},
		{"moderato pathUSD", tempotx.ChainIdModerato, pathUSDToken},
	} {
		t.Run(tt.name, func(t *testing.T) {
			signer, err := temposigner.NewSigner(feePayerKey)
			require.NoError(t, err)
			calls := 0
			sponsor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				var body struct {
					Method string
					Params []string
				}
				require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
				require.Equal(t, "eth_signRawTransaction", body.Method)
				require.Len(t, body.Params, 1)
				tx, err := tempotx.Deserialize(body.Params[0])
				require.NoError(t, err)
				tx.From, err = tempotx.VerifySignature(tx)
				require.NoError(t, err)
				tx.FeeToken = tt.feeToken
				tx.AwaitingFeePayer = false
				require.NoError(t, tempotx.AddFeePayerSignature(tx, signer))
				raw, err := tempotx.Serialize(tx, nil)
				require.NoError(t, err)
				require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": raw}))
			}))
			defer sponsor.Close()
			rpc := newOffersRPC(tt.chainID)
			intent, err := NewIntent(IntentConfig{Store: tempo.NewMemoryStore(), RPC: rpc})
			require.NoError(t, err)
			payment := newTestServer(t, NewMethod(MethodConfig{
				Intent: intent, Recipient: testRecipient, ChainID: tt.chainID,
				FeePayer: true, FeePayerURL: sponsor.URL,
			}), testRealm, offersSecret)
			offers := issueOffers(t, payment, mppserver.ChargeParams{Amount: "0.50"})
			require.Equal(t, checksummed(ousdAddress)[0], offers[0].Request["currency"])
			credential := payWithTransaction(t, rpc, offers[0])
			result, err := payment.Charge(context.Background(), mppserver.ChargeParams{
				Amount: "0.50", Authorization: credential.ToAuthorization(),
			})
			require.NoError(t, err)
			require.False(t, result.IsChallenge())
			assert.Equal(t, 1, calls)
			assert.Equal(t, tt.feeToken, broadcastFeeToken(t, rpc))
			assert.Equal(t, []common.Address{ousdToken}, sentTokens(t, rpc.sentRawTxs[0]))
		})
	}
}
