package chargeclient

import (
	"context"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tempoxyz/mpp-go/pkg/tempo"
)

func TestFeeTokenPaysUnsponsoredFees(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name           string
		credentialType tempo.CredentialType
		feePayer       bool
		feeToken       string
		want           common.Address
	}{
		{name: "pull pays the fee in FeeToken", credentialType: tempo.CredentialTypeTransaction, feeToken: tempo.OUSDAddress, want: common.HexToAddress(tempo.OUSDAddress)},
		{name: "push pays the fee in FeeToken", credentialType: tempo.CredentialTypeHash, feeToken: tempo.OUSDAddress, want: common.HexToAddress(tempo.OUSDAddress)},
		{name: "push on a sponsored challenge pays the fee in FeeToken", credentialType: tempo.CredentialTypeHash, feePayer: true, feeToken: tempo.OUSDAddress, want: common.HexToAddress(tempo.OUSDAddress)},
		{name: "without FeeToken the fee is paid in the currency", credentialType: tempo.CredentialTypeTransaction, want: common.HexToAddress(testCurrency)},
		{name: "a sponsored pull ignores FeeToken", credentialType: tempo.CredentialTypeTransaction, feePayer: true, feeToken: tempo.OUSDAddress},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			rpc := &mockRPC{chainID: 42431, gasPrice: "0x1", estimateGas: "0x5208"}
			method, err := New(Config{
				PaymentPolicy:  func(context.Context, tempo.ChargeRequest) error { return nil },
				ChainID:        42431,
				CredentialType: tt.credentialType,
				FeeToken:       tt.feeToken,
				PrivateKey:     testPrivateKey,
				RPC:            rpc,
			})
			require.NoError(t, err)
			challenge := buildChallenge(t, tempo.ChargeRequestParams{
				Amount: "0.50", Currency: testCurrency, Recipient: testRecipient, Decimals: 6, ChainID: 42431, FeePayer: tt.feePayer,
			})
			credential, err := method.CreateCredential(context.Background(), challenge)
			require.NoError(t, err)

			raw, _ := credential.Payload["signature"].(string)
			if tt.credentialType == tempo.CredentialTypeHash {
				require.Len(t, rpc.sentRawTxs, 1)
				raw = rpc.sentRawTxs[0]
			}
			tx, err := deserializeTestTx(raw)
			require.NoError(t, err)
			assert.Equal(t, tt.want, tx.FeeToken)
		})
	}
}

func TestNewRejectsInvalidFeeToken(t *testing.T) {
	t.Parallel()

	_, err := New(Config{PrivateKey: testPrivateKey, ChainID: 42431, RPC: &mockRPC{chainID: 42431}, FeeToken: "OUSD"})
	require.ErrorContains(t, err, "invalid FeeToken")
}
