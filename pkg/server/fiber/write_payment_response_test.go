package fiberadapter

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	fiberfw "github.com/gofiber/fiber/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tempoxyz/mpp-go/pkg/mpp"
)

func TestWritePaymentResponse(t *testing.T) {
	t.Parallel()

	challenge, err := mpp.NewChallengeWithError("secret", "api.example.com", "tempo", mpp.IntentCharge, map[string]any{"amount": "1"})
	require.NoError(t, err)

	app := fiberfw.New()
	app.Get("/initial", func(c *fiberfw.Ctx) error {
		WritePaymentResponse(c, nil, challenge)
		return nil
	})
	app.Get("/retry", func(c *fiberfw.Ctx) error {
		WritePaymentResponse(c, mpp.ErrVerificationFailed("bad"), challenge)
		return nil
	})
	app.Get("/unknown", func(c *fiberfw.Ctx) error {
		WritePaymentResponse(c, mpp.ErrPaymentOutcomeUnknown("abc"), challenge)
		return nil
	})
	app.Get("/none", func(c *fiberfw.Ctx) error {
		WritePaymentResponse(c, nil)
		return nil
	})

	for _, tc := range []struct {
		path       string
		status     int
		challenges int
		problem    mpp.ErrorType
	}{
		{"/initial", 402, 1, mpp.ErrorTypePaymentRequired},
		{"/retry", 402, 1, mpp.ErrorTypeVerificationFailed},
		{"/unknown", 402, 0, mpp.ErrorTypePaymentOutcomeUnknown},
		{"/none", 500, 0, mpp.ErrorTypeInternalPayment},
	} {
		resp, err := app.Test(httptest.NewRequest(http.MethodGet, tc.path, nil))
		require.NoError(t, err)
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		assert.Equal(t, tc.status, resp.StatusCode, tc.path)
		values := resp.Header.Values(mpp.HeaderWWWAuthenticate)
		assert.Len(t, values, tc.challenges, tc.path)
		for _, value := range values {
			parsed, err := mpp.ParseChallenge(value)
			require.NoError(t, err)
			assert.True(t, parsed.VerifyID("secret"), tc.path)
		}
		assert.Contains(t, string(body), string(tc.problem), tc.path)
	}
}
