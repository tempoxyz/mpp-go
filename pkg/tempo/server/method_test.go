package chargeserver

import (
	"testing"

	"github.com/stretchr/testify/assert"
	mppserver "github.com/tempoxyz/mpp-go/pkg/server"
	"github.com/tempoxyz/mpp-go/pkg/tempo"
	tempotx "github.com/tempoxyz/tempo-go/pkg/transaction"
)

func TestMethodBuildChargeRequest(t *testing.T) {
	t.Parallel()

	moderatoIntent, err := NewIntent(IntentConfig{RPCURL: tempotx.RpcUrlModerato})
	if !assert.NoErrorf(t, err,
		"NewIntent() error = %v", err) {
		return
	}

	tests := []struct {
		name       string
		config     MethodConfig
		params     mppserver.ChargeParams
		assertions func(*testing.T, tempo.ChargeRequest)
	}{
		{
			name: "infers chain and currency from intent rpc url",
			config: MethodConfig{
				Intent:    moderatoIntent,
				Recipient: "0x70997970c51812dc3a010c7d01b50e0d17dc79c8",
			},
			params: mppserver.ChargeParams{
				Amount: "0.50",
			},
			assertions: func(t *testing.T, request tempo.ChargeRequest) {
				t.Helper()
				if !assert.Equalf(t, tempotx.AlphaUSDAddress.Hex(), request.Currency,
					"request.Currency = %q, want %q", request.Currency, tempotx.AlphaUSDAddress.Hex()) {
					return
				}
				if !assert.Falsef(t, request.MethodDetails.ChainID == nil || *request.MethodDetails.ChainID != tempotx.ChainIdModerato,
					"request.MethodDetails.ChainID = %v, want %d", request.MethodDetails.ChainID, tempotx.ChainIdModerato) {
					return
				}

			},
		},
		{
			name: "rejects unknown chain without intent rpc",
			config: MethodConfig{
				ChainID:   999999,
				Recipient: "0x70997970c51812dc3a010c7d01b50e0d17dc79c8",
			},
			params: mppserver.ChargeParams{
				Amount: "0.50",
			},
		},
		{
			name: "includes external id and fee payer url",
			config: MethodConfig{
				Currency:    "0x20c0000000000000000000000000000000000001",
				Recipient:   "0x70997970c51812dc3a010c7d01b50e0d17dc79c8",
				ChainID:     42431,
				FeePayer:    true,
				FeePayerURL: "https://fee-payer.example.com",
			},
			params: mppserver.ChargeParams{
				Amount:      "0.50",
				Description: "coffee",
				ExternalID:  "ext-123",
			},
			assertions: func(t *testing.T, request tempo.ChargeRequest) {
				t.Helper()
				if !assert.Equalf(t, "ext-123", request.ExternalID,
					"request.ExternalID = %q, want ext-123", request.ExternalID) {
					return
				}
				if !assert.True(t, request.MethodDetails.FeePayer,
					"request.MethodDetails.FeePayer = false, want true") {
					return
				}
				if !assert.Equalf(t, "https://fee-payer.example.com", request.MethodDetails.FeePayerURL,
					"request.MethodDetails.FeePayerURL = %q, want https://fee-payer.example.com", request.MethodDetails.FeePayerURL) {
					return
				}

			},
		},
		{
			name: "includes splits and request modes",
			config: MethodConfig{
				Currency:       "0x20c0000000000000000000000000000000000001",
				Recipient:      "0x70997970c51812dc3a010c7d01b50e0d17dc79c8",
				ChainID:        42431,
				SupportedModes: []tempo.ChargeMode{tempo.ChargeModePull},
			},
			params: mppserver.ChargeParams{
				Amount: "0.50",
				Splits: []tempo.SplitParams{{
					Amount:    "0.10",
					Recipient: "0x3c44cdddb6a900fa2b585dd299e03d12fa4293bc",
				}},
				SupportedModes: []tempo.ChargeMode{tempo.ChargeModePush},
			},
			assertions: func(t *testing.T, request tempo.ChargeRequest) {
				t.Helper()
				{
					got := len(request.MethodDetails.Splits)
					if !assert.EqualValuesf(t, 1, got,
						"len(request.MethodDetails.Splits) = %d, want 1", got) {
						return
					}
				}
				{

					got := len(request.MethodDetails.SupportedModes)
					if !assert.Falsef(t, got != 1 || request.MethodDetails.SupportedModes[0] != tempo.ChargeModePush,
						"request.MethodDetails.SupportedModes = %#v, want [push]", request.MethodDetails.SupportedModes) {
						return
					}
				}

			},
		},
		{
			name: "explicit primary memo forces pull mode",
			config: MethodConfig{
				Currency:  "0x20c0000000000000000000000000000000000001",
				Recipient: "0x70997970c51812dc3a010c7d01b50e0d17dc79c8",
				ChainID:   42431,
			},
			params: mppserver.ChargeParams{
				Amount:         "0.50",
				Memo:           "0x0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20",
				SupportedModes: []tempo.ChargeMode{tempo.ChargeModePush},
			},
			assertions: func(t *testing.T, request tempo.ChargeRequest) {
				t.Helper()
				{
					got := len(request.MethodDetails.SupportedModes)
					if !assert.Falsef(t, got != 1 || request.MethodDetails.SupportedModes[0] != tempo.ChargeModePull,
						"request.MethodDetails.SupportedModes = %#v, want [pull]", request.MethodDetails.SupportedModes) {
						return
					}
				}

			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			method := NewMethod(tt.config)
			requestMap, err := method.BuildChargeRequest(tt.params)
			if tt.name == "rejects unknown chain without intent rpc" {
				if !assert.Falsef(t, err == nil || err.Error() != "tempo server: unknown chain id 999999; configure Intent.RPC or Intent.RPCURL explicitly",
					"BuildChargeRequest() error = %v, want unknown chain id error", err) {
					return
				}

				return
			}
			if !assert.NoErrorf(t, err,
				"BuildChargeRequest() error = %v", err) {
				return
			}

			request, err := tempo.ParseChargeRequest(requestMap)
			if !assert.NoErrorf(t, err,
				"ParseChargeRequest() error = %v", err) {
				return
			}

			tt.assertions(t, request)
		})
	}
}
