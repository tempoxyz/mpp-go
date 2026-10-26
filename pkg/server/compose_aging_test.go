package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tempoxyz/mpp-go/pkg/mpp"
)

// agingOfferMethod builds offers like the Tempo method does: once aged is set,
// offer "B" can no longer be issued, but a request carrying a credential still
// builds, as the sponsored-expiry floor allows.
type agingOfferMethod struct {
	composeTestMethod
	aged *atomic.Bool
}

func (m agingOfferMethod) BuildChargeRequest(params ChargeParams) (map[string]any, error) {
	credential := params.Authorization != "" || params.PaymentAuthorization != ""
	if m.aged.Load() && params.Currency == "B" && !credential {
		return nil, errors.New("offer can no longer be issued")
	}
	return map[string]any{"amount": params.Amount, "currency": params.Currency}, nil
}

func TestComposeAgingOfferDoesNotRejectOtherOffers(t *testing.T) {
	aged := new(atomic.Bool)
	payment, err := New(agingOfferMethod{composeTestMethod{name: "test"}, aged}, composeRealm, composeSecret)
	require.NoError(t, err)
	configs := []ComposeConfig{
		{Mpp: payment, Params: ChargeParams{Amount: "1", Currency: "A"}},
		{Mpp: payment, Params: ChargeParams{Amount: "1", Currency: "B"}},
	}

	handler := ComposeMiddleware(configs...)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	offered, err := Compose(context.Background(), "", configs...)
	require.NoError(t, err)
	require.Len(t, offered.Challenges, 2)
	authorization := mustFormatCredential(t, offered.Challenges[0].NewCredential(map[string]any{}))
	unpaid := httptest.NewRecorder()
	handler.ServeHTTP(unpaid, httptest.NewRequest(http.MethodGet, "/", nil))
	require.Equal(t, http.StatusPaymentRequired, unpaid.Code)
	httpChallenge, err := mpp.ParseChallenge(unpaid.Header().Values(mpp.HeaderWWWAuthenticate)[0])
	require.NoError(t, err)
	httpAuthorization := mustFormatCredential(t, httpChallenge.NewCredential(map[string]any{}))
	aged.Store(true)

	paid, err := Compose(context.Background(), authorization, configs...)
	require.NoError(t, err)
	require.Nil(t, paid.Challenge)
	assert.Equal(t, "0xreceipt-test", paid.Receipt.Reference)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(mpp.HeaderAuthorization, httpAuthorization)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func mustFormatCredential(t *testing.T, credential *mpp.Credential) string {
	t.Helper()
	header, err := mpp.FormatCredential(credential)
	require.NoError(t, err)
	return header
}
