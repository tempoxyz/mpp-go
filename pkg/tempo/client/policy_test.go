package chargeclient

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tempoxyz/mpp-go/pkg/tempo"
)

func TestPaymentPolicyBeforeSigning(t *testing.T) {
	for _, kind := range []tempo.CredentialType{tempo.CredentialTypeTransaction, tempo.CredentialTypeHash} {
		for _, policy := range []string{"missing", "reject", "approve"} {
			t.Run(string(kind)+"/"+policy, func(t *testing.T) {
				rpc := &mockRPC{chainID: 42431, gasPrice: "0x1", estimateGas: "0x5208"}
				config := Config{PrivateKey: testPrivateKey, RPC: rpc, ChainID: 42431, CredentialType: kind}
				called := false
				if policy != "missing" {
					config.PaymentPolicy = func(_ context.Context, request tempo.ChargeRequest) error {
						called = true
						require.Equal(t, "500000", request.Amount)
						require.True(t, strings.EqualFold(testCurrency, request.Currency))
						require.True(t, strings.EqualFold(testRecipient, request.Recipient))
						if policy == "reject" {
							return fmt.Errorf("budget exceeded")
						}
						return nil
					}
				}
				method, err := New(config)
				require.NoError(t, err)
				credential, err := method.CreateCredential(context.Background(), buildChallenge(t, tempo.ChargeRequestParams{
					Amount: "0.50", Currency: testCurrency, Recipient: testRecipient, Decimals: 6, ChainID: 42431,
				}))
				if policy == "approve" {
					require.NoError(t, err)
					require.NotNil(t, credential)
				} else {
					require.Error(t, err)
					require.Nil(t, credential)
					require.Empty(t, rpc.sentRawTxs)
				}
				require.Equal(t, policy != "missing", called)
			})
		}
	}
}

func TestPaymentPolicyReceivesVerifiedChainWhenChallengeOmitsIt(t *testing.T) {
	rpc := &mockRPC{chainID: 42431, gasPrice: "0x1", estimateGas: "0x5208"}
	called := false
	method, err := New(Config{PrivateKey: testPrivateKey, RPC: rpc, ChainID: 42431,
		PaymentPolicy: func(_ context.Context, request tempo.ChargeRequest) error {
			called = true
			require.NotNil(t, request.MethodDetails.ChainID)
			require.EqualValues(t, 42431, *request.MethodDetails.ChainID)
			return nil
		},
	})
	require.NoError(t, err)
	challenge := buildChallenge(t, tempo.ChargeRequestParams{Amount: "0.5", Currency: testCurrency, Recipient: testRecipient, Decimals: 6})
	_, err = method.CreateCredential(context.Background(), challenge)
	require.NoError(t, err)
	require.True(t, called)
}
