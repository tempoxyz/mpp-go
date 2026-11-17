package chargeclient

import (
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tempoxyz/mpp-go/pkg/client"
	"github.com/tempoxyz/mpp-go/pkg/internal/mpptest"
	"github.com/tempoxyz/mpp-go/pkg/mpp"
	"github.com/tempoxyz/mpp-go/pkg/tempo"
)

// A server offers a zero-amount proof Challenge before the real charge. A
// payer without prepaid credit gets a credit_insufficient 402 answering the
// proof that offers only the real charge under a new ID. The Tempo method,
// driven by the HTTP client, signs the proof, then pays the new charge
// within its PaymentPolicy, as mppx's client Fetch retry rounds do.

const creditInsufficient = `{"type":"https://paymentauth.org/problems/credit-insufficient","title":"Credit insufficient","status":402,"detail":"credit_insufficient"}`

// creditRoundServer is an in-process server for https://testRealm. It offers
// proof and charge; with credit it accepts the proof, otherwise it answers
// the proof with a credit_insufficient 402 offering only next. It accepts a
// Credential for any other Challenge.
type creditRoundServer struct {
	proof, charge *mpp.Challenge
	next          []*mpp.Challenge
	credit        bool
	mu            sync.Mutex
	received      []*mpp.Credential
	requests      int
}

func (s *creditRoundServer) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Body != nil {
		_, _ = io.Copy(io.Discard, req.Body)
		_ = req.Body.Close()
	}
	var credential *mpp.Credential
	if value := req.Header.Get("Authorization"); value != "" {
		parsed, err := mpp.ParseCredential(value)
		if err != nil {
			return nil, err
		}
		credential = parsed
	}
	s.mu.Lock()
	s.requests++
	if credential != nil {
		s.received = append(s.received, credential)
	}
	s.mu.Unlock()

	w := httptest.NewRecorder()
	offer := func(problem string, challenges ...*mpp.Challenge) {
		for _, challenge := range challenges {
			w.Header().Add("WWW-Authenticate", mpptest.FormatChallenge(challenge))
		}
		if problem != "" {
			w.Header().Set("Content-Type", "application/problem+json")
		}
		w.WriteHeader(http.StatusPaymentRequired)
		_, _ = io.WriteString(w, problem)
	}
	switch {
	case credential == nil:
		offer("", s.proof, s.charge)
	case credential.Challenge.ID == s.proof.ID && !s.credit:
		offer(creditInsufficient, s.next...)
	default:
		w.WriteHeader(http.StatusOK)
	}
	resp := w.Result()
	resp.Request = req
	return resp, nil
}

func (s *creditRoundServer) credentials() []*mpp.Credential {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*mpp.Credential(nil), s.received...)
}

func chargeChallenge(t *testing.T, amount string) *mpp.Challenge {
	t.Helper()
	return buildChallenge(t, tempo.ChargeRequestParams{Amount: amount, Currency: testCurrency, Recipient: testRecipient, Decimals: 6, ChainID: 42431})
}

func newCreditRoundServer(t *testing.T, next ...*mpp.Challenge) *creditRoundServer {
	t.Helper()
	return &creditRoundServer{proof: chargeChallenge(t, "0"), charge: chargeChallenge(t, "0.01"), next: next}
}

// payThrough requests https://testRealm through the HTTP Transport with a
// Tempo method whose PaymentPolicy allows at most 0.01 of testCurrency.
func payThrough(t *testing.T, server *creditRoundServer, rpc *mockRPC) (*http.Response, error) {
	t.Helper()
	method, err := New(Config{PrivateKey: testPrivateKey, ChainID: 42431, RPC: rpc, PaymentPolicy: MaxAmount(testCurrency, "0.01", 6)})
	require.NoError(t, err)
	transport, err := client.NewHTTPTransport([]client.Method{method}, server)
	require.NoError(t, err)
	req, err := http.NewRequest(http.MethodGet, "https://"+testRealm+"/resource", nil)
	require.NoError(t, err)
	resp, err := transport.RoundTrip(req)
	if resp != nil {
		t.Cleanup(func() { _ = resp.Body.Close() })
	}
	return resp, err
}

func TestNewChallengeRoundPaysChargeAfterProof(t *testing.T) {
	next := chargeChallenge(t, "0.01")
	server := newCreditRoundServer(t, next)
	rpc := &mockRPC{chainID: 42431}

	resp, err := payThrough(t, server, rpc)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	credentials := server.credentials()
	require.Len(t, credentials, 2)
	assert.Equal(t, server.proof.ID, credentials[0].Challenge.ID)
	assert.Equal(t, string(tempo.CredentialTypeProof), credentials[0].Payload["type"])
	assert.Equal(t, next.ID, credentials[1].Challenge.ID)
	assert.Equal(t, string(tempo.CredentialTypeTransaction), credentials[1].Payload["type"])
	assert.Empty(t, rpc.sentRawTxs, "a pull credential is not broadcast by the client")
}

func TestNewChallengeRoundProofAcceptedWithCredit(t *testing.T) {
	server := newCreditRoundServer(t, chargeChallenge(t, "0.01"))
	server.credit = true

	resp, err := payThrough(t, server, &mockRPC{chainID: 42431})
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	credentials := server.credentials()
	require.Len(t, credentials, 1, "the real charge is never paid")
	assert.Equal(t, server.proof.ID, credentials[0].Challenge.ID)
	assert.Equal(t, string(tempo.CredentialTypeProof), credentials[0].Payload["type"])
}

func TestNewChallengeRoundChargeOverPolicyIsNotPaid(t *testing.T) {
	server := newCreditRoundServer(t, chargeChallenge(t, "0.50"))
	rpc := &mockRPC{chainID: 42431}

	_, err := payThrough(t, server, rpc)
	require.ErrorContains(t, err, "payment rejected by policy")
	require.ErrorIs(t, err, ErrPaymentPolicy)
	credentials := server.credentials()
	require.Len(t, credentials, 1, "only the proof is sent")
	assert.Equal(t, server.proof.ID, credentials[0].Challenge.ID)
	assert.Equal(t, 2, server.requests)
	assert.Empty(t, rpc.sentRawTxs)
}

func TestNewChallengeRoundProofForAnotherRealmIsNotSigned(t *testing.T) {
	request, err := tempo.NormalizeChargeRequest(tempo.ChargeRequestParams{Amount: "0", Currency: testCurrency, Recipient: testRecipient, Decimals: 6, ChainID: 42431})
	require.NoError(t, err)
	relayed := mpptest.NewChallenge("secret-key", "other.example", tempo.MethodName, tempo.IntentCharge, request.Map(),
		mpp.WithExpires(mpp.ExpiresIn(5*time.Minute)))
	server := newCreditRoundServer(t, relayed)

	_, err = payThrough(t, server, &mockRPC{chainID: 42431})
	require.ErrorContains(t, err, "refusing to sign Tempo proof for realm")
	require.Len(t, server.credentials(), 1, "only the first proof is sent")
	assert.Equal(t, 2, server.requests)
}
