package chargeserver

import (
	"testing"

	mppserver "github.com/tempoxyz/mpp-go/pkg/server"
	"github.com/tempoxyz/mpp-go/pkg/tempo"
	tempotx "github.com/tempoxyz/tempo-go/pkg/transaction"
)

func TestMethodBuildChargeRequest(t *testing.T) {
	t.Parallel()

	moderatoIntent, err := NewIntent(IntentConfig{RPCURL: tempotx.RpcUrlModerato})
	if err != nil {
		t.Fatalf("NewIntent() error = %v", err)
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
				if request.Currency != tempotx.AlphaUSDAddress.Hex() {
					t.Fatalf("request.Currency = %q, want %q", request.Currency, tempotx.AlphaUSDAddress.Hex())
				}
				if request.MethodDetails.ChainID == nil || *request.MethodDetails.ChainID != tempotx.ChainIdModerato {
					t.Fatalf("request.MethodDetails.ChainID = %v, want %d", request.MethodDetails.ChainID, tempotx.ChainIdModerato)
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
				if request.ExternalID != "ext-123" {
					t.Fatalf("request.ExternalID = %q, want ext-123", request.ExternalID)
				}
				if !request.MethodDetails.FeePayer {
					t.Fatal("request.MethodDetails.FeePayer = false, want true")
				}
				if request.MethodDetails.FeePayerURL != "https://fee-payer.example.com" {
					t.Fatalf("request.MethodDetails.FeePayerURL = %q, want https://fee-payer.example.com", request.MethodDetails.FeePayerURL)
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
				if got := len(request.MethodDetails.Splits); got != 1 {
					t.Fatalf("len(request.MethodDetails.Splits) = %d, want 1", got)
				}
				if got := len(request.MethodDetails.SupportedModes); got != 1 || request.MethodDetails.SupportedModes[0] != tempo.ChargeModePush {
					t.Fatalf("request.MethodDetails.SupportedModes = %#v, want [push]", request.MethodDetails.SupportedModes)
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
				if got := len(request.MethodDetails.SupportedModes); got != 1 || request.MethodDetails.SupportedModes[0] != tempo.ChargeModePull {
					t.Fatalf("request.MethodDetails.SupportedModes = %#v, want [pull]", request.MethodDetails.SupportedModes)
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
				if err == nil || err.Error() != "tempo server: unknown chain id 999999; configure Intent.RPC or Intent.RPCURL explicitly" {
					t.Fatalf("BuildChargeRequest() error = %v, want unknown chain id error", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("BuildChargeRequest() error = %v", err)
			}

			request, err := tempo.ParseChargeRequest(requestMap)
			if err != nil {
				t.Fatalf("ParseChargeRequest() error = %v", err)
			}
			tt.assertions(t, request)
		})
	}
}
