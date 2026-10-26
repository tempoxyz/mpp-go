package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tempoxyz/mpp-go/pkg/mpp"
)

func apiTestServer(t *testing.T, opts ...Option) *Mpp {
	t.Helper()
	payment, err := New(composeTestMethod{name: "alpha"}, composeRealm, composeSecret, opts...)
	require.NoError(t, err)
	return payment
}

func paymentFor(challenge *mpp.Challenge) string {
	authorization, err := mpp.FormatCredential(challenge.NewCredential(map[string]any{"type": "hash", "hash": "0xabc"}))
	if err != nil {
		panic(err)
	}
	return authorization
}

func TestChargeRequestChallengesThenVerifies(t *testing.T) {
	t.Parallel()

	payment := apiTestServer(t)
	params := ChargeParams{Amount: "1.00"}
	r := httptest.NewRequest(http.MethodPost, "/premium?x=1", strings.NewReader(`{"q":1}`))
	res, err := payment.ChargeRequest(r, params)
	require.NoError(t, err)
	require.True(t, res.IsChallenge())
	assert.Equal(t, "/premium", res.Challenge.Request["_mppx_scope"].(map[string]string)["resource"])
	assert.NotEmpty(t, res.Challenge.Digest)

	rec := httptest.NewRecorder()
	res.ServeHTTP(rec, r)
	assert.Equal(t, http.StatusPaymentRequired, rec.Code)
	assert.Len(t, rec.Header().Values(mpp.HeaderWWWAuthenticate), 1)

	paid := httptest.NewRequest(http.MethodPost, "/premium?x=1", strings.NewReader(`{"q":1}`))
	paid.Header.Set(mpp.HeaderAuthorization, paymentFor(res.Challenge))
	res, err = payment.ChargeRequest(paid, params)
	require.NoError(t, err)
	require.False(t, res.IsChallenge())
	assert.Equal(t, "0xreceipt-alpha", res.Receipt.Reference)

	rec = httptest.NewRecorder()
	res.ServeHTTP(rec, paid)
	assert.NotEmpty(t, rec.Header().Get(mpp.HeaderPaymentReceipt))
	assert.Equal(t, "private", rec.Header().Get("Cache-Control"))
}

func TestChargeRequestKeepsCallerScopeAndReturnsResultOnError(t *testing.T) {
	t.Parallel()

	payment := apiTestServer(t)
	r := httptest.NewRequest(http.MethodGet, "/premium", nil)
	res, err := payment.ChargeRequest(r, ChargeParams{Amount: "1.00", MppxScope: map[string]string{"route": "custom"}})
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"route": "custom"}, res.Challenge.Request["_mppx_scope"])

	r = httptest.NewRequest(http.MethodGet, "/premium", nil)
	r.Header.Set(mpp.HeaderAuthorization, "Payment a, Payment b")
	res, err = payment.ChargeRequest(r, ChargeParams{Amount: "1.00"})
	require.Error(t, err)
	require.NotNil(t, res)
	rec := httptest.NewRecorder()
	res.ServeHTTP(rec, r)
	assert.Equal(t, http.StatusBadRequest, rec.Code)

	var nilPayment *Mpp
	res, err = nilPayment.ChargeRequest(httptest.NewRequest(http.MethodGet, "/", nil), ChargeParams{})
	require.Error(t, err)
	rec = httptest.NewRecorder()
	res.ServeHTTP(rec, r)
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

func TestChargeResultServeHTTP(t *testing.T) {
	t.Parallel()

	var nilResult *ChargeResult
	assert.False(t, nilResult.IsChallenge())
	rec := httptest.NewRecorder()
	nilResult.ServeHTTP(rec, nil)
	assert.Equal(t, http.StatusInternalServerError, rec.Code)

	payment := apiTestServer(t)
	res, err := payment.Charge(context.Background(), ChargeParams{Amount: "1.00"})
	require.NoError(t, err)
	tampered := res.Challenge.NewCredential(map[string]any{"type": "hash"})
	tampered.Challenge.ID = "forged"
	authorization, err := mpp.FormatCredential(tampered)
	require.NoError(t, err)
	res, err = payment.Charge(context.Background(), ChargeParams{Amount: "1.00", Authorization: authorization})
	require.Error(t, err)
	require.True(t, res.IsChallenge())
	rec = httptest.NewRecorder()
	res.ServeHTTP(rec, nil)
	assert.Equal(t, http.StatusPaymentRequired, rec.Code)
	assert.Equal(t, "application/problem+json", rec.Header().Get("Content-Type"))
	assert.Contains(t, rec.Body.String(), string(mpp.ErrorTypeInvalidChallenge))
	assert.Len(t, rec.Header().Values(mpp.HeaderWWWAuthenticate), 1)
}

func TestVerifyResultIsChallenge(t *testing.T) {
	t.Parallel()

	var nilResult *VerifyResult
	assert.False(t, nilResult.IsChallenge())
	assert.True(t, (&VerifyResult{Challenge: &mpp.Challenge{}}).IsChallenge())
	assert.False(t, (&VerifyResult{Receipt: &mpp.Receipt{}}).IsChallenge())
}

func TestComposeMatchesComposeCharge(t *testing.T) {
	t.Parallel()

	alpha := apiTestServer(t)
	beta, err := New(composeTestMethod{name: "beta"}, composeRealm, composeSecret)
	require.NoError(t, err)
	offers := []Offer{{Mpp: alpha, Params: ChargeParams{Amount: "1.00"}}, {Mpp: beta, Params: ChargeParams{Amount: "2.00"}}}

	offered, err := Compose(context.Background(), "", offers...)
	require.NoError(t, err)
	require.Len(t, offered.Challenges, 2)

	paid, err := Compose(context.Background(), paymentFor(offered.Challenges[1]), offers...)
	require.NoError(t, err)
	assert.Equal(t, "0xreceipt-beta", paid.Receipt.Reference)

	legacy, err := ComposeCharge(context.Background(), paymentFor(offered.Challenges[1]), offers...)
	require.NoError(t, err)
	assert.Equal(t, paid.Receipt.Reference, legacy.Receipt.Reference)

	unknown, err := mpp.NewChallengeWithError(composeSecret, composeRealm, "gamma", "charge", map[string]any{"amount": "1.00"})
	require.NoError(t, err)
	res, err := Compose(context.Background(), paymentFor(unknown), offers...)
	assert.Nil(t, res)
	assert.ErrorIs(t, err, mpp.ErrorTypeMethodUnsupported)
}

func TestComposeRequest(t *testing.T) {
	t.Parallel()

	alpha := apiTestServer(t)
	offers := []Offer{{Mpp: alpha, Params: ChargeParams{Amount: "1.00"}}, {Mpp: alpha, Params: ChargeParams{Amount: "2.00"}}}
	res, err := ComposeRequest(httptest.NewRequest(http.MethodGet, "/premium", nil), offers...)
	require.NoError(t, err)
	require.Len(t, res.Challenges, 2)

	r := httptest.NewRequest(http.MethodGet, "/premium", nil)
	r.Header.Set(mpp.HeaderAuthorization, paymentFor(res.Challenges[1]))
	res, err = ComposeRequest(r, offers...)
	require.NoError(t, err)
	echoed, err := mpp.B64Decode(res.Credential.Challenge.Request)
	require.NoError(t, err)
	assert.Equal(t, "2.00", echoed["amount"])
}

func TestWithChallengeNonce(t *testing.T) {
	t.Parallel()

	payment := apiTestServer(t, WithChallengeNonce(true))
	params := ChargeParams{Amount: "1.00", Expires: mpp.ExpiresIn(5 * time.Minute)}
	first, err := payment.Charge(context.Background(), params)
	require.NoError(t, err)
	second, err := payment.Charge(context.Background(), params)
	require.NoError(t, err)
	assert.NotEmpty(t, first.Challenge.Opaque["_mpp_nonce"])
	assert.NotEqual(t, first.Challenge.ID, second.Challenge.ID)

	params.Authorization = paymentFor(first.Challenge)
	paid, err := payment.Charge(context.Background(), params)
	require.NoError(t, err)
	assert.NotNil(t, paid.Receipt)

	// Verification accepts a nonce-bearing Challenge whatever the default.
	plain := apiTestServer(t)
	paid, err = plain.Charge(context.Background(), params)
	require.NoError(t, err)
	assert.NotNil(t, paid.Receipt)
	issued, err := plain.Charge(context.Background(), ChargeParams{Amount: "1.00"})
	require.NoError(t, err)
	assert.Empty(t, issued.Challenge.Opaque["_mpp_nonce"])
}

func TestWithEvents(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	var created, succeeded int
	var failed []error
	var failedCredentials []*mpp.Credential
	payment := apiTestServer(t, WithEvents(Events{
		ChallengeCreated: func(context.Context, *mpp.Challenge) { mu.Lock(); created++; mu.Unlock() },
		PaymentSucceeded: func(_ context.Context, credential *mpp.Credential, receipt *mpp.Receipt) {
			mu.Lock()
			defer mu.Unlock()
			if credential != nil && receipt != nil {
				succeeded++
			}
		},
		PaymentFailed: func(_ context.Context, credential *mpp.Credential, err error) {
			mu.Lock()
			defer mu.Unlock()
			failed = append(failed, err)
			failedCredentials = append(failedCredentials, credential)
		},
	}))
	handler := ChargeMiddleware(payment, ChargeParams{Amount: "1.00"})(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/premium", nil))
	challenge, err := mpp.ParseChallenge(rec.Header().Get(mpp.HeaderWWWAuthenticate))
	require.NoError(t, err)

	r := httptest.NewRequest(http.MethodGet, "/premium", nil)
	r.Header.Set(mpp.HeaderAuthorization, paymentFor(challenge))
	handler.ServeHTTP(httptest.NewRecorder(), r)

	r = httptest.NewRequest(http.MethodGet, "/premium", nil)
	r.Header.Set(mpp.HeaderAuthorization, "Payment not-base64!")
	handler.ServeHTTP(httptest.NewRecorder(), r)

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, 2, created, "initial and malformed-credential retry challenges")
	assert.Equal(t, 1, succeeded)
	require.Len(t, failed, 1)
	assert.ErrorIs(t, failed[0], mpp.ErrorTypeMalformedCredential)
	assert.Nil(t, failedCredentials[0])
}

func TestUnreadableCredentialHeaderReportsPaymentFailed(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	var failed []error
	payment := apiTestServer(t, WithEvents(Events{
		PaymentFailed: func(_ context.Context, credential *mpp.Credential, err error) {
			assert.Nil(t, credential)
			mu.Lock()
			defer mu.Unlock()
			failed = append(failed, err)
		},
	}))
	issued, err := payment.Charge(context.Background(), ChargeParams{Amount: "1.00"})
	require.NoError(t, err)
	header := paymentFor(issued.Challenge) + ", " + paymentFor(issued.Challenge)

	// A direct Charge and the HTTP flow report the same unreadable header.
	_, directErr := payment.Charge(context.Background(), ChargeParams{Amount: "1.00", Authorization: header})
	require.Error(t, directErr)

	r := httptest.NewRequest(http.MethodGet, "/premium", nil)
	r.Header.Set(mpp.HeaderAuthorization, header)
	_, requestErr := payment.ChargeRequest(r, ChargeParams{Amount: "1.00"})
	require.Error(t, requestErr)

	handler := ChargeMiddleware(payment, ChargeParams{Amount: "1.00"})(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, r)
	assert.Equal(t, http.StatusBadRequest, rec.Code)

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, failed, 3, "one PaymentFailed per call")
	assert.Equal(t, directErr.Error(), failed[0].Error())
	assert.Equal(t, requestErr, failed[1])
	assert.Equal(t, requestErr.Error(), failed[2].Error())
}

type nilPointerMethod struct{}

func (*nilPointerMethod) Name() string               { return "nil" }
func (*nilPointerMethod) Intents() map[string]Intent { return nil }

func TestNewRejectsTypedNilMethod(t *testing.T) {
	t.Parallel()

	var typedNil *nilPointerMethod
	_, err := New(typedNil, composeRealm, composeSecret)
	assert.EqualError(t, err, "server: payment method is required")
}

func TestNewDefaultsSecretButNotRealmFromEnvironment(t *testing.T) {
	t.Setenv("MPP_REALM", "env.example.com")
	t.Setenv("MPP_SECRET_KEY", composeSecret)

	// An empty realm stays empty, so replicas upgraded during a rolling
	// deploy keep accepting each other's Challenges.
	payment, err := New(composeTestMethod{name: "alpha"}, "", "")
	require.NoError(t, err)
	assert.Equal(t, "", payment.Realm())
	legacy, err := New(composeTestMethod{name: "alpha"}, "", composeSecret)
	require.NoError(t, err)
	assert.Equal(t, "", legacy.Realm())
	issued, err := legacy.Charge(context.Background(), ChargeParams{Amount: "1.00"})
	require.NoError(t, err)
	paid, err := payment.Charge(context.Background(), ChargeParams{Amount: "1.00", Authorization: paymentFor(issued.Challenge)})
	require.NoError(t, err)
	assert.NotNil(t, paid.Receipt)

	detected, err := New(composeTestMethod{name: "alpha"}, DetectRealm(), "")
	require.NoError(t, err)
	assert.Equal(t, "env.example.com", detected.Realm())
	res, err := payment.Charge(context.Background(), ChargeParams{Amount: "1.00"})
	require.NoError(t, err)
	assert.True(t, res.Challenge.VerifyID(composeSecret))

	t.Setenv("MPP_SECRET_KEY", "short")
	_, err = New(composeTestMethod{name: "alpha"}, "", "")
	assert.EqualError(t, err, "server: secret key must be at least 32 bytes")
}
