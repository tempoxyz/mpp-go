package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tempoxyz/mpp-go/pkg/mpp"
)

// failingOfferMethod is a method whose offers cannot be built.
type failingOfferMethod struct{ composeTestMethod }

func (failingOfferMethod) BuildChargeRequest(ChargeParams) (map[string]any, error) {
	return nil, errors.New("offer unavailable")
}

func TestChargeRequestServeHTTPSetsReceiptBeforeResource(t *testing.T) {
	t.Parallel()

	payment := apiTestServer(t)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		res, err := payment.ChargeRequest(r, ChargeParams{Amount: "1.00"})
		res.ServeHTTP(w, r)
		if err != nil || res.IsChallenge() {
			return
		}
		_, _ = w.Write([]byte("paid"))
	})

	unpaid := httptest.NewRecorder()
	handler.ServeHTTP(unpaid, httptest.NewRequest(http.MethodGet, "/premium", nil))
	require.Equal(t, http.StatusPaymentRequired, unpaid.Code)
	assert.Empty(t, unpaid.Body.String())
	challenge, err := mpp.ParseChallenge(unpaid.Header().Get(mpp.HeaderWWWAuthenticate))
	require.NoError(t, err)

	r := httptest.NewRequest(http.MethodGet, "/premium", nil)
	r.Header.Set(mpp.HeaderAuthorization, paymentFor(challenge))
	paid := httptest.NewRecorder()
	handler.ServeHTTP(paid, r)
	assert.Equal(t, http.StatusOK, paid.Code)
	assert.Equal(t, "paid", paid.Body.String())
	receipt, err := mpp.ParseReceipt(paid.Header().Get(mpp.HeaderPaymentReceipt))
	require.NoError(t, err)
	assert.Equal(t, "0xreceipt-alpha", receipt.Reference)
	assert.Equal(t, "private", paid.Header().Get("Cache-Control"))
}

func TestChallengeCreatedOnlyForDeliveredChallenges(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	created := map[string]int{}
	events := WithEvents(Events{ChallengeCreated: func(_ context.Context, challenge *mpp.Challenge) {
		mu.Lock()
		defer mu.Unlock()
		created[challenge.Method]++
	}})
	alpha := apiTestServer(t, events)
	beta, err := New(composeTestMethod{name: "beta"}, composeRealm, composeSecret, events)
	require.NoError(t, err)
	broken, err := New(failingOfferMethod{composeTestMethod{name: "broken"}}, composeRealm, composeSecret, events)
	require.NoError(t, err)

	// A later offer that cannot be issued returns no Challenge, so none is
	// reported, including the earlier offer's.
	res, err := ComposeRequest(httptest.NewRequest(http.MethodGet, "/premium", nil),
		Offer{Mpp: alpha, Params: ChargeParams{Amount: "1.00"}},
		Offer{Mpp: broken, Params: ChargeParams{Amount: "1.00"}},
	)
	require.Error(t, err)
	assert.False(t, res.IsChallenge())
	_, err = Compose(context.Background(), "",
		Offer{Mpp: alpha, Params: ChargeParams{Amount: "1.00"}},
		Offer{Mpp: broken, Params: ChargeParams{Amount: "1.00"}},
	)
	require.Error(t, err)
	mu.Lock()
	assert.Empty(t, created)
	mu.Unlock()

	// A delivered set is reported once per Challenge.
	res, err = ComposeRequest(httptest.NewRequest(http.MethodGet, "/premium", nil),
		Offer{Mpp: alpha, Params: ChargeParams{Amount: "1.00"}},
		Offer{Mpp: beta, Params: ChargeParams{Amount: "1.00"}},
	)
	require.NoError(t, err)
	require.Len(t, res.Challenges, 2)
	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, map[string]int{"alpha": 1, "beta": 1}, created)
}

func TestChallengeCreatedReportsEachChallengeToItsIssuer(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	var alphaSeen, betaSeen []string
	alpha := apiTestServer(t, WithEvents(Events{ChallengeCreated: func(_ context.Context, c *mpp.Challenge) {
		mu.Lock()
		defer mu.Unlock()
		alphaSeen = append(alphaSeen, c.Method)
	}}))
	beta, err := New(composeTestMethod{name: "beta"}, composeRealm, composeSecret, WithEvents(Events{ChallengeCreated: func(_ context.Context, c *mpp.Challenge) {
		mu.Lock()
		defer mu.Unlock()
		betaSeen = append(betaSeen, c.Method)
	}}))
	require.NoError(t, err)

	_, err = Compose(context.Background(), "",
		Offer{Mpp: alpha, Params: ChargeParams{Amount: "1.00"}},
		Offer{Mpp: beta, Params: ChargeParams{Amount: "1.00"}},
	)
	require.NoError(t, err)
	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, []string{"alpha"}, alphaSeen)
	assert.Equal(t, []string{"beta"}, betaSeen)
}

func TestComposeRequiresAuthOfferRejectsCredentialInAuthorization(t *testing.T) {
	t.Parallel()

	alpha := apiTestServer(t)
	beta, err := New(composeTestMethod{name: "beta"}, composeRealm, composeSecret, WithRequiresAuth(true))
	require.NoError(t, err)
	handler := ComposeMiddleware(
		ComposeConfig{Mpp: alpha, Params: ChargeParams{Amount: "1.00"}},
		ComposeConfig{Mpp: beta, Params: ChargeParams{Amount: "1.00"}},
	)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) }))

	unpaid := httptest.NewRecorder()
	handler.ServeHTTP(unpaid, httptest.NewRequest(http.MethodGet, "/premium", nil))
	require.Equal(t, http.StatusPaymentRequired, unpaid.Code)
	var betaChallenge *mpp.Challenge
	for _, value := range unpaid.Header().Values(mpp.HeaderWWWAuthenticate) {
		challenge, err := mpp.ParseChallenge(value)
		require.NoError(t, err)
		if challenge.Method == "beta" {
			betaChallenge = challenge
		}
	}
	require.NotNil(t, betaChallenge)

	// beta reads its Credential from Payment-Authorization only.
	r := httptest.NewRequest(http.MethodGet, "/premium", nil)
	r.Header.Set(mpp.HeaderAuthorization, paymentFor(betaChallenge))
	rejected := httptest.NewRecorder()
	handler.ServeHTTP(rejected, r)
	assert.Equal(t, http.StatusPaymentRequired, rejected.Code)
	assert.NotEqual(t, "ok", rejected.Body.String())
	assert.Empty(t, rejected.Header().Get(mpp.HeaderPaymentReceipt))
	retry := rejected.Header().Values(mpp.HeaderWWWAuthenticate)
	require.Len(t, retry, 1)
	assert.Contains(t, retry[0], mpp.HeaderPaymentAuthorization)

	r = httptest.NewRequest(http.MethodGet, "/premium", nil)
	r.Header.Set(mpp.HeaderPaymentAuthorization, paymentFor(betaChallenge))
	accepted := httptest.NewRecorder()
	handler.ServeHTTP(accepted, r)
	assert.Equal(t, http.StatusOK, accepted.Code)
	assert.Equal(t, "ok", accepted.Body.String())
}

func TestComposeRequiresAuthOfferSameMethodRejectsCredentialInAuthorization(t *testing.T) {
	t.Parallel()

	plain := apiTestServer(t)
	authed := apiTestServer(t, WithRequiresAuth(true))
	offers := []Offer{
		{Mpp: plain, Params: ChargeParams{Amount: "1.00"}},
		{Mpp: authed, Params: ChargeParams{Amount: "2.00"}},
	}
	issued, err := ComposeRequest(httptest.NewRequest(http.MethodGet, "/premium", nil), offers...)
	require.NoError(t, err)
	require.Len(t, issued.Challenges, 2)

	r := httptest.NewRequest(http.MethodGet, "/premium", nil)
	r.Header.Set(mpp.HeaderAuthorization, paymentFor(issued.Challenges[1]))
	res, err := ComposeRequest(r, offers...)
	require.Error(t, err)
	assert.Nil(t, res.Receipt)

	r = httptest.NewRequest(http.MethodGet, "/premium", nil)
	r.Header.Set(mpp.HeaderPaymentAuthorization, paymentFor(issued.Challenges[1]))
	res, err = ComposeRequest(r, offers...)
	require.NoError(t, err)
	require.NotNil(t, res.Receipt)
}

func TestChargeMiddlewareUnsupportedIntentGetsRetryChallenge(t *testing.T) {
	t.Parallel()

	payment := apiTestServer(t)
	handler := ChargeMiddleware(payment, ChargeParams{Amount: "1.00"})(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	issued, err := payment.Charge(context.Background(), ChargeParams{Amount: "1.00"})
	require.NoError(t, err)

	// A Credential echoing an intent the method lacks is rejected with a
	// retry offer, as before the unified flow.
	session := *issued.Challenge
	session.Intent = "session"
	r := httptest.NewRequest(http.MethodGet, "/premium", nil)
	r.Header.Set(mpp.HeaderAuthorization, paymentFor(&session))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, r)
	assert.Equal(t, http.StatusPaymentRequired, rec.Code)
	require.Len(t, rec.Header().Values(mpp.HeaderWWWAuthenticate), 1)
	var problem struct {
		Type string `json:"type"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &problem))
	assert.Contains(t, problem.Type, "invalid-challenge")

	// A Credential for a method no offer has is still a 400.
	other := *issued.Challenge
	other.Method = "zeta"
	r = httptest.NewRequest(http.MethodGet, "/premium", nil)
	r.Header.Set(mpp.HeaderAuthorization, paymentFor(&other))
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, r)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Empty(t, rec.Header().Values(mpp.HeaderWWWAuthenticate))
}

func TestPanickingHooksDoNotAbortRequests(t *testing.T) {
	t.Parallel()

	payment := apiTestServer(t, WithEvents(Events{
		ChallengeCreated: func(context.Context, *mpp.Challenge) { panic("challenge hook") },
		PaymentSucceeded: func(context.Context, *mpp.Credential, *mpp.Receipt) { panic("success hook") },
		PaymentFailed:    func(context.Context, *mpp.Credential, error) { panic("failure hook") },
	}))
	handler := ChargeMiddleware(payment, ChargeParams{Amount: "1.00"})(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))

	unpaid := httptest.NewRecorder()
	handler.ServeHTTP(unpaid, httptest.NewRequest(http.MethodGet, "/premium", nil))
	require.Equal(t, http.StatusPaymentRequired, unpaid.Code)
	challenge, err := mpp.ParseChallenge(unpaid.Header().Get(mpp.HeaderWWWAuthenticate))
	require.NoError(t, err)

	r := httptest.NewRequest(http.MethodGet, "/premium", nil)
	r.Header.Set(mpp.HeaderAuthorization, paymentFor(challenge))
	paid := httptest.NewRecorder()
	handler.ServeHTTP(paid, r)
	assert.Equal(t, http.StatusOK, paid.Code)
	assert.Equal(t, "ok", paid.Body.String())
	assert.NotEmpty(t, paid.Header().Get(mpp.HeaderPaymentReceipt))

	r = httptest.NewRequest(http.MethodGet, "/premium", nil)
	r.Header.Set(mpp.HeaderAuthorization, "Payment not-base64!")
	malformed := httptest.NewRecorder()
	handler.ServeHTTP(malformed, r)
	assert.Equal(t, http.StatusPaymentRequired, malformed.Code)
}
