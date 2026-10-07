package mpp

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAuthorizationLimitBeforeSchemeExtraction(t *testing.T) {
	for _, header := range []string{
		strings.Repeat(" ", maxAuthorizationPayload) + "Payment abc",
		strings.Repeat("Bearer x,", maxAuthorizationPayload) + "Payment abc",
		"Payment abc," + strings.Repeat("x", maxAuthorizationPayload),
	} {
		require.Empty(t, FindPaymentAuthorization(header))
		_, err := FindPaymentAuthorizationStrict(header)
		require.ErrorContains(t, err, "maximum size")
	}
	header := "Payment " + strings.Repeat("a", maxAuthorizationPayload-len("Payment "))
	got, err := FindPaymentAuthorizationStrict(header)
	require.NoError(t, err)
	require.Equal(t, header, got)
}
