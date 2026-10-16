package echoadapter

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	echofw "github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tempoxyz/mpp-go/pkg/mpp"
	"github.com/tempoxyz/mpp-go/pkg/server"
)

var testOffers = []string{
	"0x20c0000000000000000000006a37DA5C996874BE",
	"0x20C000000000000000000000b9537d11c60E8b50",
}

// offersTestMethod issues one charge offer per currency.
type offersTestMethod struct {
	fail bool
}

func (offersTestMethod) Name() string { return "tempo" }

func (m offersTestMethod) Intents() map[string]server.Intent {
	return map[string]server.Intent{"charge": offersTestIntent{fail: m.fail}}
}

func (offersTestMethod) BuildChargeRequests(params server.ChargeParams) ([]map[string]any, error) {
	requests := make([]map[string]any, 0, len(testOffers))
	for _, currency := range testOffers {
		requests = append(requests, map[string]any{"amount": params.Amount, "currency": currency})
	}
	return requests, nil
}

type offersTestIntent struct {
	fail bool
}

func (offersTestIntent) Name() string { return "charge" }

func (i offersTestIntent) Verify(_ context.Context, _ *mpp.Credential, request map[string]any) (*mpp.Receipt, error) {
	if i.fail {
		return nil, mpp.ErrVerificationFailed("offer rejected")
	}
	return mpp.Success("tempo", "0xreceipt-"+request["currency"].(string)), nil
}

func offeredCurrencies(header http.Header) ([]string, []*mpp.Challenge, error) {
	var currencies []string
	var challenges []*mpp.Challenge
	for _, value := range header.Values(mpp.HeaderWWWAuthenticate) {
		challenge, err := mpp.ParseChallenge(value)
		if err != nil {
			return nil, nil, err
		}
		currencies = append(currencies, challenge.Request["currency"].(string))
		challenges = append(challenges, challenge)
	}
	return currencies, challenges, nil
}

func TestChargeMiddleware_AdvertisesEveryOffer(t *testing.T) {
	t.Parallel()

	for _, fail := range []bool{false, true} {
		payment := newTestServer(t, offersTestMethod{fail: fail}, "api.example.com", "test-secret-key-minimum-32-byte-secret")
		e := echofw.New()
		e.GET("/paid", func(c echofw.Context) error {
			return c.String(http.StatusOK, Receipt(c).Reference)
		}, ChargeMiddleware(payment, server.ChargeParams{Amount: "1"}))

		recorder := httptest.NewRecorder()
		e.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/paid", nil))
		require.Equal(t, http.StatusPaymentRequired, recorder.Code)
		currencies, challenges, err := offeredCurrencies(recorder.Header())
		require.NoError(t, err)
		require.Equal(t, testOffers, currencies)

		request := httptest.NewRequest(http.MethodGet, "/paid", nil)
		request.Header.Set(mpp.HeaderAuthorization, challenges[1].NewCredential(map[string]any{"type": "test"}).ToAuthorization())
		paid := httptest.NewRecorder()
		e.ServeHTTP(paid, request)
		if fail {
			require.Equal(t, http.StatusPaymentRequired, paid.Code)
			retry, _, err := offeredCurrencies(paid.Header())
			require.NoError(t, err)
			assert.Equal(t, testOffers, retry)
			continue
		}
		require.Equal(t, http.StatusOK, paid.Code)
		assert.Equal(t, "0xreceipt-"+testOffers[1], paid.Body.String())
	}
}
