package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tempoxyz/mpp-go/pkg/mpp"
)

// --- test helpers ---

type composeTestMethod struct {
	name string
}

func (m composeTestMethod) Name() string { return m.name }

func (m composeTestMethod) Intents() map[string]Intent {
	return map[string]Intent{"charge": composeTestIntent{method: m.name}}
}

type composeTestIntent struct {
	method string
}

func (i composeTestIntent) Name() string { return "charge" }

func (i composeTestIntent) Verify(_ context.Context, _ *mpp.Credential, _ map[string]any) (*mpp.Receipt, error) {
	return mpp.Success("0xreceipt-"+i.method, mpp.WithReceiptMethod(i.method)), nil
}

const (
	composeRealm  = "api.example.com"
	composeSecret = "secret-key"
)

func composeTestServer(t *testing.T, configs ...ComposeConfig) *httptest.Server {
	t.Helper()
	handler := ComposeMiddleware(configs...)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cred := CredentialFromContext(r.Context())
		receipt := ReceiptFromContext(r.Context())
		io.WriteString(w, cred.Challenge.Method+":"+receipt.Reference)
	}))
	return httptest.NewServer(handler)
}

// getChallenge does a bare GET and returns the 402 response.
func getChallenge(t *testing.T, url string) *http.Response {
	t.Helper()
	resp, err := http.Get(url)
	if !assert.NoErrorf(t, err,
		"http.Get() error = %v", err) {
		return *new(*http.Response)
	}

	if resp.StatusCode != http.StatusPaymentRequired {
		resp.Body.Close()
		assert.Failf(t, "", "status = %d, want %d", resp.StatusCode, http.StatusPaymentRequired)
		return *new(*http.Response)

	}
	return resp
}

// findChallenge parses WWW-Authenticate headers and returns the one matching methodName.
func findChallenge(t *testing.T, resp *http.Response, methodName string) *mpp.Challenge {
	t.Helper()
	for _, h := range resp.Header.Values("WWW-Authenticate") {
		c, err := mpp.ParseChallenge(h)
		if !assert.NoErrorf(t, err,
			"ParseChallenge() error = %v", err) {
			return *new(*mpp.Challenge)
		}

		if c.Method == methodName {
			return c
		}
	}
	assert.Failf(t, "", "did not find challenge for method %q", methodName)
	return *new(*mpp.Challenge)
}

// payWith sends a credential and returns the response.
func payWith(t *testing.T, url string, challenge *mpp.Challenge) *http.Response {
	t.Helper()
	credential := &mpp.Credential{
		Challenge: challenge.ToEcho(),
		Source:    "did:key:z6Mktest",
		Payload:   map[string]any{"type": "hash", "hash": "0xabc"},
	}
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if !assert.NoErrorf(t, err,
		"http.NewRequest() error = %v", err) {
		return *new(*http.Response)
	}

	req.Header.Set("Authorization", credential.ToAuthorization())
	resp, err := http.DefaultClient.Do(req)
	if !assert.NoErrorf(t, err,
		"Do() error = %v", err) {
		return *new(*http.Response)
	}

	return resp
}

// --- tests ---

func TestComposeMiddleware_FansOutChallenges(t *testing.T) {
	methodA := New(composeTestMethod{name: "alpha"}, composeRealm, composeSecret)
	methodB := New(composeTestMethod{name: "beta"}, composeRealm, composeSecret)

	srv := composeTestServer(t,
		ComposeConfig{Mpp: methodA, Params: ChargeParams{Amount: "1.00"}},
		ComposeConfig{Mpp: methodB, Params: ChargeParams{Amount: "2.00"}},
	)
	defer srv.Close()

	resp := getChallenge(t, srv.URL)
	defer resp.Body.Close()

	wwwAuth := resp.Header.Values("WWW-Authenticate")
	if !assert.Lenf(t, wwwAuth, 2,
		"got %d WWW-Authenticate headers, want 2", len(wwwAuth)) {
		return
	}

	challenge0, _ := mpp.ParseChallenge(wwwAuth[0])
	challenge1, _ := mpp.ParseChallenge(wwwAuth[1])
	if !assert.NotEqualf(t, challenge1.Method, challenge0.Method,
		"expected different methods, both are %q", challenge0.Method) {
		return
	}

}

func TestComposeMiddleware_DispatchesToCorrectMethod(t *testing.T) {
	methodA := New(composeTestMethod{name: "alpha"}, composeRealm, composeSecret)
	methodB := New(composeTestMethod{name: "beta"}, composeRealm, composeSecret)

	srv := composeTestServer(t,
		ComposeConfig{Mpp: methodA, Params: ChargeParams{Amount: "1.00"}},
		ComposeConfig{Mpp: methodB, Params: ChargeParams{Amount: "2.00"}},
	)
	defer srv.Close()

	resp := getChallenge(t, srv.URL)
	betaChallenge := findChallenge(t, resp, "beta")
	resp.Body.Close()

	paid := payWith(t, srv.URL, betaChallenge)
	defer paid.Body.Close()

	if paid.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(paid.Body)
		assert.Failf(t, "", "paid status = %d, want 200; body = %s", paid.StatusCode, body)
		return
	}

	body, _ := io.ReadAll(paid.Body)
	if got := string(body); got != "beta:0xreceipt-beta" {
		assert.Failf(t, "", "body = %q, want %q", got, "beta:0xreceipt-beta")
		return
	}

	receipt, err := mpp.ParsePaymentReceipt(paid.Header.Get("Payment-Receipt"))
	if !assert.NoErrorf(t, err,
		"ParsePaymentReceipt() error = %v", err) {
		return
	}
	if !assert.Equalf(t, "0xreceipt-beta", receipt.Reference,
		"receipt reference = %q, want %q", receipt.Reference, "0xreceipt-beta") {
		return
	}

}

func TestComposeMiddleware_SameMethodDifferentAmounts(t *testing.T) {
	// Two entries with the same method+intent but different amounts.
	// The credential should dispatch to the correct entry based on request matching.
	methodCheap := New(composeTestMethod{name: "tempo"}, composeRealm, composeSecret)
	methodExpensive := New(composeTestMethod{name: "tempo"}, composeRealm, composeSecret)

	srv := composeTestServer(t,
		ComposeConfig{Mpp: methodCheap, Params: ChargeParams{Amount: "0.01"}},
		ComposeConfig{Mpp: methodExpensive, Params: ChargeParams{Amount: "10.00"}},
	)
	defer srv.Close()

	resp := getChallenge(t, srv.URL)
	defer resp.Body.Close()

	wwwAuth := resp.Header.Values("WWW-Authenticate")
	if !assert.Lenf(t, wwwAuth, 2,
		"got %d WWW-Authenticate headers, want 2", len(wwwAuth)) {
		return

		// Pick the second (expensive) challenge by parsing and checking the request amount.
	}

	var expensiveChallenge *mpp.Challenge
	for _, h := range wwwAuth {
		c, err := mpp.ParseChallenge(h)
		if !assert.NoErrorf(t, err,
			"ParseChallenge() error = %v", err) {
			return
		}

		if amt, ok := c.Request["amount"]; ok && amt == "10.00" {
			expensiveChallenge = c
			break
		}
	}
	if !assert.NotNil(t, expensiveChallenge,
		"did not find the 10.00 amount challenge") {
		return
	}

	paid := payWith(t, srv.URL, expensiveChallenge)
	defer paid.Body.Close()

	if paid.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(paid.Body)
		assert.Failf(t, "", "paid status = %d, want 200; body = %s", paid.StatusCode, body)
		return
	}
}

func TestComposeMiddleware_SameRequestDifferentMetaSelectsMatchingConfig(t *testing.T) {
	methodBasic := New(composeTestMethod{name: "tempo"}, composeRealm, composeSecret)
	methodPro := New(composeTestMethod{name: "tempo"}, composeRealm, composeSecret)

	srv := composeTestServer(t,
		ComposeConfig{Mpp: methodBasic, Params: ChargeParams{Amount: "1.00", Meta: map[string]string{"plan": "basic"}}},
		ComposeConfig{Mpp: methodPro, Params: ChargeParams{Amount: "1.00", Meta: map[string]string{"plan": "pro"}}},
	)
	defer srv.Close()

	resp := getChallenge(t, srv.URL)
	defer resp.Body.Close()

	var proChallenge *mpp.Challenge
	for _, h := range resp.Header.Values("WWW-Authenticate") {
		c, err := mpp.ParseChallenge(h)
		if !assert.NoErrorf(t, err,
			"ParseChallenge() error = %v", err) {
			return
		}

		if c.Opaque["plan"] == "pro" {
			proChallenge = c
			break
		}
	}
	if !assert.NotNil(t, proChallenge,
		"did not find the pro challenge") {
		return
	}

	paid := payWith(t, srv.URL, proChallenge)
	defer paid.Body.Close()

	if paid.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(paid.Body)
		assert.Failf(t, "", "paid status = %d, want 200; body = %s", paid.StatusCode, body)
		return
	}
}

func TestComposeMiddleware_RejectsUnknownMethod(t *testing.T) {
	methodA := New(composeTestMethod{name: "alpha"}, composeRealm, composeSecret)

	srv := composeTestServer(t,
		ComposeConfig{Mpp: methodA, Params: ChargeParams{Amount: "1.00"}},
	)
	defer srv.Close()

	fakeChallenge := mpp.NewChallenge(composeSecret, composeRealm, "unknown", "charge", map[string]any{"amount": "1.00"})
	resp := payWith(t, srv.URL, fakeChallenge)
	defer resp.Body.Close()
	if !assert.Equalf(t, http.StatusBadRequest, resp.StatusCode,
		"status = %d, want %d", resp.StatusCode, http.StatusBadRequest) {
		return
	}

}

func TestComposeMiddleware_CrossMethodCredentialRejected(t *testing.T) {
	methodA := New(composeTestMethod{name: "alpha"}, composeRealm, composeSecret)
	methodB := New(composeTestMethod{name: "beta"}, composeRealm, composeSecret)

	srv := composeTestServer(t,
		ComposeConfig{Mpp: methodA, Params: ChargeParams{Amount: "1.00"}},
		ComposeConfig{Mpp: methodB, Params: ChargeParams{Amount: "2.00"}},
	)
	defer srv.Close()

	resp := getChallenge(t, srv.URL)
	alphaChallenge := findChallenge(t, resp, "alpha")
	resp.Body.Close()

	// Tamper: change the method name in the echo to "beta" but keep alpha's HMAC.
	echo := alphaChallenge.ToEcho()
	echo.Method = "beta"
	credential := &mpp.Credential{
		Challenge: echo,
		Payload:   map[string]any{"type": "hash", "hash": "0xabc"},
	}

	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	req.Header.Set("Authorization", credential.ToAuthorization())

	paid, err := http.DefaultClient.Do(req)
	if !assert.NoErrorf(t, err,
		"Do() error = %v", err) {
		return
	}

	defer paid.Body.Close()
	if !assert.NotEqual(t, http.StatusOK, paid.StatusCode,
		"expected credential to be rejected, but got 200") {
		return
	}

}

func TestComposeMiddleware_AcceptsPaymentFromMixedAuthorizationHeader(t *testing.T) {
	methodA := New(composeTestMethod{name: "alpha"}, composeRealm, composeSecret)
	methodB := New(composeTestMethod{name: "beta"}, composeRealm, composeSecret)

	srv := composeTestServer(t,
		ComposeConfig{Mpp: methodA, Params: ChargeParams{Amount: "1.00"}},
		ComposeConfig{Mpp: methodB, Params: ChargeParams{Amount: "2.00"}},
	)
	defer srv.Close()

	resp := getChallenge(t, srv.URL)
	betaChallenge := findChallenge(t, resp, "beta")
	resp.Body.Close()

	credential := &mpp.Credential{
		Challenge: betaChallenge.ToEcho(),
		Source:    "did:key:z6Mktest",
		Payload:   map[string]any{"type": "hash", "hash": "0xabc"},
	}
	req, err := http.NewRequest(http.MethodGet, srv.URL, nil)
	if !assert.NoErrorf(t, err,
		"http.NewRequest() error = %v", err) {
		return
	}

	req.Header.Set("Authorization", "Bearer test-token, "+credential.ToAuthorization())

	paid, err := http.DefaultClient.Do(req)
	if !assert.NoErrorf(t, err,
		"Do() error = %v", err) {
		return
	}

	defer paid.Body.Close()

	if paid.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(paid.Body)
		assert.Failf(t, "", "paid status = %d, want 200; body = %s", paid.StatusCode, body)
		return
	}
}

func TestComposeMiddleware_ReturnsMalformedCredentialForInvalidEchoedRequest(t *testing.T) {
	methodA := New(composeTestMethod{name: "alpha"}, composeRealm, composeSecret)

	srv := composeTestServer(t,
		ComposeConfig{Mpp: methodA, Params: ChargeParams{Amount: "1.00"}},
	)
	defer srv.Close()

	resp := getChallenge(t, srv.URL)
	challenge := findChallenge(t, resp, "alpha")
	resp.Body.Close()

	credential := &mpp.Credential{
		Challenge: challenge.ToEcho(),
		Payload:   map[string]any{"type": "hash", "hash": "0xabc"},
	}
	credential.Challenge.Request = "%%%"

	req, err := http.NewRequest(http.MethodGet, srv.URL, nil)
	if !assert.NoErrorf(t, err,
		"http.NewRequest() error = %v", err) {
		return
	}

	req.Header.Set("Authorization", credential.ToAuthorization())

	paid, err := http.DefaultClient.Do(req)
	if !assert.NoErrorf(t, err,
		"Do() error = %v", err) {
		return
	}

	defer paid.Body.Close()

	if paid.StatusCode != http.StatusBadRequest {
		body, _ := io.ReadAll(paid.Body)
		assert.Failf(t, "", "status = %d, want %d; body = %s", paid.StatusCode, http.StatusBadRequest, body)
		return
	}

	var problem struct {
		Type string `json:"type"`
	}
	if err := json.NewDecoder(paid.Body).Decode(&problem); err != nil {
		assert.Failf(t, "", "Decode(problem) error = %v", err)
		return
	}
	if !assert.Equalf(t, string(mpp.ErrorTypeMalformedCredential), problem.Type,
		"problem type = %q, want %q", problem.Type, mpp.ErrorTypeMalformedCredential) {
		return
	}

}

func TestComposeMiddleware_SingleMethod(t *testing.T) {
	methodA := New(composeTestMethod{name: "alpha"}, composeRealm, composeSecret)

	srv := composeTestServer(t,
		ComposeConfig{Mpp: methodA, Params: ChargeParams{Amount: "1.00"}},
	)
	defer srv.Close()

	resp := getChallenge(t, srv.URL)
	defer resp.Body.Close()

	wwwAuth := resp.Header.Values("WWW-Authenticate")
	if !assert.Lenf(t, wwwAuth, 1,
		"got %d WWW-Authenticate headers, want 1", len(wwwAuth)) {
		return
	}

	challenge, _ := mpp.ParseChallenge(wwwAuth[0])
	paid := payWith(t, srv.URL, challenge)
	defer paid.Body.Close()

	if paid.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(paid.Body)
		assert.Failf(t, "", "status = %d, want 200; body = %s", paid.StatusCode, body)
		return
	}

	body, _ := io.ReadAll(paid.Body)
	if got := string(body); got != "alpha:0xreceipt-alpha" {
		assert.Failf(t, "", "body = %q, want %q", got, "alpha:0xreceipt-alpha")
		return
	}
}

func TestComposeMiddlewareRejectsCRLFChallengeDescription(t *testing.T) {
	methodA := New(composeTestMethod{name: "alpha"}, composeRealm, composeSecret)

	srv := composeTestServer(t,
		ComposeConfig{
			Mpp: methodA,
			Params: ChargeParams{
				Amount:      "1.00",
				Description: "Line one\r\nLine two",
			},
		},
	)
	defer srv.Close()

	resp, err := http.Get(srv.URL)
	if !assert.NoErrorf(t, err,
		"http.Get() error = %v", err) {
		return
	}

	defer resp.Body.Close()

	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	assert.Empty(t, resp.Header.Values("WWW-Authenticate"))

	var problem struct {
		Type string `json:"type"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&problem))
	assert.Equal(t, string(mpp.ErrorTypeInvalidChallenge), problem.Type)
}

func TestComposeMiddleware_PanicsOnEmptyConfigs(t *testing.T) {
	defer func() {
		{
			r := recover()
			if !assert.NotNil(t, r,
				"expected panic for empty configs") {
				return
			}
		}

	}()
	ComposeMiddleware()
}

func TestComposeMiddleware_PanicsOnNilMpp(t *testing.T) {
	defer func() {
		{
			r := recover()
			if !assert.NotNil(t, r,
				"expected panic for nil Mpp") {
				return
			}
		}

	}()
	ComposeMiddleware(ComposeConfig{Mpp: nil, Params: ChargeParams{Amount: "1.00"}})
}

func TestComposeMiddleware_PanicsOnMixedRealms(t *testing.T) {
	a := New(composeTestMethod{name: "alpha"}, "realm-a.example.com", composeSecret)
	b := New(composeTestMethod{name: "beta"}, "realm-b.example.com", composeSecret)

	defer func() {
		{
			r := recover()
			if !assert.NotNil(t, r,
				"expected panic for mixed realms") {
				return
			}
		}

	}()
	ComposeMiddleware(
		ComposeConfig{Mpp: a, Params: ChargeParams{Amount: "1.00"}},
		ComposeConfig{Mpp: b, Params: ChargeParams{Amount: "2.00"}},
	)
}
