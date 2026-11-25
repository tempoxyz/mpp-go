package client

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tempoxyz/mpp-go/pkg/internal/mpptest"
	"github.com/tempoxyz/mpp-go/pkg/mpp"
)

// These tests cover a 402 that answers a Credential with new Challenges: a
// zero-amount proof Challenge (which a payer with prepaid credit signs to pay
// nothing) offered before the real charge, then, when the payer has no credit,
// a credit_insufficient 402 that offers only the real charge under a new ID.
// As in mppx's client Fetch retry rounds, the Transport pays the new
// Challenge, resends the same Credential when the Challenge it answered is
// offered again and stops at its retry limit.

const (
	roundRealm = "api.example.com"
	roundURL   = "https://" + roundRealm + "/resource"
	// creditInsufficientProblem is what a server answers a proof with when
	// the payer has no prepaid credit.
	creditInsufficientProblem = `{"type":"https://paymentauth.org/problems/credit-insufficient","title":"Credit insufficient","status":402,"detail":"credit_insufficient"}`
	verificationFailedProblem = `{"type":"https://paymentauth.org/problems/verification-failed","title":"Verification failed","status":402}`
)

// roundChallenge returns a tempo/charge Challenge for roundRealm. nonce makes
// Challenges with the same amount distinct, as a server's nonce does.
func roundChallenge(amount, nonce string) *mpp.Challenge {
	return mpptest.NewChallenge("secret", roundRealm, "tempo", "charge", map[string]any{
		"amount":    amount,
		"currency":  "0x20c0000000000000000000000000000000000001",
		"recipient": "0x70997970c51812dc3a010c7d01b50e0d17dc79c8",
	}, mpp.WithExpires(mpp.ExpiresIn(5*time.Minute)), mpp.WithMeta(map[string]string{"nonce": nonce}))
}

// roundMethod pays tempo/charge Challenges, with a proof for amount "0" and
// a transaction otherwise, and records the ID of every Challenge it pays.
type roundMethod struct {
	mu   sync.Mutex
	paid []string
}

func (m *roundMethod) Name() string   { return "tempo" }
func (m *roundMethod) Intent() string { return "charge" }

func (m *roundMethod) CreateCredential(_ context.Context, challenge *mpp.Challenge) (*mpp.Credential, error) {
	m.mu.Lock()
	m.paid = append(m.paid, challenge.ID)
	m.mu.Unlock()
	kind := "transaction"
	if challenge.Request["amount"] == "0" {
		kind = "proof"
	}
	return challenge.NewCredential(map[string]any{"type": kind, "signature": "0x" + challenge.ID}), nil
}

func (m *roundMethod) paidIDs() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.paid...)
}

// roundRequest is one request the fake server received.
type roundRequest struct {
	method     string
	url        string
	body       string
	header     http.Header
	credential *mpp.Credential
}

// roundServer is an in-process http.RoundTripper standing in for the server,
// so requests keep roundURL's HTTPS origin, which owns the proof's realm.
type roundServer struct {
	handle  func(n int, credential *mpp.Credential, w http.ResponseWriter)
	mu      sync.Mutex
	history []roundRequest
}

func (s *roundServer) RoundTrip(req *http.Request) (*http.Response, error) {
	var body []byte
	if req.Body != nil {
		var err error
		body, err = io.ReadAll(req.Body)
		_ = req.Body.Close()
		if err != nil {
			return nil, err
		}
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
	n := len(s.history)
	s.history = append(s.history, roundRequest{
		method: req.Method, url: req.URL.String(), body: string(body),
		header: req.Header.Clone(), credential: credential,
	})
	s.mu.Unlock()
	recorder := httptest.NewRecorder()
	s.handle(n, credential, recorder)
	resp := recorder.Result()
	resp.Request = req
	return resp, nil
}

// credentials returns the Credentials the server received, in order.
func (s *roundServer) credentials() []*mpp.Credential {
	s.mu.Lock()
	defer s.mu.Unlock()
	var credentials []*mpp.Credential
	for _, request := range s.history {
		if request.credential != nil {
			credentials = append(credentials, request.credential)
		}
	}
	return credentials
}

func (s *roundServer) requests() []roundRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]roundRequest(nil), s.history...)
}

// writeChallenges answers 402 with challenges and, when set, a problem body.
func writeChallenges(w http.ResponseWriter, problem string, challenges ...*mpp.Challenge) {
	for _, challenge := range challenges {
		w.Header().Add("WWW-Authenticate", mpptest.FormatChallenge(challenge))
	}
	if problem != "" {
		w.Header().Set("Content-Type", "application/problem+json")
	}
	w.WriteHeader(http.StatusPaymentRequired)
	_, _ = io.WriteString(w, problem)
}

func writePaid(w http.ResponseWriter) {
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, "paid")
}

// creditServer offers the proof, then the real charge. With credit it
// accepts the proof; without, it answers the proof with a credit_insufficient
// 402 offering only next, a new real charge, and accepts a Credential for it.
func creditServer(t *testing.T, credit bool, proof, charge, next *mpp.Challenge) *roundServer {
	return &roundServer{handle: func(_ int, credential *mpp.Credential, w http.ResponseWriter) {
		switch {
		case credential == nil:
			writeChallenges(w, "", proof, charge)
		case credential.Challenge.ID == proof.ID && credit:
			writePaid(w)
		case credential.Challenge.ID == proof.ID:
			writeChallenges(w, creditInsufficientProblem, next)
		case credential.Challenge.ID == next.ID:
			writePaid(w)
		default:
			writeChallenges(w, verificationFailedProblem, next)
		}
	}}
}

// roundEntryPoints are the HTTP entry points that pay a 402.
var roundEntryPoints = []string{"Transport", "Client", "NewHTTPClient"}

// roundClient returns the entry point's request function, with server as
// the inner transport.
func roundClient(t *testing.T, entry string, server http.RoundTripper, methods []Method, opts ...Option) func(*http.Request) (*http.Response, error) {
	t.Helper()
	withServer := append([]Option{WithHTTPClient(&http.Client{Transport: server})}, opts...)
	switch entry {
	case "Transport":
		transport, err := NewHTTPTransport(methods, server, opts...)
		require.NoError(t, err)
		return transport.RoundTrip
	case "Client":
		return New(methods, withServer...).Do
	case "NewHTTPClient":
		hc, err := NewHTTPClient(methods, withServer...)
		require.NoError(t, err)
		return hc.Do
	}
	t.Fatalf("unknown entry point %q", entry)
	return nil
}

func readBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return string(body)
}

func newRoundRequest(t *testing.T) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, roundURL, nil)
	require.NoError(t, err)
	return req
}

// The proof gets a credit_insufficient 402 offering only the real charge,
// which the client then pays.
func TestNewChallengeRound_PaysRealChargeAfterInsufficientCredit(t *testing.T) {
	proof, charge, next := roundChallenge("0", "1"), roundChallenge("10000", "1"), roundChallenge("10000", "2")
	for _, entry := range roundEntryPoints {
		t.Run(entry, func(t *testing.T) {
			server := creditServer(t, false, proof, charge, next)
			method := &roundMethod{}

			resp, err := roundClient(t, entry, server, []Method{method})(newRoundRequest(t))
			require.NoError(t, err)
			assert.Equal(t, http.StatusOK, resp.StatusCode)
			assert.Equal(t, "paid", readBody(t, resp))

			credentials := server.credentials()
			require.Len(t, credentials, 2)
			assert.Equal(t, proof.ID, credentials[0].Challenge.ID)
			assert.Equal(t, "proof", credentials[0].Payload["type"])
			assert.Equal(t, next.ID, credentials[1].Challenge.ID)
			assert.Equal(t, "transaction", credentials[1].Payload["type"])
			assert.Equal(t, []string{proof.ID, next.ID}, method.paidIDs())
			assert.Len(t, server.requests(), 3)
		})
	}
}

// The 402 answering the proof starts a new round whatever its problem type.
func TestNewChallengeRound_AnyProblemTypeStartsNewRound(t *testing.T) {
	proof, next := roundChallenge("0", "1"), roundChallenge("10000", "2")
	for name, problem := range map[string]string{
		"no problem":          "",
		"credit-insufficient": creditInsufficientProblem,
		"verification-failed": verificationFailedProblem,
		"payment-required":    `{"type":"https://paymentauth.org/problems/payment-required","status":402}`,
	} {
		t.Run(name, func(t *testing.T) {
			server := &roundServer{handle: func(_ int, credential *mpp.Credential, w http.ResponseWriter) {
				switch {
				case credential == nil:
					writeChallenges(w, "", proof)
				case credential.Challenge.ID == proof.ID:
					writeChallenges(w, problem, next)
				default:
					writePaid(w)
				}
			}}
			method := &roundMethod{}
			resp, err := newTestTransport([]Method{method}, server).RoundTrip(newRoundRequest(t))
			require.NoError(t, err)
			assert.Equal(t, http.StatusOK, resp.StatusCode)
			_ = readBody(t, resp)
			assert.Equal(t, []string{proof.ID, next.ID}, method.paidIDs())
		})
	}
}

// A payer with credit pays nothing: the proof is accepted and the real
// charge is never paid.
func TestNewChallengeRound_ProofAcceptedWithCredit(t *testing.T) {
	proof, charge, next := roundChallenge("0", "1"), roundChallenge("10000", "1"), roundChallenge("10000", "2")
	for _, entry := range roundEntryPoints {
		t.Run(entry, func(t *testing.T) {
			server := creditServer(t, true, proof, charge, next)
			method := &roundMethod{}

			resp, err := roundClient(t, entry, server, []Method{method})(newRoundRequest(t))
			require.NoError(t, err)
			assert.Equal(t, http.StatusOK, resp.StatusCode)
			assert.Equal(t, "paid", readBody(t, resp))

			credentials := server.credentials()
			require.Len(t, credentials, 1)
			assert.Equal(t, proof.ID, credentials[0].Challenge.ID)
			assert.Equal(t, []string{proof.ID}, method.paidIDs())
			assert.Len(t, server.requests(), 2)
		})
	}
}

// A 402 that re-offers the Challenge just answered never gets a second
// payment: at most the same Credential is resent, up to the retry limit, and
// the server's final 402 is returned.
func TestNewChallengeRound_SameChallengeIsNotPaidAgain(t *testing.T) {
	proof, charge, next := roundChallenge("0", "1"), roundChallenge("10000", "1"), roundChallenge("10000", "2")
	for _, tc := range []struct {
		name     string
		wantPaid []string
		// handle answers a request carrying credential.
		handle func(credential *mpp.Credential, w http.ResponseWriter)
	}{
		{
			name:     "proof re-offered",
			wantPaid: []string{proof.ID},
			handle: func(_ *mpp.Credential, w http.ResponseWriter) {
				writeChallenges(w, verificationFailedProblem, proof)
			},
		},
		{
			name:     "new real charge re-offered",
			wantPaid: []string{proof.ID, next.ID},
			handle: func(credential *mpp.Credential, w http.ResponseWriter) {
				if credential.Challenge.ID == proof.ID {
					writeChallenges(w, creditInsufficientProblem, next)
					return
				}
				writeChallenges(w, verificationFailedProblem, next)
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := &roundServer{handle: func(_ int, credential *mpp.Credential, w http.ResponseWriter) {
				if credential == nil {
					writeChallenges(w, "", proof, charge)
					return
				}
				tc.handle(credential, w)
			}}
			method := &roundMethod{}

			resp, err := newTestTransport([]Method{method}, server).RoundTrip(newRoundRequest(t))
			require.NoError(t, err, "a rejected Credential returns the server's 402")
			assert.Equal(t, http.StatusPaymentRequired, resp.StatusCode)
			assert.Contains(t, readBody(t, resp), "verification-failed")
			assert.Equal(t, tc.wantPaid, method.paidIDs(), "each Challenge is paid at most once")

			requests := server.requests()
			assert.Len(t, requests, defaultMaxPaymentRetries+1, "resends stop at the retry limit")
			last := tc.wantPaid[len(tc.wantPaid)-1]
			for _, request := range requests[len(tc.wantPaid)+1:] {
				require.NotNil(t, request.credential)
				assert.Equal(t, last, request.credential.Challenge.ID, "only the same Credential is resent")
			}
		})
	}
}

// A 402 answering the proof with no payable Challenge is the server's final
// answer: it is returned at once, without another payment.
func TestNewChallengeRound_NoPayableChallengeIsFinal(t *testing.T) {
	proof, charge := roundChallenge("0", "1"), roundChallenge("10000", "1")
	expired := mpptest.NewChallenge("secret", roundRealm, "tempo", "charge", map[string]any{"amount": "10000"},
		mpp.WithExpires(time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)))
	unsupported := mpptest.NewChallenge("secret", roundRealm, "stripe", "charge", map[string]any{"amount": "10000"})
	for name, offers := range map[string][]*mpp.Challenge{
		"no challenge":       nil,
		"unsupported method": {unsupported},
		"expired charge":     {expired},
	} {
		t.Run(name, func(t *testing.T) {
			for _, entry := range roundEntryPoints {
				t.Run(entry, func(t *testing.T) {
					server := &roundServer{handle: func(_ int, credential *mpp.Credential, w http.ResponseWriter) {
						if credential == nil {
							writeChallenges(w, "", proof, charge)
							return
						}
						writeChallenges(w, creditInsufficientProblem, offers...)
					}}
					method := &roundMethod{}

					resp, err := roundClient(t, entry, server, []Method{method})(newRoundRequest(t))
					require.NoError(t, err)
					assert.Equal(t, http.StatusPaymentRequired, resp.StatusCode)
					assert.Contains(t, readBody(t, resp), "credit_insufficient")
					assert.Len(t, server.requests(), 2)
					assert.Equal(t, []string{proof.ID}, method.paidIDs())
				})
			}
		})
	}
}

// A server that answers every Credential with a new Challenge is paid at
// most the retry limit's number of times, then its 402 is returned.
func TestNewChallengeRound_StopsAtRetryLimit(t *testing.T) {
	for name, tc := range map[string]struct {
		opts  []Option
		limit int
	}{
		"default": {limit: defaultMaxPaymentRetries},
		"zero":    {opts: []Option{WithMaxRetries(0)}, limit: 0},
		"one":     {opts: []Option{WithMaxRetries(1)}, limit: 1},
		"five":    {opts: []Option{WithMaxRetries(5)}, limit: 5},
	} {
		t.Run(name, func(t *testing.T) {
			for _, entry := range roundEntryPoints {
				t.Run(entry, func(t *testing.T) {
					server := &roundServer{handle: func(n int, _ *mpp.Credential, w http.ResponseWriter) {
						amount := "10000"
						if n == 0 {
							amount = "0"
						}
						writeChallenges(w, creditInsufficientProblem, roundChallenge(amount, strings.Repeat("n", n+1)))
					}}
					method := &roundMethod{}

					resp, err := roundClient(t, entry, server, []Method{method}, tc.opts...)(newRoundRequest(t))
					require.NoError(t, err)
					assert.Equal(t, http.StatusPaymentRequired, resp.StatusCode)
					_ = readBody(t, resp)
					paid := method.paidIDs()
					assert.Len(t, paid, tc.limit)
					assert.Len(t, server.requests(), tc.limit+1)
					seen := make(map[string]bool)
					for _, id := range paid {
						assert.False(t, seen[id], "challenge %s paid twice", id)
						seen[id] = true
					}
				})
			}
		})
	}
}

// The new round's Challenges go through every check the first round's do.
func TestNewChallengeRound_NewRoundIsChecked(t *testing.T) {
	proof := roundChallenge("0", "1")
	next := roundChallenge("10000", "2")
	serve := func(t *testing.T, offers ...*mpp.Challenge) *roundServer {
		return &roundServer{handle: func(_ int, credential *mpp.Credential, w http.ResponseWriter) {
			switch {
			case credential == nil:
				writeChallenges(w, "", proof)
			case credential.Challenge.ID == proof.ID:
				writeChallenges(w, creditInsufficientProblem, offers...)
			default:
				writePaid(w)
			}
		}}
	}

	t.Run("proof for another realm is refused", func(t *testing.T) {
		relayed := mpptest.NewChallenge("secret", "other.example", "tempo", "charge", map[string]any{"amount": "0"},
			mpp.WithExpires(mpp.ExpiresIn(5*time.Minute)))
		for _, entry := range roundEntryPoints {
			t.Run(entry, func(t *testing.T) {
				server := serve(t, relayed)
				method := &roundMethod{}
				_, err := roundClient(t, entry, server, []Method{method})(newRoundRequest(t))
				require.ErrorContains(t, err, "refusing to sign Tempo proof for realm")
				assert.Equal(t, []string{proof.ID}, method.paidIDs())
				assert.Len(t, server.requests(), 2)
			})
		}
	})

	t.Run("relayed proof is skipped for a valid charge", func(t *testing.T) {
		relayed := mpptest.NewChallenge("secret", "other.example", "tempo", "charge", map[string]any{"amount": "0"})
		server := serve(t, relayed, next)
		method := &roundMethod{}
		resp, err := newTestTransport([]Method{method}, server).RoundTrip(newRoundRequest(t))
		require.NoError(t, err)
		assert.Equal(t, http.StatusOK, resp.StatusCode)
		_ = readBody(t, resp)
		assert.Equal(t, []string{proof.ID, next.ID}, method.paidIDs())
	})

	t.Run("onChallenge is asked again and can decline", func(t *testing.T) {
		server := serve(t, next)
		method := &roundMethod{}
		var asked []string
		overBudget := errors.New("over budget")
		tr := newTestTransport([]Method{method}, server, WithOnChallenge(func(_ context.Context, challenge *mpp.Challenge, _ func(context.Context) (*mpp.Credential, error)) (*mpp.Credential, error) {
			asked = append(asked, challenge.ID)
			if challenge.Request["amount"] != "0" {
				return nil, overBudget
			}
			return nil, nil
		}))
		_, err := tr.RoundTrip(newRoundRequest(t))
		require.ErrorIs(t, err, ErrPaymentDeclined)
		require.ErrorIs(t, err, overBudget)
		assert.Equal(t, []string{proof.ID, next.ID}, asked)
		assert.Equal(t, []string{proof.ID}, method.paidIDs())
		assert.Len(t, server.requests(), 2)
	})

	t.Run("orderChallenges sees each round and can refuse", func(t *testing.T) {
		server := serve(t, next)
		method := &roundMethod{}
		var rounds [][]string
		tr := newTestTransport([]Method{method}, server, WithOrderChallenges(func(_ context.Context, candidates []ChallengeCandidate) ([]ChallengeCandidate, error) {
			var ids []string
			for _, candidate := range candidates {
				ids = append(ids, candidate.Challenge.ID)
			}
			rounds = append(rounds, ids)
			if len(rounds) > 1 {
				return nil, nil
			}
			return candidates, nil
		}))
		resp, err := tr.RoundTrip(newRoundRequest(t))
		require.NoError(t, err)
		assert.Equal(t, http.StatusPaymentRequired, resp.StatusCode)
		_ = readBody(t, resp)
		assert.Equal(t, [][]string{{proof.ID}, {next.ID}}, rounds)
		assert.Equal(t, []string{proof.ID}, method.paidIDs())
	})

	t.Run("method declined by payment preference is not paid", func(t *testing.T) {
		other := mpptest.NewChallenge("secret", roundRealm, "stripe", "charge", map[string]any{"amount": "10000"})
		server := serve(t, other)
		method := &roundMethod{}
		stripe := &mockMethod{name: "stripe", intent: "charge", cred: newTestCredential("stripe")}
		tr := newTestTransport([]Method{method, stripe}, server, WithPaymentPreferences(PaymentPreferences{"stripe/charge": 0}))
		resp, err := tr.RoundTrip(newRoundRequest(t))
		require.NoError(t, err)
		assert.Equal(t, http.StatusPaymentRequired, resp.StatusCode)
		_ = readBody(t, resp)
		assert.Zero(t, stripe.calls)
		assert.Equal(t, []string{proof.ID}, method.paidIDs())
	})
}

// Every retry sends the caller's method, URL, body and headers again, with
// only the newest round's Credential.
func TestNewChallengeRound_RetriesPreserveRequest(t *testing.T) {
	proof, charge, next := roundChallenge("0", "1"), roundChallenge("10000", "1"), roundChallenge("10000", "2")
	for _, entry := range roundEntryPoints {
		t.Run(entry, func(t *testing.T) {
			server := creditServer(t, false, proof, charge, next)
			method := &roundMethod{}
			const body = `{"query":"paid"}`
			req, err := http.NewRequest(http.MethodPost, roundURL+"?page=2", strings.NewReader(body))
			require.NoError(t, err)
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-Request-Id", "abc")

			resp, err := roundClient(t, entry, server, []Method{method})(req)
			require.NoError(t, err)
			assert.Equal(t, http.StatusOK, resp.StatusCode)
			_ = readBody(t, resp)

			requests := server.requests()
			require.Len(t, requests, 3)
			for i, got := range requests {
				assert.Equal(t, http.MethodPost, got.method, "request %d", i)
				assert.Equal(t, roundURL+"?page=2", got.url, "request %d", i)
				assert.Equal(t, body, got.body, "request %d", i)
				assert.Equal(t, "application/json", got.header.Get("Content-Type"), "request %d", i)
				assert.Equal(t, "abc", got.header.Get("X-Request-Id"), "request %d", i)
				assert.Len(t, got.header.Values("Authorization"), min(i, 1), "request %d", i)
			}
			assert.Equal(t, proof.ID, requests[1].credential.Challenge.ID)
			assert.Equal(t, next.ID, requests[2].credential.Challenge.ID)
			assert.Empty(t, req.Header.Get("Authorization"), "the caller's request is not modified")
		})
	}
}

// A 402 that offers again the Challenge just answered gets the same
// Credential again, as in mppx: exactly one signing for any problem but
// invalid-payload, and display fields such as the description do not make it
// a new Challenge. Sends stop at the retry limit.
func TestNewChallengeRound_SameChallengeResendsSameCredential(t *testing.T) {
	charge := roundChallenge("10000", "1")
	reoffered := *charge
	reoffered.Description = "Pay again"
	for name, problem := range map[string]string{
		"verification-failed": verificationFailedProblem,
		"payment-expired":     `{"type":"https://paymentauth.org/problems/payment-expired","status":402}`,
		"no problem":          "",
	} {
		t.Run(name, func(t *testing.T) {
			for _, entry := range roundEntryPoints {
				t.Run(entry, func(t *testing.T) {
					server := &roundServer{handle: func(_ int, credential *mpp.Credential, w http.ResponseWriter) {
						if credential == nil {
							writeChallenges(w, "", charge)
							return
						}
						writeChallenges(w, problem, &reoffered)
					}}
					method := &roundMethod{}

					resp, err := roundClient(t, entry, server, []Method{method})(newRoundRequest(t))
					require.NoError(t, err)
					assert.Equal(t, http.StatusPaymentRequired, resp.StatusCode, "the last 402 is the result")
					_ = readBody(t, resp)
					assert.Equal(t, []string{charge.ID}, method.paidIDs(), "exactly one signing")

					requests := server.requests()
					require.Len(t, requests, 1+defaultMaxPaymentRetries, "at most three Credentials are sent")
					sent := requests[1].header.Get("Authorization")
					require.NotEmpty(t, sent)
					for _, request := range requests[1:] {
						assert.Equal(t, sent, request.header.Get("Authorization"), "the same Credential is resent")
					}
				})
			}
		})
	}

	t.Run("accepted on resend", func(t *testing.T) {
		server := &roundServer{handle: func(n int, credential *mpp.Credential, w http.ResponseWriter) {
			switch {
			case credential == nil:
				writeChallenges(w, "", charge)
			case n == 1:
				writeChallenges(w, verificationFailedProblem, charge)
			default:
				writePaid(w)
			}
		}}
		method := &roundMethod{}
		resp, err := newTestTransport([]Method{method}, server).RoundTrip(newRoundRequest(t))
		require.NoError(t, err)
		assert.Equal(t, "paid", readBody(t, resp))
		assert.Equal(t, []string{charge.ID}, method.paidIDs())
		requests := server.requests()
		require.Len(t, requests, 3)
		assert.Equal(t, requests[1].header.Get("Authorization"), requests[2].header.Get("Authorization"))
	})
}

const invalidPayloadProblem = `{"type":"https://paymentauth.org/problems/invalid-payload","title":"Invalid payload","status":402}`

// An invalid-payload problem rejects the Credential itself: when its 402
// offers the same Challenge again, the Transport signs a new Credential for
// it instead of resending the rejected one. Each new Credential counts toward
// the retry limit, so a server that answers invalid-payload forever gets three.
func TestNewChallengeRound_InvalidPayloadSignsNewCredential(t *testing.T) {
	charge := roundChallenge("10000", "1")
	reoffered := *charge
	reoffered.Description = "Pay again"

	t.Run("accepted", func(t *testing.T) {
		for _, entry := range roundEntryPoints {
			t.Run(entry, func(t *testing.T) {
				server := &roundServer{handle: func(n int, credential *mpp.Credential, w http.ResponseWriter) {
					switch {
					case credential == nil:
						writeChallenges(w, "", charge)
					case n == 1:
						writeChallenges(w, invalidPayloadProblem, &reoffered)
					default:
						writePaid(w)
					}
				}}
				method := &roundMethod{}

				resp, err := roundClient(t, entry, server, []Method{method})(newRoundRequest(t))
				require.NoError(t, err)
				assert.Equal(t, http.StatusOK, resp.StatusCode)
				assert.Equal(t, "paid", readBody(t, resp))
				assert.Equal(t, []string{charge.ID, charge.ID}, method.paidIDs(), "a new Credential is signed")
				require.Len(t, server.requests(), 3)
				credentials := server.credentials()
				require.Len(t, credentials, 2)
				assert.Equal(t, charge.ID, credentials[1].Challenge.ID)
			})
		}
	})

	t.Run("invalid-payload forever", func(t *testing.T) {
		for _, entry := range roundEntryPoints {
			t.Run(entry, func(t *testing.T) {
				server := &roundServer{handle: func(_ int, credential *mpp.Credential, w http.ResponseWriter) {
					if credential == nil {
						writeChallenges(w, "", charge)
						return
					}
					writeChallenges(w, invalidPayloadProblem, charge)
				}}
				method := &roundMethod{}

				resp, err := roundClient(t, entry, server, []Method{method})(newRoundRequest(t))
				require.NoError(t, err)
				assert.Equal(t, http.StatusPaymentRequired, resp.StatusCode, "the last 402 is the result")
				assert.Contains(t, readBody(t, resp), "invalid-payload")
				assert.Equal(t, []string{charge.ID, charge.ID, charge.ID}, method.paidIDs(), "one signing per Credential sent")
				assert.Len(t, server.requests(), 1+defaultMaxPaymentRetries, "at most three Credentials are sent")
			})
		}
	})

	t.Run("other problem after invalid-payload resends", func(t *testing.T) {
		server := &roundServer{handle: func(n int, credential *mpp.Credential, w http.ResponseWriter) {
			switch {
			case credential == nil:
				writeChallenges(w, "", charge)
			case n == 1:
				writeChallenges(w, invalidPayloadProblem, charge)
			case n == 2:
				writeChallenges(w, verificationFailedProblem, charge)
			default:
				writePaid(w)
			}
		}}
		method := &roundMethod{}
		resp, err := newTestTransport([]Method{method}, server).RoundTrip(newRoundRequest(t))
		require.NoError(t, err)
		assert.Equal(t, "paid", readBody(t, resp))
		assert.Equal(t, []string{charge.ID, charge.ID}, method.paidIDs())
		requests := server.requests()
		require.Len(t, requests, 4)
		assert.Equal(t, requests[2].header.Get("Authorization"), requests[3].header.Get("Authorization"), "the new Credential is resent")
	})
}

// Only the Challenge the last request answered is resent: a Challenge
// answered in an earlier round and offered again gets a new Credential, as
// in mppx.
func TestNewChallengeRound_EarlierChallengeGetsNewCredential(t *testing.T) {
	first, second := roundChallenge("10000", "1"), roundChallenge("10000", "2")
	server := &roundServer{handle: func(n int, credential *mpp.Credential, w http.ResponseWriter) {
		switch n {
		case 0:
			writeChallenges(w, "", first)
		case 1:
			writeChallenges(w, verificationFailedProblem, second)
		case 2:
			writeChallenges(w, verificationFailedProblem, first)
		default:
			writePaid(w)
		}
	}}
	method := &roundMethod{}
	resp, err := newTestTransport([]Method{method}, server).RoundTrip(newRoundRequest(t))
	require.NoError(t, err)
	assert.Equal(t, "paid", readBody(t, resp))
	assert.Equal(t, []string{first.ID, second.ID, first.ID}, method.paidIDs())
	assert.Len(t, server.requests(), 4)
}

// Only a 402 starts a new round: any other answer to a Credential is the
// final result, even when it carries a new Challenge.
func TestNewChallengeRound_NonPaymentRequiredIsFinal(t *testing.T) {
	charge, next := roundChallenge("10000", "1"), roundChallenge("10000", "2")
	for _, status := range []int{http.StatusOK, http.StatusBadRequest, http.StatusGone, http.StatusInternalServerError, http.StatusServiceUnavailable} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			for _, entry := range roundEntryPoints {
				t.Run(entry, func(t *testing.T) {
					server := &roundServer{handle: func(_ int, credential *mpp.Credential, w http.ResponseWriter) {
						if credential == nil {
							writeChallenges(w, "", charge)
							return
						}
						w.Header().Add("WWW-Authenticate", mpptest.FormatChallenge(next))
						w.WriteHeader(status)
					}}
					method := &roundMethod{}
					resp, err := roundClient(t, entry, server, []Method{method})(newRoundRequest(t))
					require.NoError(t, err)
					assert.Equal(t, status, resp.StatusCode)
					_ = readBody(t, resp)
					assert.Equal(t, []string{charge.ID}, method.paidIDs())
					assert.Len(t, server.requests(), 2)
				})
			}
		})
	}
}
