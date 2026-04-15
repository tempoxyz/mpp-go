package server

import (
	"testing"

	genericserver "github.com/tempoxyz/mpp-go/pkg/server"
	"github.com/tempoxyz/mpp-go/pkg/tempo"
)

func TestMethodBuildChargeRequest_IncludesExternalIDAndFeePayerURL(t *testing.T) {
	t.Parallel()

	method := NewMethod(MethodConfig{
		Currency:    "0x20c0000000000000000000000000000000000001",
		Recipient:   "0x70997970c51812dc3a010c7d01b50e0d17dc79c8",
		ChainID:     42431,
		FeePayer:    true,
		FeePayerURL: "https://fee-payer.example.com",
	})

	requestMap, err := method.BuildChargeRequest(genericserver.ChargeParams{
		Amount:      "0.50",
		Description: "coffee",
		ExternalID:  "ext-123",
	})
	if err != nil {
		t.Fatalf("BuildChargeRequest() error = %v", err)
	}

	request, err := tempo.ParseChargeRequest(requestMap)
	if err != nil {
		t.Fatalf("ParseChargeRequest() error = %v", err)
	}
	if request.ExternalID != "ext-123" {
		t.Fatalf("request.ExternalID = %q, want ext-123", request.ExternalID)
	}
	if !request.MethodDetails.FeePayer {
		t.Fatal("request.MethodDetails.FeePayer = false, want true")
	}
	if request.MethodDetails.FeePayerURL != "https://fee-payer.example.com" {
		t.Fatalf("request.MethodDetails.FeePayerURL = %q, want https://fee-payer.example.com", request.MethodDetails.FeePayerURL)
	}
}
