package chargeserver

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tempoxyz/mpp-go/pkg/mpp"
	mppserver "github.com/tempoxyz/mpp-go/pkg/server"
)

const sponsoredIssuanceRecipient = "0x70997970c51812dc3a010c7d01b50e0d17dc79c8"

func TestSponsoredChallengeIssuanceRequires15Seconds(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		feePayer bool
		expires  string
		wantErr  bool
	}{
		{name: "sponsored expiring in 10s", feePayer: true, expires: mpp.Expires.Seconds(10), wantErr: true},
		{name: "sponsored already expired", feePayer: true, expires: mpp.Expires.Seconds(-5), wantErr: true},
		{name: "sponsored with unparseable expires", feePayer: true, expires: "soon", wantErr: true},
		{name: "sponsored expiring in 30s", feePayer: true, expires: mpp.Expires.Seconds(30)},
		{name: "sponsored with default expiry", feePayer: true},
		{name: "unsponsored expiring in 10s", expires: mpp.Expires.Seconds(10)},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			method := NewMethod(MethodConfig{Intent: testStoredIntent(), Recipient: sponsoredIssuanceRecipient, ChainID: 42431})
			_, err := method.BuildChargeRequest(mppserver.ChargeParams{Amount: "0.50", FeePayer: tt.feePayer, Expires: tt.expires})
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestSponsoredChargeRefusesShortExpiryAtIssuance(t *testing.T) {
	t.Parallel()

	for _, sponsoredByDefault := range []bool{false, true} {
		method := NewMethod(MethodConfig{Intent: testStoredIntent(), Recipient: sponsoredIssuanceRecipient, ChainID: 42431, FeePayer: sponsoredByDefault})
		payment, err := mppserver.New(method, "api.example.com", "0123456789abcdef0123456789abcdef")
		require.NoError(t, err)

		result, err := payment.Charge(context.Background(), mppserver.ChargeParams{
			Amount: "0.50", FeePayer: !sponsoredByDefault, Expires: mpp.Expires.Seconds(10),
		})
		require.ErrorContains(t, err, "sponsored charge challenges must expire at least 15s after issuance")
		require.Nil(t, result)

		result, err = payment.Charge(context.Background(), mppserver.ChargeParams{
			Amount: "0.50", FeePayer: !sponsoredByDefault, Expires: mpp.Expires.Seconds(60),
		})
		require.NoError(t, err)
		require.True(t, result.IsChallenge())
	}
}
