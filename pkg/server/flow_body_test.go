package server

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tempoxyz/mpp-go/pkg/mpp"
)

// bodyBoundOfferMethod is a method whose offers bind the request body and
// require it to verify a Credential. (ComposeMiddleware builds every offer
// once at construction, with no request body or Credential.)
type bodyBoundOfferMethod struct{ composeTestMethod }

func (bodyBoundOfferMethod) BuildChargeRequest(params ChargeParams) (map[string]any, error) {
	body, _ := params.Body.([]byte)
	verifying := params.Authorization != "" || params.PaymentAuthorization != ""
	if verifying && len(body) == 0 {
		return nil, errors.New("offer needs the request body")
	}
	return map[string]any{
		"amount":   params.Amount,
		"currency": "usd",
		"bodySize": len(body),
	}, nil
}

// bodyBoundOffersMethod is bodyBoundOfferMethod issuing its offers through
// ChargeOffersBuilder.
type bodyBoundOffersMethod struct{ bodyBoundOfferMethod }

func (m bodyBoundOffersMethod) BuildChargeRequests(params ChargeParams) ([]map[string]any, error) {
	request, err := m.BuildChargeRequest(params)
	if err != nil {
		return nil, err
	}
	return []map[string]any{request}, nil
}

func TestPaidRetryBuildsOffersWithRequestBody(t *testing.T) {
	t.Parallel()

	methods := map[string]Method{
		"request builder": bodyBoundOfferMethod{composeTestMethod{name: "alpha"}},
		"offers builder":  bodyBoundOffersMethod{bodyBoundOfferMethod{composeTestMethod{name: "alpha"}}},
	}
	for name, method := range methods {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			payment, err := New(method, composeRealm, composeSecret)
			require.NoError(t, err)
			beta, err := New(composeTestMethod{name: "beta"}, composeRealm, composeSecret)
			require.NoError(t, err)
			resource := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte("paid:" + ReceiptFromContext(r.Context()).Reference))
			})
			handlers := map[string]http.Handler{
				"ChargeMiddleware": ChargeMiddleware(payment, ChargeParams{Amount: "1.00"})(resource),
				"ComposeMiddleware": ComposeMiddleware(
					ComposeConfig{Mpp: beta, Params: ChargeParams{Amount: "2.00"}},
					ComposeConfig{Mpp: payment, Params: ChargeParams{Amount: "1.00"}},
				)(resource),
			}
			for name, handler := range handlers {
				t.Run(name, func(t *testing.T) {
					t.Parallel()

					unpaid := httptest.NewRecorder()
					handler.ServeHTTP(unpaid, httptest.NewRequest(http.MethodPost, "/premium", strings.NewReader(`{"q":1}`)))
					require.Equal(t, http.StatusPaymentRequired, unpaid.Code)
					var challenge *mpp.Challenge
					for _, header := range unpaid.Header().Values(mpp.HeaderWWWAuthenticate) {
						c, err := mpp.ParseChallenge(header)
						require.NoError(t, err)
						if c.Method == "alpha" {
							challenge = c
						}
					}
					require.NotNil(t, challenge)

					r := httptest.NewRequest(http.MethodPost, "/premium", strings.NewReader(`{"q":1}`))
					r.Header.Set(mpp.HeaderAuthorization, paymentFor(challenge))
					paid := httptest.NewRecorder()
					handler.ServeHTTP(paid, r)
					assert.Equal(t, http.StatusOK, paid.Code, paid.Body.String())
					assert.Equal(t, "paid:0xreceipt-alpha", paid.Body.String())
				})
			}
		})
	}
}
