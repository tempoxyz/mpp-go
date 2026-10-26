package server

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tempoxyz/mpp-go/pkg/mpp"
)

// authRecordingMethod records the Authorization value its request builder
// sees on each build.
type authRecordingMethod struct {
	composeTestMethod
	mu   *sync.Mutex
	seen *[]string
}

func newAuthRecordingMethod() authRecordingMethod {
	return authRecordingMethod{composeTestMethod: composeTestMethod{name: "alpha"}, mu: &sync.Mutex{}, seen: &[]string{}}
}

func (m authRecordingMethod) BuildChargeRequest(params ChargeParams) (map[string]any, error) {
	m.mu.Lock()
	*m.seen = append(*m.seen, params.Authorization)
	m.mu.Unlock()
	return map[string]any{"amount": params.Amount, "currency": "usd"}, nil
}

func (m authRecordingMethod) take() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	seen := *m.seen
	*m.seen = nil
	return seen
}

func alphaChallenge(t *testing.T, rec *httptest.ResponseRecorder) *mpp.Challenge {
	t.Helper()
	require.Equal(t, http.StatusPaymentRequired, rec.Code)
	for _, value := range rec.Header().Values(mpp.HeaderWWWAuthenticate) {
		challenge, err := mpp.ParseChallenge(value)
		require.NoError(t, err)
		if challenge.Method == "alpha" {
			return challenge
		}
	}
	t.Fatal("no alpha challenge")
	return nil
}

func TestRequestBuildersSeeApplicationAuthorizationOnRequiresAuthRoute(t *testing.T) {
	t.Parallel()

	const bearer = "Bearer api-key-123"
	for _, name := range []string{"ChargeMiddleware", "ComposeMiddleware"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			method := newAuthRecordingMethod()
			payment, err := New(method, composeRealm, composeSecret, WithRequiresAuth(true))
			require.NoError(t, err)
			resource := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
			var handler http.Handler
			if name == "ChargeMiddleware" {
				handler = ChargeMiddleware(payment, ChargeParams{Amount: "1.00"})(resource)
			} else {
				beta, err := New(composeTestMethod{name: "beta"}, composeRealm, composeSecret)
				require.NoError(t, err)
				handler = ComposeMiddleware(
					ComposeConfig{Mpp: beta, Params: ChargeParams{Amount: "2.00"}},
					ComposeConfig{Mpp: payment, Params: ChargeParams{Amount: "1.00"}},
				)(resource)
			}
			method.take() // builds at construction

			unpaidReq := httptest.NewRequest(http.MethodGet, "/premium", nil)
			unpaidReq.Header.Set(mpp.HeaderAuthorization, bearer)
			unpaid := httptest.NewRecorder()
			handler.ServeHTTP(unpaid, unpaidReq)
			challenge := alphaChallenge(t, unpaid)
			seen := method.take()
			require.NotEmpty(t, seen)
			for _, authorization := range seen {
				assert.Equal(t, bearer, authorization)
			}

			paidReq := httptest.NewRequest(http.MethodGet, "/premium", nil)
			paidReq.Header.Set(mpp.HeaderAuthorization, bearer)
			paidReq.Header.Set(mpp.HeaderPaymentAuthorization, paymentFor(challenge))
			paid := httptest.NewRecorder()
			handler.ServeHTTP(paid, paidReq)
			assert.Equal(t, http.StatusOK, paid.Code, paid.Body.String())
			assert.Equal(t, "ok", paid.Body.String())
			assert.NotEmpty(t, paid.Header().Get(mpp.HeaderPaymentReceipt))
			seen = method.take()
			require.NotEmpty(t, seen)
			for _, authorization := range seen {
				assert.Equal(t, bearer, authorization)
			}
		})
	}
}

func TestRequestBuildersSeePaymentCredentialInAuthorization(t *testing.T) {
	t.Parallel()

	method := newAuthRecordingMethod()
	payment, err := New(method, composeRealm, composeSecret)
	require.NoError(t, err)
	handler := ChargeMiddleware(payment, ChargeParams{Amount: "1.00"})(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) }))

	unpaid := httptest.NewRecorder()
	handler.ServeHTTP(unpaid, httptest.NewRequest(http.MethodGet, "/premium", nil))
	challenge := alphaChallenge(t, unpaid)
	assert.Equal(t, []string{""}, method.take())

	credential := paymentFor(challenge)
	r := httptest.NewRequest(http.MethodGet, "/premium", nil)
	r.Header.Set(mpp.HeaderAuthorization, credential)
	paid := httptest.NewRecorder()
	handler.ServeHTTP(paid, r)
	assert.Equal(t, http.StatusOK, paid.Code, paid.Body.String())
	seen := method.take()
	require.NotEmpty(t, seen)
	for _, authorization := range seen {
		assert.Equal(t, credential, authorization)
	}
}
