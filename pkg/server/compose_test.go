package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

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
	if err != nil {
		t.Fatalf("http.Get() error = %v", err)
	}
	if resp.StatusCode != http.StatusPaymentRequired {
		resp.Body.Close()
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusPaymentRequired)
	}
	return resp
}

// findChallenge parses WWW-Authenticate headers and returns the one matching methodName.
func findChallenge(t *testing.T, resp *http.Response, methodName string) *mpp.Challenge {
	t.Helper()
	for _, h := range resp.Header.Values("WWW-Authenticate") {
		c, err := mpp.ParseChallenge(h)
		if err != nil {
			t.Fatalf("ParseChallenge() error = %v", err)
		}
		if c.Method == methodName {
			return c
		}
	}
	t.Fatalf("did not find challenge for method %q", methodName)
	return nil
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
	if err != nil {
		t.Fatalf("http.NewRequest() error = %v", err)
	}
	req.Header.Set("Authorization", credential.ToAuthorization())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do() error = %v", err)
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
	if len(wwwAuth) != 2 {
		t.Fatalf("got %d WWW-Authenticate headers, want 2", len(wwwAuth))
	}

	challenge0, _ := mpp.ParseChallenge(wwwAuth[0])
	challenge1, _ := mpp.ParseChallenge(wwwAuth[1])
	if challenge0.Method == challenge1.Method {
		t.Fatalf("expected different methods, both are %q", challenge0.Method)
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
		t.Fatalf("paid status = %d, want 200; body = %s", paid.StatusCode, body)
	}

	body, _ := io.ReadAll(paid.Body)
	if got := string(body); got != "beta:0xreceipt-beta" {
		t.Fatalf("body = %q, want %q", got, "beta:0xreceipt-beta")
	}

	receipt, err := mpp.ParsePaymentReceipt(paid.Header.Get("Payment-Receipt"))
	if err != nil {
		t.Fatalf("ParsePaymentReceipt() error = %v", err)
	}
	if receipt.Reference != "0xreceipt-beta" {
		t.Fatalf("receipt reference = %q, want %q", receipt.Reference, "0xreceipt-beta")
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
	if len(wwwAuth) != 2 {
		t.Fatalf("got %d WWW-Authenticate headers, want 2", len(wwwAuth))
	}

	// Pick the second (expensive) challenge by parsing and checking the request amount.
	var expensiveChallenge *mpp.Challenge
	for _, h := range wwwAuth {
		c, err := mpp.ParseChallenge(h)
		if err != nil {
			t.Fatalf("ParseChallenge() error = %v", err)
		}
		if amt, ok := c.Request["amount"]; ok && amt == "10.00" {
			expensiveChallenge = c
			break
		}
	}
	if expensiveChallenge == nil {
		t.Fatal("did not find the 10.00 amount challenge")
	}

	paid := payWith(t, srv.URL, expensiveChallenge)
	defer paid.Body.Close()

	if paid.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(paid.Body)
		t.Fatalf("paid status = %d, want 200; body = %s", paid.StatusCode, body)
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
		if err != nil {
			t.Fatalf("ParseChallenge() error = %v", err)
		}
		if c.Opaque["plan"] == "pro" {
			proChallenge = c
			break
		}
	}
	if proChallenge == nil {
		t.Fatal("did not find the pro challenge")
	}

	paid := payWith(t, srv.URL, proChallenge)
	defer paid.Body.Close()

	if paid.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(paid.Body)
		t.Fatalf("paid status = %d, want 200; body = %s", paid.StatusCode, body)
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

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusBadRequest)
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
	if err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	defer paid.Body.Close()

	if paid.StatusCode == http.StatusOK {
		t.Fatal("expected credential to be rejected, but got 200")
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
	if err != nil {
		t.Fatalf("http.NewRequest() error = %v", err)
	}
	req.Header.Set("Authorization", "Bearer test-token, "+credential.ToAuthorization())

	paid, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	defer paid.Body.Close()

	if paid.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(paid.Body)
		t.Fatalf("paid status = %d, want 200; body = %s", paid.StatusCode, body)
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
	if err != nil {
		t.Fatalf("http.NewRequest() error = %v", err)
	}
	req.Header.Set("Authorization", credential.ToAuthorization())

	paid, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	defer paid.Body.Close()

	if paid.StatusCode != http.StatusBadRequest {
		body, _ := io.ReadAll(paid.Body)
		t.Fatalf("status = %d, want %d; body = %s", paid.StatusCode, http.StatusBadRequest, body)
	}

	var problem struct {
		Type string `json:"type"`
	}
	if err := json.NewDecoder(paid.Body).Decode(&problem); err != nil {
		t.Fatalf("Decode(problem) error = %v", err)
	}
	if problem.Type != string(mpp.ErrorTypeMalformedCredential) {
		t.Fatalf("problem type = %q, want %q", problem.Type, mpp.ErrorTypeMalformedCredential)
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
	if len(wwwAuth) != 1 {
		t.Fatalf("got %d WWW-Authenticate headers, want 1", len(wwwAuth))
	}

	challenge, _ := mpp.ParseChallenge(wwwAuth[0])
	paid := payWith(t, srv.URL, challenge)
	defer paid.Body.Close()

	if paid.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(paid.Body)
		t.Fatalf("status = %d, want 200; body = %s", paid.StatusCode, body)
	}

	body, _ := io.ReadAll(paid.Body)
	if got := string(body); got != "alpha:0xreceipt-alpha" {
		t.Fatalf("body = %q, want %q", got, "alpha:0xreceipt-alpha")
	}
}

func TestComposeMiddleware_PanicsOnEmptyConfigs(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic for empty configs")
		}
	}()
	ComposeMiddleware()
}

func TestComposeMiddleware_PanicsOnNilMpp(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic for nil Mpp")
		}
	}()
	ComposeMiddleware(ComposeConfig{Mpp: nil, Params: ChargeParams{Amount: "1.00"}})
}

func TestComposeMiddleware_PanicsOnMixedRealms(t *testing.T) {
	a := New(composeTestMethod{name: "alpha"}, "realm-a.example.com", composeSecret)
	b := New(composeTestMethod{name: "beta"}, "realm-b.example.com", composeSecret)

	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic for mixed realms")
		}
	}()
	ComposeMiddleware(
		ComposeConfig{Mpp: a, Params: ChargeParams{Amount: "1.00"}},
		ComposeConfig{Mpp: b, Params: ChargeParams{Amount: "2.00"}},
	)
}
