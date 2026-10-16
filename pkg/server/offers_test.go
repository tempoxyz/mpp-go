package server

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tempoxyz/mpp-go/pkg/mpp"
)

const (
	offerA = "0x20c0000000000000000000006a37DA5C996874BE"
	offerB = "0x20C000000000000000000000b9537d11c60E8b50"
	offerC = "0x20c0000000000000000000000000000000000001"
)

// offersTestMethod offers one charge request per currency unless the request
// overrides the currency.
type offersTestMethod struct {
	name       string
	currencies []string
	intent     Intent
	err        error
}

func (m offersTestMethod) Name() string {
	if m.name == "" {
		return "tempo"
	}
	return m.name
}

func (m offersTestMethod) Intents() map[string]Intent {
	return map[string]Intent{"charge": m.intent}
}

func (m offersTestMethod) BuildChargeRequests(params ChargeParams) ([]map[string]any, error) {
	if m.err != nil {
		return nil, m.err
	}
	currencies := m.currencies
	if params.Currency != "" {
		currencies = []string{params.Currency}
	}
	requests := make([]map[string]any, 0, len(currencies))
	for _, currency := range currencies {
		requests = append(requests, map[string]any{"amount": params.Amount, "currency": currency})
	}
	return requests, nil
}

// offersBothBuilders implements both builder interfaces; offers must win.
type offersBothBuilders struct {
	offersTestMethod
}

func (m offersBothBuilders) BuildChargeRequest(params ChargeParams) (map[string]any, error) {
	return map[string]any{"amount": params.Amount, "currency": "single"}, nil
}

// recordingOfferIntent records the currency of each verified request.
type recordingOfferIntent struct {
	mu       sync.Mutex
	verified []string
	err      error
}

func (i *recordingOfferIntent) Name() string { return "charge" }

func (i *recordingOfferIntent) Verify(_ context.Context, _ *mpp.Credential, request map[string]any) (*mpp.Receipt, error) {
	if i.err != nil {
		return nil, i.err
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	currency, _ := request["currency"].(string)
	i.verified = append(i.verified, currency)
	return mpp.Success("tempo", "0xreceipt-"+currency), nil
}

func (i *recordingOfferIntent) currencies() []string {
	i.mu.Lock()
	defer i.mu.Unlock()
	return append([]string(nil), i.verified...)
}

func newOffersTestServer(t *testing.T, intent Intent, currencies ...string) *Mpp {
	t.Helper()
	return newTestServer(t, offersTestMethod{currencies: currencies, intent: intent}, "api.example.com", "test-secret-key-minimum-32-byte-secret")
}

func offeredCurrencies(challenges []*mpp.Challenge) []string {
	currencies := make([]string, 0, len(challenges))
	for _, challenge := range challenges {
		currency, _ := challenge.Request["currency"].(string)
		currencies = append(currencies, currency)
	}
	return currencies
}

func TestMppCharge_IssuesOneChallengePerOfferInOrder(t *testing.T) {
	t.Parallel()

	payment := newOffersTestServer(t, &recordingOfferIntent{}, offerA, offerB)
	result, err := payment.Charge(context.Background(), ChargeParams{Amount: "1"})
	require.NoError(t, err)
	require.True(t, result.IsChallenge())
	require.Len(t, result.Challenges, 2)
	assert.Equal(t, []string{offerA, offerB}, offeredCurrencies(result.Challenges))
	assert.Same(t, result.Challenges[0], result.Challenge)
	assert.NotEqual(t, result.Challenges[0].ID, result.Challenges[1].ID)
	assert.Equal(t, result.Challenges[0].Expires, result.Challenges[1].Expires)
	assert.NotEmpty(t, result.Challenges[0].Expires)
	assert.Nil(t, result.Receipt)
	assert.Nil(t, result.Credential)
}

func TestMppCharge_OffersShareExplicitExpiresMetaAndDigest(t *testing.T) {
	t.Parallel()

	payment := newOffersTestServer(t, &recordingOfferIntent{}, offerA, offerB, offerC)
	expires := mpp.Expires.Minutes(10)
	result, err := payment.Charge(context.Background(), ChargeParams{
		Amount:      "1",
		Expires:     expires,
		Meta:        map[string]string{"trace": "abc"},
		Body:        []byte(`{"q":1}`),
		Description: "coffee",
	})
	require.NoError(t, err)
	require.Len(t, result.Challenges, 3)
	for _, challenge := range result.Challenges {
		assert.Equal(t, expires, challenge.Expires)
		assert.Equal(t, map[string]string{"trace": "abc"}, challenge.Opaque)
		assert.Equal(t, mpp.BodyDigest.Compute([]byte(`{"q":1}`)), challenge.Digest)
		assert.Equal(t, "coffee", challenge.Description)
	}
}

func TestMppCharge_VerifiesCredentialForAnyOffer(t *testing.T) {
	t.Parallel()

	for index, currency := range []string{offerA, offerB} {
		t.Run(currency, func(t *testing.T) {
			t.Parallel()

			intent := &recordingOfferIntent{}
			payment := newOffersTestServer(t, intent, offerA, offerB)
			issued, err := payment.Charge(context.Background(), ChargeParams{Amount: "1"})
			require.NoError(t, err)

			credential := issued.Challenges[index].NewCredential(map[string]any{"type": "test"})
			result, err := payment.Charge(context.Background(), ChargeParams{Amount: "1", Authorization: credential.ToAuthorization()})
			require.NoError(t, err)
			require.False(t, result.IsChallenge())
			assert.Empty(t, result.Challenges)
			assert.Equal(t, "0xreceipt-"+currency, result.Receipt.Reference)
			assert.Equal(t, []string{currency}, intent.currencies())
		})
	}
}

func TestMppCharge_RejectsCredentialForUnofferedCurrency(t *testing.T) {
	t.Parallel()

	intent := &recordingOfferIntent{}
	payment := newOffersTestServer(t, intent, offerA, offerB)

	// Signed by this server, but for a currency this route does not offer.
	other, err := payment.Charge(context.Background(), ChargeParams{Amount: "1", Currency: offerC})
	require.NoError(t, err)
	credential := other.Challenge.NewCredential(map[string]any{"type": "test"})

	result, err := payment.Charge(context.Background(), ChargeParams{Amount: "1", Authorization: credential.ToAuthorization()})
	var paymentErr *mpp.PaymentError
	require.ErrorAs(t, err, &paymentErr)
	assert.Equal(t, mpp.ErrorTypeInvalidChallenge, paymentErr.Type)
	assert.Contains(t, err.Error(), "credential request does not match this route's requirements")
	require.NotNil(t, result)
	assert.Equal(t, []string{offerA, offerB}, offeredCurrencies(result.Challenges))
	assert.Same(t, result.Challenges[0], result.Challenge)
	assert.Empty(t, intent.currencies())
}

func TestMppCharge_RejectsForgedOffer(t *testing.T) {
	t.Parallel()

	intent := &recordingOfferIntent{}
	payment := newOffersTestServer(t, intent, offerA, offerB)
	forged := mpp.NewChallenge("another-secret-key-minimum-32-bytes!!", "api.example.com", "tempo", "charge",
		map[string]any{"amount": "1", "currency": offerB}, mpp.WithExpires(mpp.Expires.Minutes(5)))
	credential := forged.NewCredential(map[string]any{"type": "test"})

	result, err := payment.Charge(context.Background(), ChargeParams{Amount: "1", Authorization: credential.ToAuthorization()})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "challenge was not issued by this server")
	assert.Equal(t, []string{offerA, offerB}, offeredCurrencies(result.Challenges))
	assert.Empty(t, intent.currencies())
}

func TestMppCharge_VerificationFailureReissuesEveryOffer(t *testing.T) {
	t.Parallel()

	intent := &recordingOfferIntent{err: mpp.ErrVerificationFailed("insufficient balance")}
	payment := newOffersTestServer(t, intent, offerA, offerB)
	issued, err := payment.Charge(context.Background(), ChargeParams{Amount: "1"})
	require.NoError(t, err)

	credential := issued.Challenges[1].NewCredential(map[string]any{"type": "test"})
	result, err := payment.Charge(context.Background(), ChargeParams{Amount: "1", Authorization: credential.ToAuthorization()})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "insufficient balance")
	require.Len(t, result.Challenges, 2)
	assert.Equal(t, []string{offerA, offerB}, offeredCurrencies(result.Challenges))
	// The primary Challenge is the offer the client selected.
	assert.Same(t, result.Challenges[1], result.Challenge)
}

func TestMppCharge_MalformedCredentialReissuesEveryOffer(t *testing.T) {
	t.Parallel()

	payment := newOffersTestServer(t, &recordingOfferIntent{}, offerA, offerB)
	result, err := payment.Charge(context.Background(), ChargeParams{Amount: "1", Authorization: "Payment not-base64!"})
	require.Error(t, err)
	assert.Equal(t, []string{offerA, offerB}, offeredCurrencies(result.Challenges))
}

func TestMppCharge_PerRequestCurrencyOverrideIssuesSingleOffer(t *testing.T) {
	t.Parallel()

	intent := &recordingOfferIntent{}
	payment := newOffersTestServer(t, intent, offerA, offerB)
	issued, err := payment.Charge(context.Background(), ChargeParams{Amount: "1", Currency: offerC})
	require.NoError(t, err)
	assert.Equal(t, []string{offerC}, offeredCurrencies(issued.Challenges))

	credential := issued.Challenge.NewCredential(map[string]any{"type": "test"})
	result, err := payment.Charge(context.Background(), ChargeParams{Amount: "1", Currency: offerC, Authorization: credential.ToAuthorization()})
	require.NoError(t, err)
	assert.Equal(t, "0xreceipt-"+offerC, result.Receipt.Reference)
}

func TestMppCharge_SingleOfferMethodsPopulateChallenges(t *testing.T) {
	t.Parallel()

	payment := newTestServer(t, chargeTestMethod{intents: map[string]Intent{"charge": verifyTestIntent{}}}, "api.example.com", "test-secret-key-minimum-32-byte-secret")
	result, err := payment.Charge(context.Background(), ChargeParams{Amount: "1", Currency: offerA})
	require.NoError(t, err)
	require.Len(t, result.Challenges, 1)
	assert.Same(t, result.Challenge, result.Challenges[0])
}

func TestMppCharge_OffersBuilderTakesPrecedence(t *testing.T) {
	t.Parallel()

	method := offersBothBuilders{offersTestMethod{currencies: []string{offerA, offerB}, intent: &recordingOfferIntent{}}}
	payment := newTestServer(t, method, "api.example.com", "test-secret-key-minimum-32-byte-secret")
	result, err := payment.Charge(context.Background(), ChargeParams{Amount: "1"})
	require.NoError(t, err)
	assert.Equal(t, []string{offerA, offerB}, offeredCurrencies(result.Challenges))
}

func TestMppCharge_OffersBuilderErrors(t *testing.T) {
	t.Parallel()

	failing := newTestServer(t, offersTestMethod{err: errors.New("boom"), intent: &recordingOfferIntent{}}, "api.example.com", "test-secret-key-minimum-32-byte-secret")
	result, err := failing.Charge(context.Background(), ChargeParams{Amount: "1"})
	require.EqualError(t, err, "boom")
	assert.Nil(t, result)

	empty := newOffersTestServer(t, &recordingOfferIntent{})
	result, err = empty.Charge(context.Background(), ChargeParams{Amount: "1"})
	require.EqualError(t, err, `server: method "tempo" returned no charge offers`)
	assert.Nil(t, result)
}

func TestMppCharge_OffersBindMppxScope(t *testing.T) {
	t.Parallel()

	payment := newOffersTestServer(t, &recordingOfferIntent{}, offerA, offerB)
	scope := map[string]string{"route": "/paid"}
	result, err := payment.Charge(context.Background(), ChargeParams{Amount: "1", MppxScope: scope})
	require.NoError(t, err)
	for _, challenge := range result.Challenges {
		assert.Equal(t, map[string]string{"route": "/paid"}, challenge.Request["_mppx_scope"])
	}
}

func TestChargeMiddleware_AdvertisesEveryOfferAndAcceptsAny(t *testing.T) {
	t.Parallel()

	intent := &recordingOfferIntent{}
	payment := newOffersTestServer(t, intent, offerA, offerB)
	handler := ChargeMiddleware(payment, ChargeParams{Amount: "1"})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, ReceiptFromContext(r.Context()).Reference)
	}))

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/paid", nil))
	require.Equal(t, http.StatusPaymentRequired, recorder.Code)
	challenges := parseAuthenticateValues(t, recorder.Header())
	require.Equal(t, []string{offerA, offerB}, offeredCurrencies(challenges))

	for _, challenge := range []*mpp.Challenge{challenges[1], challenges[0]} {
		request := httptest.NewRequest(http.MethodGet, "/paid", nil)
		request.Header.Set(mpp.HeaderAuthorization, challenge.NewCredential(map[string]any{"type": "test"}).ToAuthorization())
		paid := httptest.NewRecorder()
		handler.ServeHTTP(paid, request)
		require.Equal(t, http.StatusOK, paid.Code)
		assert.Equal(t, "0xreceipt-"+challenge.Request["currency"].(string), paid.Body.String())
	}
	assert.Equal(t, []string{offerB, offerA}, intent.currencies())
}

func TestChargeMiddleware_VerificationFailureAdvertisesEveryOffer(t *testing.T) {
	t.Parallel()

	payment := newOffersTestServer(t, &recordingOfferIntent{err: mpp.ErrVerificationFailed("nope")}, offerA, offerB)
	handler := ChargeMiddleware(payment, ChargeParams{Amount: "1"})(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("handler must not run")
	}))
	issued, err := payment.Charge(context.Background(), ChargeParams{Amount: "1", MppxScope: map[string]string{"resource": "/paid"}})
	require.NoError(t, err)

	request := httptest.NewRequest(http.MethodGet, "/paid", nil)
	request.Header.Set(mpp.HeaderAuthorization, issued.Challenges[1].NewCredential(map[string]any{"type": "test"}).ToAuthorization())
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	require.Equal(t, http.StatusPaymentRequired, recorder.Code)
	assert.Equal(t, []string{offerA, offerB}, offeredCurrencies(parseAuthenticateValues(t, recorder.Header())))
	assert.Contains(t, recorder.Body.String(), "nope")
}

func TestComposeMiddleware_ExpandsMultiOfferEntries(t *testing.T) {
	t.Parallel()

	intent := &recordingOfferIntent{}
	offers := newTestServer(t, offersTestMethod{currencies: []string{offerA, offerB}, intent: intent}, composeRealm, composeSecret)
	stripe := newTestServer(t, composeTestMethod{name: "stripe"}, composeRealm, composeSecret)
	srv := composeTestServer(t,
		ComposeConfig{Mpp: offers, Params: ChargeParams{Amount: "1"}},
		ComposeConfig{Mpp: stripe, Params: ChargeParams{Amount: "1", Currency: "usd"}},
	)
	defer srv.Close()

	resp := getChallenge(t, srv.URL)
	resp.Body.Close()
	challenges := parseAuthenticateValues(t, resp.Header)
	require.Len(t, challenges, 3)
	assert.Equal(t, []string{"tempo", "tempo", "stripe"}, []string{challenges[0].Method, challenges[1].Method, challenges[2].Method})
	assert.Equal(t, []string{offerA, offerB, "usd"}, offeredCurrencies(challenges))

	request, err := http.NewRequest(http.MethodGet, srv.URL, nil)
	require.NoError(t, err)
	request.Header.Set(mpp.HeaderAuthorization, challenges[1].NewCredential(map[string]any{"type": "test"}).ToAuthorization())
	paid, err := http.DefaultClient.Do(request)
	require.NoError(t, err)
	body, _ := io.ReadAll(paid.Body)
	paid.Body.Close()
	require.Equal(t, http.StatusOK, paid.StatusCode)
	assert.Equal(t, "tempo:0xreceipt-"+offerB, string(body))
	assert.Equal(t, []string{offerB}, intent.currencies())
}

func TestComposeMiddleware_SelectsMultiOfferEntryByOffer(t *testing.T) {
	t.Parallel()

	cheap := &recordingOfferIntent{}
	premium := &recordingOfferIntent{}
	cheapMpp := newTestServer(t, offersTestMethod{currencies: []string{offerA, offerB}, intent: cheap}, composeRealm, composeSecret)
	premiumMpp := newTestServer(t, offersTestMethod{currencies: []string{offerA, offerB}, intent: premium}, composeRealm, composeSecret)
	srv := composeTestServer(t,
		ComposeConfig{Mpp: cheapMpp, Params: ChargeParams{Amount: "1"}},
		ComposeConfig{Mpp: premiumMpp, Params: ChargeParams{Amount: "5"}},
	)
	defer srv.Close()

	resp := getChallenge(t, srv.URL)
	resp.Body.Close()
	challenges := parseAuthenticateValues(t, resp.Header)
	require.Len(t, challenges, 4)

	// The second offer of the second entry must dispatch to that entry, not
	// fall back to the first tempo entry.
	request, err := http.NewRequest(http.MethodGet, srv.URL, nil)
	require.NoError(t, err)
	request.Header.Set(mpp.HeaderAuthorization, challenges[3].NewCredential(map[string]any{"type": "test"}).ToAuthorization())
	paid, err := http.DefaultClient.Do(request)
	require.NoError(t, err)
	paid.Body.Close()
	require.Equal(t, http.StatusOK, paid.StatusCode)
	assert.Empty(t, cheap.currencies())
	assert.Equal(t, []string{offerB}, premium.currencies())
}

func TestWriteChallenges(t *testing.T) {
	t.Parallel()

	first := mpp.NewChallenge("test-secret-key-minimum-32-byte-secret", "api.example.com", "tempo", "charge", map[string]any{"currency": offerA})
	second := mpp.NewChallenge("test-secret-key-minimum-32-byte-secret", "api.example.com", "tempo", "charge", map[string]any{"currency": offerB})

	recorder := httptest.NewRecorder()
	recorder.Header().Set(mpp.HeaderWWWAuthenticate, "stale")
	WriteChallenges(recorder, []*mpp.Challenge{first, second}, "api.example.com")
	assert.Equal(t, http.StatusPaymentRequired, recorder.Code)
	assert.Equal(t, "no-store", recorder.Header().Get("Cache-Control"))
	assert.Equal(t, []string{offerA, offerB}, offeredCurrencies(parseAuthenticateValues(t, recorder.Header())))

	single := httptest.NewRecorder()
	WriteChallenge(single, first, "api.example.com")
	assert.Equal(t, []string{first.ToAuthenticate("api.example.com")}, single.Header().Values(mpp.HeaderWWWAuthenticate))

	empty := httptest.NewRecorder()
	WriteChallenges(empty, nil, "api.example.com")
	assert.Equal(t, http.StatusBadRequest, empty.Code)
	assert.Empty(t, empty.Header().Values(mpp.HeaderWWWAuthenticate))

	withErr := httptest.NewRecorder()
	WritePaymentErrorWithChallenges(withErr, mpp.ErrVerificationFailed("nope"), []*mpp.Challenge{first, second}, "api.example.com")
	assert.Equal(t, http.StatusPaymentRequired, withErr.Code)
	assert.Equal(t, []string{offerA, offerB}, offeredCurrencies(parseAuthenticateValues(t, withErr.Header())))

	noChallenges := httptest.NewRecorder()
	WritePaymentErrorWithChallenges(noChallenges, mpp.ErrVerificationFailed("nope"), nil, "api.example.com")
	assert.Equal(t, http.StatusPaymentRequired, noChallenges.Code)
	assert.Empty(t, noChallenges.Header().Values(mpp.HeaderWWWAuthenticate))
}

func TestWriteChallengesRejectsInvalidChallengeWithoutPartialHeaders(t *testing.T) {
	t.Parallel()

	valid := mpp.NewChallenge("test-secret-key-minimum-32-byte-secret", "api.example.com", "tempo", "charge", map[string]any{"currency": offerA})
	invalid := mpp.NewChallenge("test-secret-key-minimum-32-byte-secret", "api.example.com", "tempo", "charge", map[string]any{"currency": offerB}, mpp.WithDescription("bad\r\nheader"))

	recorder := httptest.NewRecorder()
	WriteChallenges(recorder, []*mpp.Challenge{valid, invalid}, "api.example.com")
	assert.Equal(t, http.StatusBadRequest, recorder.Code)
	assert.Empty(t, recorder.Header().Values(mpp.HeaderWWWAuthenticate))
}

func parseAuthenticateValues(t *testing.T, header http.Header) []*mpp.Challenge {
	t.Helper()
	values := header.Values(mpp.HeaderWWWAuthenticate)
	challenges := make([]*mpp.Challenge, 0, len(values))
	for _, value := range values {
		challenge, err := mpp.ParseChallenge(value)
		require.NoError(t, err)
		challenges = append(challenges, challenge)
	}
	return challenges
}
