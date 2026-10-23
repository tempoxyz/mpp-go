package chargeclient

import (
	"context"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"

	"github.com/stretchr/testify/require"
	"github.com/tempoxyz/mpp-go/pkg/tempo"
	tempotx "github.com/tempoxyz/tempo-go/pkg/transaction"
)

func TestMACHChargeUsesStablecoinFeeToken(t *testing.T) {
	t.Parallel()

	alphaUSD := tempotx.AlphaUSDAddress.Hex()
	tests := []struct {
		name         string
		currency     string
		balances     map[string]string
		wantFeeToken string
	}{
		{
			name:         "funded default currency",
			currency:     tempo.MACHAddress,
			balances:     map[string]string{strings.ToLower(alphaUSD): "0x" + strings.Repeat("0", 62) + "01"},
			wantFeeToken: alphaUSD,
		},
		{
			name:         "funded pathUSD first",
			currency:     tempo.MACHAddress,
			balances:     map[string]string{strings.ToLower(tempo.PathUSDAddress): "0x1", strings.ToLower(alphaUSD): "0x1"},
			wantFeeToken: tempo.PathUSDAddress,
		},
		{
			name:         "unfunded falls back to pathUSD",
			currency:     strings.ToLower(tempo.MACHAddress),
			wantFeeToken: tempo.PathUSDAddress,
		},
		{
			name:         "stablecoin pays its own fees",
			currency:     testCurrency,
			wantFeeToken: testCurrency,
		},
	}
	for _, kind := range []tempo.CredentialType{tempo.CredentialTypeTransaction, tempo.CredentialTypeHash} {
		for _, tt := range tests {
			t.Run(string(kind)+"/"+tt.name, func(t *testing.T) {
				t.Parallel()

				rpc := &mockRPC{chainID: 42431, gasPrice: "0x1", estimateGas: "0x5208", balances: tt.balances}
				method, err := New(Config{
					PaymentPolicy:  func(context.Context, tempo.ChargeRequest) error { return nil },
					ChainID:        42431,
					CredentialType: kind,
					PrivateKey:     testPrivateKey,
					RPC:            rpc,
				})
				require.NoError(t, err)
				credential, err := method.CreateCredential(context.Background(), buildChallenge(t, tempo.ChargeRequestParams{
					Amount: "0.50", Currency: tt.currency, Recipient: testRecipient, Decimals: 6, ChainID: 42431,
				}))
				require.NoError(t, err)
				payload, err := tempo.ParseChargeCredentialPayload(credential.Payload)
				require.NoError(t, err)
				serialized := payload.Signature
				if kind == tempo.CredentialTypeHash {
					require.Len(t, rpc.sentRawTxs, 1)
					serialized = rpc.sentRawTxs[0]
				}
				tx, err := tempotx.Deserialize(serialized)
				require.NoError(t, err)
				require.True(t, strings.EqualFold(tt.wantFeeToken, tx.FeeToken.Hex()), "fee token = %s", tx.FeeToken.Hex())
				require.Len(t, tx.Calls, 1)
				require.True(t, strings.EqualFold(tt.currency, tx.Calls[0].To.Hex()), "transfer token = %s", tx.Calls[0].To.Hex())
			})
		}
	}
}

func TestMACHSponsoredChargeLeavesFeeTokenToFeePayer(t *testing.T) {
	t.Parallel()

	rpc := &mockRPC{chainID: 42431, gasPrice: "0x1", estimateGas: "0x5208"}
	method, err := New(Config{
		PaymentPolicy: func(context.Context, tempo.ChargeRequest) error { return nil },
		ChainID:       42431,
		PrivateKey:    testPrivateKey,
		RPC:           rpc,
	})
	require.NoError(t, err)
	credential, err := method.CreateCredential(context.Background(), buildChallenge(t, tempo.ChargeRequestParams{
		Amount: "0.50", Currency: tempo.MACHAddress, Recipient: testRecipient, Decimals: 6, ChainID: 42431, FeePayer: true,
	}))
	require.NoError(t, err)
	payload, err := tempo.ParseChargeCredentialPayload(credential.Payload)
	require.NoError(t, err)
	tx, err := tempotx.Deserialize(payload.Signature)
	require.NoError(t, err)
	require.Equal(t, common.Address{}, tx.FeeToken)
}
