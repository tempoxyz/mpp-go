package mpp

import (
	"github.com/stretchr/testify/require"
	"strings"
	"testing"
)

func TestAuthorizationLimitBeforeSchemeExtraction(t *testing.T) {
	for _, header := range []string{
		strings.Repeat(" ", maxHeaderPayload) + "Payment abc",
		strings.Repeat("Bearer x,", maxHeaderPayload) + "Payment abc",
		"Payment abc," + strings.Repeat("x", maxHeaderPayload),
	} {
		require.Empty(t, FindPaymentAuthorization(header))
		_, err := FindPaymentAuthorizationStrict(header)
		require.ErrorContains(t, err, "maximum size")
	}
	header := "Payment " + strings.Repeat("a", maxHeaderPayload-len("Payment "))
	got, err := FindPaymentAuthorizationStrict(header)
	require.NoError(t, err)
	require.Equal(t, header, got)
}
