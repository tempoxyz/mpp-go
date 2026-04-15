package chargeserver

import (
	"testing"

	mppserver "github.com/tempoxyz/mpp-go/pkg/server"
	"github.com/tempoxyz/mpp-go/pkg/tempo"
)

func TestMethodBuildChargeRequest(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		config     MethodConfig
		params     mppserver.ChargeParams
		assertions func(*testing.T, tempo.ChargeRequest)
	}{
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
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			method := NewMethod(tt.config)
			requestMap, err := method.BuildChargeRequest(tt.params)
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
