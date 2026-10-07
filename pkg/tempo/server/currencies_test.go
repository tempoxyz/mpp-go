package chargeserver

import (
	"context"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tempoxyz/mpp-go/pkg/mpp"
	mppserver "github.com/tempoxyz/mpp-go/pkg/server"
	"github.com/tempoxyz/mpp-go/pkg/tempo"
	chargeclient "github.com/tempoxyz/mpp-go/pkg/tempo/client"
	tempotx "github.com/tempoxyz/tempo-go/pkg/transaction"
)

const (
	offersSecret = "test-secret-key-minimum-32-byte-secret"
	// usdceAddress is USDC.e on Tempo mainnet, spelled out to pin the constant.
	usdceAddress = "0x20C000000000000000000000b9537d11c60E8b50"
	// ousdAddress is OUSD on Tempo mainnet and Moderato, spelled out to pin the constant.
	ousdAddress = "0x20c0000000000000000000006a37DA5C996874BE"
	// pathUSDAddress is pathUSD on Tempo networks, spelled out to pin the constant.
	pathUSDAddress = "0x20c0000000000000000000000000000000000000"
)

func TestCurrencyConstants(t *testing.T) {
	t.Parallel()

	assert.Equal(t, ousdAddress, tempo.OUSDAddress)
	assert.Equal(t, pathUSDAddress, tempo.PathUSDAddress)
	assert.Equal(t, usdceAddress, tempo.MainnetUSDCAddress)
}

func TestNewMethodDefaultCurrencies(t *testing.T) {
	t.Parallel()

	moderatoIntent, err := NewIntent(IntentConfig{Store: tempo.NewMemoryStore(), RPCURL: tempotx.RpcUrlModerato})
	require.NoError(t, err)
	mainnetIntent, err := NewIntent(IntentConfig{Store: tempo.NewMemoryStore(), RPCURL: tempotx.RpcUrlMainnet})
	require.NoError(t, err)
	customIntent, err := NewIntent(IntentConfig{Store: tempo.NewMemoryStore(), RPCURL: "https://rpc.example.com"})
	require.NoError(t, err)

	tests := []struct {
		name        string
		config      MethodConfig
		wantChainID int64
		want        []string
	}{
		{
			name:        "mainnet chain id offers OUSD then USDC.e",
			config:      MethodConfig{Intent: testStoredIntent(), ChainID: tempotx.ChainIdMainnet},
			wantChainID: tempotx.ChainIdMainnet,
			want:        []string{ousdAddress, usdceAddress},
		},
		{
			name:        "moderato chain id offers OUSD then pathUSD",
			config:      MethodConfig{Intent: testStoredIntent(), ChainID: tempotx.ChainIdModerato},
			wantChainID: tempotx.ChainIdModerato,
			want:        []string{ousdAddress, pathUSDAddress},
		},
		{
			name:        "chain inferred from moderato rpc url",
			config:      MethodConfig{Intent: moderatoIntent},
			wantChainID: tempotx.ChainIdModerato,
			want:        []string{ousdAddress, pathUSDAddress},
		},
		{
			name:        "chain inferred from mainnet rpc url",
			config:      MethodConfig{Intent: mainnetIntent},
			wantChainID: tempotx.ChainIdMainnet,
			want:        []string{ousdAddress, usdceAddress},
		},
		{
			name:        "explicit chain id beats rpc url inference",
			config:      MethodConfig{Intent: moderatoIntent, ChainID: tempotx.ChainIdMainnet},
			wantChainID: tempotx.ChainIdMainnet,
			want:        []string{ousdAddress, usdceAddress},
		},
		{
			name:        "unknown chain keeps legacy single default",
			config:      MethodConfig{Intent: customIntent, ChainID: 999999},
			wantChainID: 999999,
			want:        []string{tempotx.AlphaUSDAddress.Hex()},
		},
		{
			name:        "unrecognized rpc url without chain id keeps legacy single default",
			config:      MethodConfig{Intent: customIntent},
			wantChainID: 0,
			want:        []string{tempotx.AlphaUSDAddress.Hex()},
		},
		{
			name:        "zero config offers mainnet defaults",
			config:      MethodConfig{Intent: testStoredIntent()},
			wantChainID: tempotx.ChainIdMainnet,
			want:        []string{ousdAddress, usdceAddress},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			config := tt.config
			config.Recipient = testRecipient
			method := NewMethod(config)
			assert.Equal(t, tt.want, method.currencies)
			assert.Equal(t, tt.wantChainID, method.chainID)
			assert.Equal(t, checksummed(tt.want...), offerCurrencies(t, method, mppserver.ChargeParams{Amount: "1"}, tt.wantChainID))

			// The single-request builder keeps returning the first offer.
			single, err := method.BuildChargeRequest(mppserver.ChargeParams{Amount: "1"})
			require.NoError(t, err)
			assert.Equal(t, checksummed(tt.want[0]), []string{single["currency"].(string)})
		})
	}
}

func TestNewMethodExplicitCurrencies(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		config MethodConfig
		want   []string
	}{
		{
			name:   "explicit list replaces defaults and preserves order",
			config: MethodConfig{Intent: testStoredIntent(), ChainID: tempotx.ChainIdMainnet, Currencies: []string{usdceAddress, ousdAddress}},
			want:   []string{usdceAddress, ousdAddress},
		},
		{
			name:   "explicit list may add tokens outside the defaults",
			config: MethodConfig{Intent: testStoredIntent(), ChainID: tempotx.ChainIdModerato, Currencies: []string{testCurrency, pathUSDAddress, ousdAddress}},
			want:   []string{testCurrency, pathUSDAddress, ousdAddress},
		},
		{
			name:   "single element list",
			config: MethodConfig{Intent: testStoredIntent(), ChainID: tempotx.ChainIdMainnet, Currencies: []string{usdceAddress}},
			want:   []string{usdceAddress},
		},
		{
			name:   "explicit list applies on unknown chain",
			config: MethodConfig{Intent: testStoredIntent(), Currencies: []string{ousdAddress, testCurrency}},
			want:   []string{ousdAddress, testCurrency},
		},
		{
			name: "mixed-case duplicates are removed keeping first spelling and order",
			config: MethodConfig{Intent: testStoredIntent(), ChainID: tempotx.ChainIdMainnet, Currencies: []string{
				ousdAddress,
				strings.ToLower(ousdAddress),
				usdceAddress,
				"0X" + strings.ToUpper(strings.TrimPrefix(ousdAddress, "0x")),
				strings.ToLower(usdceAddress),
			}},
			want: []string{ousdAddress, usdceAddress},
		},
		{
			name:   "legacy currency restricts acceptance to one token",
			config: MethodConfig{Intent: testStoredIntent(), ChainID: tempotx.ChainIdMainnet, Currency: usdceAddress},
			want:   []string{usdceAddress},
		},
		{
			name:   "legacy currency is kept verbatim",
			config: MethodConfig{Intent: testStoredIntent(), ChainID: tempotx.ChainIdModerato, Currency: strings.ToLower(ousdAddress)},
			want:   []string{strings.ToLower(ousdAddress)},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			config := tt.config
			config.Recipient = testRecipient
			method := NewMethod(config)
			assert.Equal(t, tt.want, method.currencies)
			assert.Equal(t, checksummed(tt.want...), offerCurrencies(t, method, mppserver.ChargeParams{Amount: "1"}, method.chainID))
		})
	}
}

func TestNewMethodExplicitCurrenciesAreCopied(t *testing.T) {
	t.Parallel()

	currencies := []string{ousdAddress, usdceAddress}
	method := NewMethod(MethodConfig{Intent: testStoredIntent(), ChainID: tempotx.ChainIdMainnet, Recipient: testRecipient, Currencies: currencies})
	currencies[0] = testCurrency
	assert.Equal(t, []string{ousdAddress, usdceAddress}, method.currencies)
}

func TestInvalidCurrencyConfiguration(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		currency   string
		currencies []string
		wantErr    string
	}{
		{
			name:       "currency and currencies together",
			currency:   usdceAddress,
			currencies: []string{ousdAddress},
			wantErr:    "tempo server: specify either Currency or Currencies, not both",
		},
		{
			name:       "currency and empty currencies together",
			currency:   usdceAddress,
			currencies: []string{},
			wantErr:    "tempo server: specify either Currency or Currencies, not both",
		},
		{
			name:       "empty currencies",
			currencies: []string{},
			wantErr:    "tempo server: Currencies must contain at least one currency",
		},
		{
			name:       "non-hex address",
			currencies: []string{ousdAddress, "usdc"},
			wantErr:    `tempo server: invalid currency address "usdc"`,
		},
		{
			name:       "short address",
			currencies: []string{"0x20c0"},
			wantErr:    `tempo server: invalid currency address "0x20c0"`,
		},
		{
			name:       "address without 0x prefix",
			currencies: []string{strings.TrimPrefix(ousdAddress, "0x")},
			wantErr:    `tempo server: invalid currency address "20c0000000000000000000006a37DA5C996874BE"`,
		},
		{
			name:       "empty string entry",
			currencies: []string{""},
			wantErr:    `tempo server: invalid currency address ""`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := resolveCurrencies(tt.currency, tt.currencies, tempotx.ChainIdMainnet)
			require.EqualError(t, err, tt.wantErr)

			assert.PanicsWithValue(t, tt.wantErr, func() {
				NewMethod(MethodConfig{Intent: testStoredIntent(),
					ChainID:    tempotx.ChainIdMainnet,
					Recipient:  testRecipient,
					Currency:   tt.currency,
					Currencies: tt.currencies,
				})
			})

			method, err := MethodFromConfig(Config{Store: tempo.NewMemoryStore(),
				ChainID:    tempotx.ChainIdMainnet,
				Recipient:  testRecipient,
				Currency:   tt.currency,
				Currencies: tt.currencies,
			})
			require.EqualError(t, err, tt.wantErr)
			assert.Nil(t, method)
		})
	}
}

func TestMethodFromConfigCurrencies(t *testing.T) {
	t.Parallel()

	defaults, err := MethodFromConfig(Config{Store: tempo.NewMemoryStore(), RPCURL: tempotx.RpcUrlModerato, Recipient: testRecipient})
	require.NoError(t, err)
	assert.Equal(t, []string{ousdAddress, pathUSDAddress}, defaults.currencies)

	explicit, err := MethodFromConfig(Config{Store: tempo.NewMemoryStore(),
		RPCURL:     tempotx.RpcUrlModerato,
		Recipient:  testRecipient,
		Currencies: []string{pathUSDAddress},
	})
	require.NoError(t, err)
	assert.Equal(t, []string{pathUSDAddress}, explicit.currencies)

	legacy, err := MethodFromConfig(Config{Store: tempo.NewMemoryStore(),
		RPCURL:    tempotx.RpcUrlModerato,
		Recipient: testRecipient,
		Currency:  testCurrency,
	})
	require.NoError(t, err)
	assert.Equal(t, []string{testCurrency}, legacy.currencies)

	relayed, err := MethodFromConfig(Config{Store: tempo.NewMemoryStore(),
		ChainID:    tempotx.ChainIdMainnet,
		Recipient:  testRecipient,
		Currencies: []string{usdceAddress, ousdAddress},
		Relay:      &RelayConfig{APIBaseURL: "https://relay.example.com", APIKey: "key"},
	})
	require.NoError(t, err)
	assert.Equal(t, []string{usdceAddress, ousdAddress}, relayed.currencies)
}

func TestMethodBuildChargeRequestsPerRequestCurrencyOverride(t *testing.T) {
	t.Parallel()

	method := NewMethod(MethodConfig{Intent: testStoredIntent(), ChainID: tempotx.ChainIdMainnet, Recipient: testRecipient})
	got := offerCurrencies(t, method, mppserver.ChargeParams{Amount: "1", Currency: testCurrency}, tempotx.ChainIdMainnet)
	assert.Equal(t, checksummed(testCurrency), got)

	single, err := method.BuildChargeRequest(mppserver.ChargeParams{Amount: "1", Currency: testCurrency})
	require.NoError(t, err)
	assert.Equal(t, checksummed(testCurrency), []string{single["currency"].(string)})
}

func TestMethodBuildChargeRequestsSharesRequestFieldsAcrossOffers(t *testing.T) {
	t.Parallel()

	method := NewMethod(MethodConfig{Intent: testStoredIntent(),
		ChainID:     tempotx.ChainIdMainnet,
		Recipient:   testRecipient,
		FeePayerURL: "https://fee-payer.example.com",
	})
	requests, err := method.BuildChargeRequests(mppserver.ChargeParams{
		Amount:      "0.50",
		ExternalID:  "ext-1",
		FeePayer:    true,
		Description: "coffee",
	})
	require.NoError(t, err)
	require.Len(t, requests, 2)
	for i, want := range []string{ousdAddress, usdceAddress} {
		request, err := tempo.ParseChargeRequest(requests[i])
		require.NoError(t, err)
		assert.Equal(t, checksummed(want), []string{request.Currency})
		assert.Equal(t, "500000", request.Amount)
		assert.Equal(t, checksummed(testRecipient), []string{request.Recipient})
		assert.Equal(t, "ext-1", request.ExternalID)
		assert.True(t, request.MethodDetails.FeePayer)
		assert.Equal(t, "https://fee-payer.example.com", request.MethodDetails.FeePayerURL)
	}
}

func TestMethodBuildChargeRequestsPropagatesErrors(t *testing.T) {
	t.Parallel()

	method := NewMethod(MethodConfig{Intent: testStoredIntent(), ChainID: tempotx.ChainIdMainnet})
	_, err := method.BuildChargeRequests(mppserver.ChargeParams{Amount: "1"})
	require.EqualError(t, err, "tempo server: recipient must be configured on the method or the request")
}

func TestDefaultFeePayerPoliciesUnchanged(t *testing.T) {
	t.Parallel()

	intent, err := NewIntent(IntentConfig{Store: tempo.NewMemoryStore()})
	require.NoError(t, err)

	want := FeePayerPolicy{
		MaxFeePerGas:         big.NewInt(100_000_000_000),
		MaxPriorityFeePerGas: big.NewInt(100_000_000_000),
		MaxTotalFee:          big.NewInt(50_000_000_000_000_000),
	}
	assert.Equal(t, map[string]FeePayerPolicy{
		"0x20C0000000000000000000000000000000000000": want,
		"0x20C0000000000000000000000000000000000001": want,
		"0x20C000000000000000000000b9537d11c60E8b50": want,
	}, intent.feePayerPolicy)
	_, ok := intent.feePayerPolicy[common.HexToAddress(ousdAddress).Hex()]
	assert.False(t, ok, "OUSD must not be a default sponsored fee token")

	raw := defaultFeePayerPolicies()
	keys := make([]string, 0, len(raw))
	for key := range raw {
		keys = append(keys, key)
	}
	assert.ElementsMatch(t, []string{
		"0x20c0000000000000000000000000000000000000",
		"0x20C0000000000000000000000000000000000001",
		"0x20C000000000000000000000b9537d11c60E8b50",
	}, keys)
}

func TestChargeOffers_HashCredentialForEachOfferVerifies(t *testing.T) {
	t.Parallel()

	for index, currency := range []string{ousdAddress, usdceAddress} {
		t.Run(currency, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			rpc := newOffersRPC(tempotx.ChainIdMainnet)
			payment := newOffersServer(t, rpc, MethodConfig{Intent: testStoredIntent(), ChainID: tempotx.ChainIdMainnet})

			challenges := issueOffers(t, payment, mppserver.ChargeParams{Amount: "0.50"})
			require.Equal(t, checksummed(ousdAddress, usdceAddress), challengeCurrencies(challenges))

			credential := payWithHash(t, rpc, challenges[index])
			result, err := payment.Charge(ctx, mppserver.ChargeParams{
				Amount:        "0.50",
				Authorization: credential.ToAuthorization(),
			})
			require.NoError(t, err)
			require.False(t, result.IsChallenge())
			require.NotNil(t, result.Receipt)
			assert.Equal(t, "success", result.Receipt.Status)
			assert.Equal(t, testReceiptHash, result.Receipt.Reference)
			require.Len(t, rpc.sentRawTxs, 1)
			assert.Equal(t, []common.Address{common.HexToAddress(currency)}, sentTokens(t, rpc.sentRawTxs[0]))
		})
	}
}

func TestChargeOffers_ModeratoDefaultsVerifyPathUSD(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	rpc := newOffersRPC(tempotx.ChainIdModerato)
	payment := newOffersServer(t, rpc, MethodConfig{Intent: testStoredIntent(), ChainID: tempotx.ChainIdModerato})

	challenges := issueOffers(t, payment, mppserver.ChargeParams{Amount: "0.50"})
	require.Equal(t, checksummed(ousdAddress, pathUSDAddress), challengeCurrencies(challenges))
	for _, challenge := range challenges {
		request, err := tempo.ParseChargeRequest(challenge.Request)
		require.NoError(t, err)
		require.NotNil(t, request.MethodDetails.ChainID)
		assert.EqualValues(t, tempotx.ChainIdModerato, *request.MethodDetails.ChainID)
	}

	credential := payWithHash(t, rpc, challenges[1])
	result, err := payment.Charge(ctx, mppserver.ChargeParams{Amount: "0.50", Authorization: credential.ToAuthorization()})
	require.NoError(t, err)
	require.False(t, result.IsChallenge())
	assert.Equal(t, "success", result.Receipt.Status)
}

func TestChargeOffers_RejectsCredentialForUnofferedCurrency(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	rpc := newOffersRPC(tempotx.ChainIdMainnet)
	payment := newOffersServer(t, rpc, MethodConfig{Intent: testStoredIntent(), ChainID: tempotx.ChainIdMainnet})

	// The same server signs a challenge for another token through a
	// per-request override, e.g. on a different route.
	other := issueOffers(t, payment, mppserver.ChargeParams{Amount: "0.50", Currency: testCurrency})
	require.Equal(t, checksummed(testCurrency), challengeCurrencies(other))
	credential := other[0].NewCredential(map[string]any{"type": "hash", "hash": testReceiptHash})

	result, err := payment.Charge(ctx, mppserver.ChargeParams{Amount: "0.50", Authorization: credential.ToAuthorization()})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "credential request does not match this route's requirements")
	require.NotNil(t, result)
	assert.Nil(t, result.Receipt)
	assert.Equal(t, checksummed(ousdAddress, usdceAddress), challengeCurrencies(result.Challenges))
	assert.Same(t, result.Challenges[0], result.Challenge)
	assert.Empty(t, rpc.sentRawTxs)
}

func TestChargeOffers_LegacyCurrencyRejectsOtherDefaultOffer(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	rpc := newOffersRPC(tempotx.ChainIdMainnet)
	defaults := newOffersServer(t, rpc, MethodConfig{Intent: testStoredIntent(), ChainID: tempotx.ChainIdMainnet})
	legacy := newOffersServer(t, rpc, MethodConfig{Intent: testStoredIntent(), ChainID: tempotx.ChainIdMainnet, Currency: usdceAddress})

	legacyOffers := issueOffers(t, legacy, mppserver.ChargeParams{Amount: "0.50"})
	require.Equal(t, checksummed(usdceAddress), challengeCurrencies(legacyOffers))

	// A challenge for OUSD signed with the same secret is not accepted by the
	// USDC.e-only server.
	ousdOffer := issueOffers(t, defaults, mppserver.ChargeParams{Amount: "0.50"})[0]
	require.Equal(t, checksummed(ousdAddress), []string{ousdOffer.Request["currency"].(string)})
	credential := ousdOffer.NewCredential(map[string]any{"type": "hash", "hash": testReceiptHash})
	_, err := legacy.Charge(ctx, mppserver.ChargeParams{Amount: "0.50", Authorization: credential.ToAuthorization()})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "credential request does not match this route's requirements")

	paid := payWithHash(t, rpc, legacyOffers[0])
	result, err := legacy.Charge(ctx, mppserver.ChargeParams{Amount: "0.50", Authorization: paid.ToAuthorization()})
	require.NoError(t, err)
	assert.Equal(t, "success", result.Receipt.Status)
}

func TestChargeOffers_ExplicitCurrenciesRejectDroppedDefault(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	rpc := newOffersRPC(tempotx.ChainIdMainnet)
	defaults := newOffersServer(t, rpc, MethodConfig{Intent: testStoredIntent(), ChainID: tempotx.ChainIdMainnet})
	usdcOnly := newOffersServer(t, rpc, MethodConfig{Intent: testStoredIntent(), ChainID: tempotx.ChainIdMainnet, Currencies: []string{usdceAddress}})

	require.Equal(t, checksummed(usdceAddress), challengeCurrencies(issueOffers(t, usdcOnly, mppserver.ChargeParams{Amount: "0.50"})))
	ousdOffer := issueOffers(t, defaults, mppserver.ChargeParams{Amount: "0.50"})[0]
	credential := ousdOffer.NewCredential(map[string]any{"type": "hash", "hash": testReceiptHash})
	_, err := usdcOnly.Charge(ctx, mppserver.ChargeParams{Amount: "0.50", Authorization: credential.ToAuthorization()})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "credential request does not match this route's requirements")
}

func TestChargeOffers_PerRequestCurrencyOverrideVerifies(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	rpc := newOffersRPC(tempotx.ChainIdMainnet)
	payment := newOffersServer(t, rpc, MethodConfig{Intent: testStoredIntent(), ChainID: tempotx.ChainIdMainnet})
	params := mppserver.ChargeParams{Amount: "0.50", Currency: testCurrency}

	challenges := issueOffers(t, payment, params)
	require.Equal(t, checksummed(testCurrency), challengeCurrencies(challenges))

	credential := payWithHash(t, rpc, challenges[0])
	params.Authorization = credential.ToAuthorization()
	result, err := payment.Charge(ctx, params)
	require.NoError(t, err)
	require.False(t, result.IsChallenge())
	assert.Equal(t, "success", result.Receipt.Status)
}

func TestChargeOffers_ChargeMiddlewareAdvertisesOffersInOrder(t *testing.T) {
	t.Parallel()

	rpc := newOffersRPC(tempotx.ChainIdMainnet)
	payment := newOffersServer(t, rpc, MethodConfig{Intent: testStoredIntent(), ChainID: tempotx.ChainIdMainnet})
	recorder := serveOffersMiddleware(payment, "")

	require.Equal(t, 402, recorder.Code)
	values := recorder.Header().Values(mpp.HeaderWWWAuthenticate)
	require.Len(t, values, 2)
	challenges := make([]*mpp.Challenge, 0, len(values))
	for _, value := range values {
		challenge, err := mpp.ParseChallenge(value)
		require.NoError(t, err)
		challenges = append(challenges, challenge)
	}
	assert.Equal(t, checksummed(ousdAddress, usdceAddress), challengeCurrencies(challenges))

	credential := payWithHash(t, rpc, challenges[1])
	paid := serveOffersMiddleware(payment, credential.ToAuthorization())
	require.Equal(t, 200, paid.Code)
	assert.NotEmpty(t, paid.Header().Get(mpp.HeaderPaymentReceipt))
}

func offerCurrencies(t *testing.T, method *Method, params mppserver.ChargeParams, wantChainID int64) []string {
	t.Helper()
	requests, err := method.BuildChargeRequests(params)
	require.NoError(t, err)
	currencies := make([]string, 0, len(requests))
	for _, requestMap := range requests {
		request, err := tempo.ParseChargeRequest(requestMap)
		require.NoError(t, err)
		currencies = append(currencies, request.Currency)
		if wantChainID == 0 {
			assert.Nil(t, request.MethodDetails.ChainID)
			continue
		}
		require.NotNil(t, request.MethodDetails.ChainID)
		assert.Equal(t, wantChainID, *request.MethodDetails.ChainID)
	}
	return currencies
}

func challengeCurrencies(challenges []*mpp.Challenge) []string {
	currencies := make([]string, 0, len(challenges))
	for _, challenge := range challenges {
		currency, _ := challenge.Request["currency"].(string)
		currencies = append(currencies, currency)
	}
	return currencies
}

func newOffersRPC(chainID int64) *mockRPC {
	rpc := &mockRPC{
		chainID:     uint64(chainID),
		nonce:       7,
		gasPrice:    "0x1",
		estimateGas: "0x5208",
		receipts:    map[string]map[string]any{},
	}
	// Emit Transfer logs from the token each call targets so verification
	// sees the currency the client actually paid with.
	rpc.onSend = func(raw string) (string, map[string]any, error) {
		tx, err := tempotx.Deserialize(raw)
		if err != nil {
			return "", nil, err
		}
		sender, err := tempotx.VerifySignature(tx)
		if err != nil {
			return "", nil, err
		}
		logs := make([]any, 0, len(tx.Calls))
		for _, call := range tx.Calls {
			data := common.Bytes2Hex(call.Data)
			amount, _ := new(big.Int).SetString(data[72:136], 16)
			recipient := common.HexToAddress("0x" + data[32:72]).Hex()
			logs = append(logs, transferLog(call.To.Hex(), sender.Hex(), recipient, amount, ""))
			if strings.HasPrefix(data, tempo.TransferWithMemoSelector) {
				logs = append(logs, transferLog(call.To.Hex(), sender.Hex(), recipient, amount, "0x"+data[136:200]))
			}
		}
		return testReceiptHash, map[string]any{"status": "0x1", "logs": logs}, nil
	}
	return rpc
}

func newOffersServer(t *testing.T, rpc *mockRPC, config MethodConfig) *mppserver.Mpp {
	t.Helper()
	intent, err := NewIntent(IntentConfig{Store: tempo.NewMemoryStore(), RPC: rpc})
	require.NoError(t, err)
	config.Intent = intent
	config.Recipient = testRecipient
	return newTestServer(t, NewMethod(config), testRealm, offersSecret)
}

func issueOffers(t *testing.T, payment *mppserver.Mpp, params mppserver.ChargeParams) []*mpp.Challenge {
	t.Helper()
	result, err := payment.Charge(context.Background(), params)
	require.NoError(t, err)
	require.True(t, result.IsChallenge())
	require.NotEmpty(t, result.Challenges)
	assert.Same(t, result.Challenges[0], result.Challenge)
	for _, challenge := range result.Challenges {
		assert.Equal(t, result.Challenge.Expires, challenge.Expires)
	}
	return result.Challenges
}

func payWithHash(t *testing.T, rpc *mockRPC, challenge *mpp.Challenge) *mpp.Credential {
	t.Helper()
	method, err := chargeclient.New(chargeclient.Config{PaymentPolicy: func(context.Context, tempo.ChargeRequest) error { return nil },
		PrivateKey:     testPrivateKey,
		RPC:            rpc,
		ChainID:        int64(rpc.chainID),
		CredentialType: tempo.CredentialTypeHash,
	})
	require.NoError(t, err)
	credential, err := method.CreateCredential(context.Background(), challenge)
	require.NoError(t, err)
	return credential
}

func sentTokens(t *testing.T, raw string) []common.Address {
	t.Helper()
	tx, err := tempotx.Deserialize(raw)
	require.NoError(t, err)
	tokens := make([]common.Address, 0, len(tx.Calls))
	for _, call := range tx.Calls {
		tokens = append(tokens, *call.To)
	}
	return tokens
}

func serveOffersMiddleware(payment *mppserver.Mpp, authorization string) *httptest.ResponseRecorder {
	handler := mppserver.ChargeMiddleware(payment, mppserver.ChargeParams{Amount: "0.50"})(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }),
	)
	request := httptest.NewRequest(http.MethodGet, "/paid", nil)
	if authorization != "" {
		request.Header.Set(mpp.HeaderAuthorization, authorization)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

// checksummed returns EIP-55 spellings, matching normalized charge requests.
func checksummed(values ...string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		out = append(out, common.HexToAddress(value).Hex())
	}
	return out
}

func TestMethodFromConfigDefaultOffers(t *testing.T) {
	method, err := MethodFromConfig(Config{Store: tempo.NewMemoryStore(), Recipient: testRecipient})
	require.NoError(t, err)
	assert.Equal(t, checksummed(ousdAddress, usdceAddress), offerCurrencies(t, method, mppserver.ChargeParams{Amount: "1"}, tempotx.ChainIdMainnet))
}
