package chargeserver

import (
	"context"
	"testing"
	"time"

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
		{name: "sponsored expiring in 10s", feePayer: true, expires: mpp.ExpiresIn(10 * time.Second), wantErr: true},
		{name: "sponsored already expired", feePayer: true, expires: mpp.ExpiresIn(-5 * time.Second), wantErr: true},
		{name: "sponsored with unparseable expires", feePayer: true, expires: "soon", wantErr: true},
		{name: "sponsored expiring in 30s", feePayer: true, expires: mpp.ExpiresIn(30 * time.Second)},
		{name: "sponsored with default expiry", feePayer: true},
		{name: "unsponsored expiring in 10s", expires: mpp.ExpiresIn(10 * time.Second)},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			method := newMethod(methodConfig{Intent: testStoredIntent(), Recipient: sponsoredIssuanceRecipient, ChainID: 42431})
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
		method := newMethod(methodConfig{Intent: testStoredIntent(), Recipient: sponsoredIssuanceRecipient, ChainID: 42431, FeePayer: sponsoredByDefault})
		payment, err := mppserver.New(method, "api.example.com", "0123456789abcdef0123456789abcdef")
		require.NoError(t, err)

		result, err := payment.Charge(context.Background(), mppserver.ChargeParams{
			Amount: "0.50", FeePayer: !sponsoredByDefault, Expires: mpp.ExpiresIn(10 * time.Second),
		})
		require.ErrorContains(t, err, "sponsored charge challenges must expire at least 15s after issuance")
		require.Nil(t, result)

		result, err = payment.Charge(context.Background(), mppserver.ChargeParams{
			Amount: "0.50", FeePayer: !sponsoredByDefault, Expires: mpp.ExpiresIn(60 * time.Second),
		})
		require.NoError(t, err)
		require.True(t, result.IsChallenge())
	}
}

func TestSponsoredIssuanceFloorSkipsZeroAmountProofCharges(t *testing.T) {
	t.Parallel()

	for _, sponsoredByDefault := range []bool{false, true} {
		method := newMethod(methodConfig{Intent: testStoredIntent(), Recipient: sponsoredIssuanceRecipient, ChainID: 42431, FeePayer: sponsoredByDefault})
		_, err := method.BuildChargeRequest(mppserver.ChargeParams{Amount: "0", FeePayer: !sponsoredByDefault, Expires: mpp.ExpiresIn(10 * time.Second)})
		require.NoError(t, err)

		payment, err := mppserver.New(method, "api.example.com", "0123456789abcdef0123456789abcdef")
		require.NoError(t, err)
		result, err := payment.Charge(context.Background(), mppserver.ChargeParams{
			Amount: "0", FeePayer: !sponsoredByDefault, Expires: mpp.ExpiresIn(10 * time.Second),
		})
		require.NoError(t, err)
		require.True(t, result.IsChallenge())
	}
}

func TestSponsoredIssuanceFloorSkipsCredentialVerification(t *testing.T) {
	t.Parallel()

	method := newMethod(methodConfig{Intent: testStoredIntent(), Recipient: sponsoredIssuanceRecipient, ChainID: 42431, FeePayer: true})
	expires := mpp.ExpiresIn(10 * time.Second)

	// A request carrying a Payment credential is verified against its echoed
	// expiry, so a fixed Expires near its end must not be refused here.
	_, err := method.BuildChargeRequest(mppserver.ChargeParams{Amount: "0.50", Expires: expires, Authorization: "Payment eyJ9"})
	require.NoError(t, err)
	_, err = method.BuildChargeRequest(mppserver.ChargeParams{Amount: "0.50", Expires: expires, PaymentAuthorization: "Payment eyJ9"})
	require.NoError(t, err)

	// Application authentication alone still issues a challenge, so the floor applies.
	_, err = method.BuildChargeRequest(mppserver.ChargeParams{Amount: "0.50", Expires: expires, Authorization: "Bearer app-token"})
	require.ErrorContains(t, err, "sponsored charge challenges must expire at least 15s after issuance")
}
