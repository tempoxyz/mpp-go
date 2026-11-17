package mcp_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tempoxyz/mpp-go/pkg/client"
	"github.com/tempoxyz/mpp-go/pkg/internal/mpptest"
	mcpclient "github.com/tempoxyz/mpp-go/pkg/mcp/client"
	"github.com/tempoxyz/mpp-go/pkg/mpp"
)

// These tests cover a tool call whose Credential is answered with new
// Challenges: a zero-amount Tempo proof Challenge (which a payer with prepaid
// credit signs to pay nothing) offered before the real charge, then, without
// credit, a credit_insufficient payment-required error offering only the real
// charge under a new ID. As in mppx's client Fetch retry rounds, the client
// pays the new Challenge, resends the same Credential when the Challenge it
// answered is offered again and stops at its retry limit.

// roundChallenge returns a tempo/charge Challenge; nonce makes Challenges
// with the same amount distinct.
func roundChallenge(challengeRealm, amount, nonce string) *mpp.Challenge {
	return mpptest.NewChallenge(secretKey, challengeRealm, "tempo", "charge", map[string]any{
		"amount":    amount,
		"currency":  "0x20c0000000000000000000000000000000000001",
		"recipient": "0x70997970c51812dc3a010c7d01b50e0d17dc79c8",
	}, mpp.WithExpires(mpp.ExpiresIn(5*time.Minute)), mpp.WithMeta(map[string]string{"nonce": nonce}))
}

// roundPayer pays tempo/charge Challenges, with a proof for amount "0" and a
// transaction otherwise, and records the ID of every Challenge it pays.
type roundPayer struct {
	mu   sync.Mutex
	paid []string
}

func (m *roundPayer) Name() string   { return "tempo" }
func (m *roundPayer) Intent() string { return "charge" }

func (m *roundPayer) CreateCredential(_ context.Context, challenge *mpp.Challenge) (*mpp.Credential, error) {
	m.mu.Lock()
	m.paid = append(m.paid, challenge.ID)
	m.mu.Unlock()
	kind := "transaction"
	if challenge.Request["amount"] == "0" {
		kind = "proof"
	}
	return challenge.NewCredential(map[string]any{"type": kind, "signature": "0x" + challenge.ID}), nil
}

// ValidateOrigin trusts proofs for realm over an origin-less session, as
// the Tempo method's Config.TrustedProofRealms does.
func (m *roundPayer) ValidateOrigin(origin *url.URL, challenge *mpp.Challenge) error {
	if challenge.Request["amount"] != "0" || (origin == nil && challenge.Realm == realm) {
		return nil
	}
	return fmt.Errorf("proof realm %q not trusted", challenge.Realm)
}

func (m *roundPayer) paidIDs() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.paid...)
}

// roundCall is one tools/call the server received.
type roundCall struct {
	name, arguments string
	challengeID     string // the Credential's Challenge ID, or ""
	payloadType     string
}

// roundServer records each call and answers it with handle, given the
// call's index and the Challenge ID its Credential answers ("" for none).
type roundServer struct {
	mu    sync.Mutex
	calls []roundCall
}

func (s *roundServer) serve(handle func(n int, challengeID string) error) *mcp.Server {
	return newServer(func(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		call := roundCall{name: req.Params.Name, arguments: string(req.Params.Arguments)}
		if value, ok := req.Params.Meta[mpp.MCPCredentialMetaKey]; ok {
			raw, err := json.Marshal(value)
			if err != nil {
				return nil, err
			}
			var credential struct {
				Challenge struct {
					ID string `json:"id"`
				} `json:"challenge"`
				Payload map[string]any `json:"payload"`
			}
			if err := json.Unmarshal(raw, &credential); err != nil {
				return nil, err
			}
			call.challengeID = credential.Challenge.ID
			call.payloadType = fmt.Sprint(credential.Payload["type"])
		}
		s.mu.Lock()
		n := len(s.calls)
		s.calls = append(s.calls, call)
		s.mu.Unlock()
		if err := handle(n, call.challengeID); err != nil {
			return nil, err
		}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "paid"}}}, nil
	})
}

func (s *roundServer) received() []roundCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]roundCall(nil), s.calls...)
}

// paymentRequiredError is a payment-required error offering challenges, with
// problem details when problemType is set.
func paymentRequiredError(t *testing.T, problemType string, challenges ...*mpp.Challenge) error {
	t.Helper()
	data := mpp.MCPPaymentRequiredData{HTTPStatus: 402, Challenges: challenges}
	if problemType != "" {
		data.Problem = map[string]any{"type": problemType, "status": 402, "detail": "credit_insufficient"}
	}
	raw, err := json.Marshal(data)
	require.NoError(t, err)
	return &jsonrpc.Error{Code: mpp.MCPPaymentRequiredCode, Message: "Payment Required", Data: raw}
}

const creditInsufficientType = "https://paymentauth.org/problems/credit-insufficient"

// roundConfig trusts the in-memory session with proofs for realm only.
func roundConfig(payer client.Method) mcpclient.Config {
	return mcpclient.Config{
		Methods: []client.Method{payer},
		ValidateTempoProofSession: func(_ context.Context, _ *mcp.ClientSession, proofRealm string) error {
			if proofRealm != realm {
				return fmt.Errorf("untrusted proof realm %q", proofRealm)
			}
			return nil
		},
	}
}

// creditHandler offers proof then charge; with credit it accepts the proof,
// otherwise it answers the proof with a credit_insufficient error offering
// only next, and accepts a Credential for next.
func creditHandler(t *testing.T, credit bool, proof, charge, next *mpp.Challenge) func(int, string) error {
	return func(_ int, challengeID string) error {
		switch {
		case challengeID == "":
			return paymentRequiredError(t, "", proof, charge)
		case challengeID == proof.ID && credit:
			return nil
		case challengeID == proof.ID:
			return paymentRequiredError(t, creditInsufficientType, next)
		case challengeID == next.ID:
			return nil
		}
		return paymentRequiredError(t, string(mpp.ErrorTypeVerificationFailed), next)
	}
}

func TestMCPNewChallengeRound_PaysRealChargeAfterInsufficientCredit(t *testing.T) {
	proof, charge, next := roundChallenge(realm, "0", "1"), roundChallenge(realm, "10000", "1"), roundChallenge(realm, "10000", "2")
	server := &roundServer{}
	payer := &roundPayer{}
	session := connect(t, server.serve(creditHandler(t, false, proof, charge, next)), mcpclient.Wrap(newClient(), roundConfig(payer)))

	result, err := callPremium(context.Background(), session, nil)
	require.NoError(t, err)
	require.False(t, result.IsError)
	assert.Equal(t, []string{proof.ID, next.ID}, payer.paidIDs())

	calls := server.received()
	require.Len(t, calls, 3)
	assert.Empty(t, calls[0].challengeID)
	assert.Equal(t, proof.ID, calls[1].challengeID)
	assert.Equal(t, "proof", calls[1].payloadType)
	assert.Equal(t, next.ID, calls[2].challengeID)
	assert.Equal(t, "transaction", calls[2].payloadType)
}

func TestMCPNewChallengeRound_ProofAcceptedWithCredit(t *testing.T) {
	proof, charge, next := roundChallenge(realm, "0", "1"), roundChallenge(realm, "10000", "1"), roundChallenge(realm, "10000", "2")
	server := &roundServer{}
	payer := &roundPayer{}
	session := connect(t, server.serve(creditHandler(t, true, proof, charge, next)), mcpclient.Wrap(newClient(), roundConfig(payer)))

	result, err := callPremium(context.Background(), session, nil)
	require.NoError(t, err)
	require.False(t, result.IsError)
	assert.Equal(t, []string{proof.ID}, payer.paidIDs(), "the real charge is never paid")
	require.Len(t, server.received(), 2)
}

// A re-offered Challenge gets the same Credential again, never a second
// payment, until the retry limit returns the server's error.
func TestMCPNewChallengeRound_SameChallengeIsNotPaidAgain(t *testing.T) {
	proof, charge, next := roundChallenge(realm, "0", "1"), roundChallenge(realm, "10000", "1"), roundChallenge(realm, "10000", "2")
	for name, tc := range map[string]struct {
		wantPaid []string
		handle   func(challengeID string) error
	}{
		"proof re-offered": {
			wantPaid: []string{proof.ID},
			handle: func(string) error {
				return paymentRequiredError(t, string(mpp.ErrorTypeVerificationFailed), proof)
			},
		},
		"new real charge re-offered": {
			wantPaid: []string{proof.ID, next.ID},
			handle: func(challengeID string) error {
				if challengeID == proof.ID {
					return paymentRequiredError(t, creditInsufficientType, next)
				}
				return paymentRequiredError(t, string(mpp.ErrorTypeVerificationFailed), next)
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			server := &roundServer{}
			payer := &roundPayer{}
			srv := server.serve(func(_ int, challengeID string) error {
				if challengeID == "" {
					return paymentRequiredError(t, "", proof, charge)
				}
				return tc.handle(challengeID)
			})
			session := connect(t, srv, mcpclient.Wrap(newClient(), roundConfig(payer)))

			_, err := callPremium(context.Background(), session, nil)
			wireErr, data := paymentData(t, err)
			assert.EqualValues(t, mpp.MCPPaymentRequiredCode, wireErr.Code)
			assert.Equal(t, string(mpp.ErrorTypeVerificationFailed), data.Problem["type"])
			assert.Equal(t, tc.wantPaid, payer.paidIDs(), "each Challenge is paid at most once")

			calls := server.received()
			require.Len(t, calls, 4, "one unpaid call and three paid retries")
			last := tc.wantPaid[len(tc.wantPaid)-1]
			for _, call := range calls[len(tc.wantPaid)+1:] {
				assert.Equal(t, last, call.challengeID, "only the same Credential is resent")
			}
		})
	}
}

// An error answering the proof with no payable Challenge ends the call with
// that error, without another payment.
func TestMCPNewChallengeRound_NoPayableChallengeIsFinal(t *testing.T) {
	proof, charge := roundChallenge(realm, "0", "1"), roundChallenge(realm, "10000", "1")
	expired := mpptest.NewChallenge(secretKey, realm, "tempo", "charge", map[string]any{"amount": "10000"},
		mpp.WithExpires(time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)))
	unsupported := mpptest.NewChallenge(secretKey, realm, "stripe", "charge", map[string]any{"amount": "10000"})
	for name, final := range map[string]func(t *testing.T) error{
		"no challenge": func(t *testing.T) error {
			raw, err := json.Marshal(map[string]any{"httpStatus": 402, "challenges": []any{}, "problem": map[string]any{"type": creditInsufficientType}})
			require.NoError(t, err)
			return &jsonrpc.Error{Code: mpp.MCPPaymentVerificationFailedCode, Message: "Credit insufficient", Data: raw}
		},
		"unsupported method": func(t *testing.T) error { return paymentRequiredError(t, creditInsufficientType, unsupported) },
		"expired charge":     func(t *testing.T) error { return paymentRequiredError(t, creditInsufficientType, expired) },
	} {
		t.Run(name, func(t *testing.T) {
			server := &roundServer{}
			payer := &roundPayer{}
			srv := server.serve(func(_ int, challengeID string) error {
				if challengeID == "" {
					return paymentRequiredError(t, "", proof, charge)
				}
				return final(t)
			})
			session := connect(t, srv, mcpclient.Wrap(newClient(), roundConfig(payer)))

			_, err := callPremium(context.Background(), session, nil)
			_, data := paymentData(t, err)
			assert.Equal(t, creditInsufficientType, data.Problem["type"], "the server's error is returned")
			assert.Len(t, server.received(), 2)
			assert.Equal(t, []string{proof.ID}, payer.paidIDs())
		})
	}
}

// A server that answers every Credential with a new Challenge is paid at
// most three times.
func TestMCPNewChallengeRound_StopsAtRetryLimit(t *testing.T) {
	server := &roundServer{}
	payer := &roundPayer{}
	srv := server.serve(func(n int, _ string) error {
		amount := "10000"
		if n == 0 {
			amount = "0"
		}
		return paymentRequiredError(t, creditInsufficientType, roundChallenge(realm, amount, fmt.Sprint(n)))
	})
	session := connect(t, srv, mcpclient.Wrap(newClient(), roundConfig(payer)))

	_, err := callPremium(context.Background(), session, nil)
	wireErr, _ := paymentData(t, err)
	assert.EqualValues(t, mpp.MCPPaymentRequiredCode, wireErr.Code)
	paid := payer.paidIDs()
	assert.Len(t, paid, 3)
	assert.Len(t, server.received(), 4)
	seen := make(map[string]bool)
	for _, id := range paid {
		assert.False(t, seen[id], "challenge %s paid twice", id)
		seen[id] = true
	}
}

// The new round's Challenges go through every check the first round's do.
func TestMCPNewChallengeRound_NewRoundIsChecked(t *testing.T) {
	proof, next := roundChallenge(realm, "0", "1"), roundChallenge(realm, "10000", "2")
	serve := func(t *testing.T, server *roundServer, offers ...*mpp.Challenge) *mcp.Server {
		return server.serve(func(_ int, challengeID string) error {
			switch challengeID {
			case "":
				return paymentRequiredError(t, "", proof)
			case proof.ID:
				return paymentRequiredError(t, creditInsufficientType, offers...)
			}
			return nil
		})
	}

	t.Run("proof for another realm is refused", func(t *testing.T) {
		server := &roundServer{}
		payer := &roundPayer{}
		session := connect(t, serve(t, server, roundChallenge("other.example", "0", "2")), mcpclient.Wrap(newClient(), roundConfig(payer)))
		_, err := callPremium(context.Background(), session, nil)
		require.ErrorContains(t, err, "untrusted proof realm")
		assert.Equal(t, []string{proof.ID}, payer.paidIDs())
		assert.Len(t, server.received(), 2)
	})

	t.Run("OnPaymentRequired is asked again and can decline", func(t *testing.T) {
		server := &roundServer{}
		payer := &roundPayer{}
		var asked []string
		config := roundConfig(payer)
		config.OnPaymentRequired = func(_ context.Context, challenge *mpp.Challenge) (bool, error) {
			asked = append(asked, challenge.ID)
			return challenge.Request["amount"] == "0", nil
		}
		session := connect(t, serve(t, server, next), mcpclient.Wrap(newClient(), config))
		_, err := callPremium(context.Background(), session, nil)
		require.ErrorIs(t, err, mcpclient.ErrPaymentDeclined)
		assert.Equal(t, []string{proof.ID, next.ID}, asked)
		assert.Equal(t, []string{proof.ID}, payer.paidIDs())
		assert.Len(t, server.received(), 2)
	})

	t.Run("OrderChallenges sees each round and can refuse", func(t *testing.T) {
		server := &roundServer{}
		payer := &roundPayer{}
		var rounds [][]string
		refused := errors.New("not paying the charge")
		config := roundConfig(payer)
		config.OrderChallenges = func(_ context.Context, candidates []client.ChallengeCandidate) ([]client.ChallengeCandidate, error) {
			var ids []string
			for _, candidate := range candidates {
				ids = append(ids, candidate.Challenge.ID)
			}
			rounds = append(rounds, ids)
			if len(rounds) > 1 {
				return nil, refused
			}
			return candidates, nil
		}
		session := connect(t, serve(t, server, next), mcpclient.Wrap(newClient(), config))
		_, err := callPremium(context.Background(), session, nil)
		require.ErrorIs(t, err, refused)
		assert.Equal(t, [][]string{{proof.ID}, {next.ID}}, rounds)
		assert.Equal(t, []string{proof.ID}, payer.paidIDs())
	})
}

// Every retry sends the original tool name and arguments again, with only
// the newest round's Credential.
func TestMCPNewChallengeRound_RetriesPreserveCall(t *testing.T) {
	proof, charge, next := roundChallenge(realm, "0", "1"), roundChallenge(realm, "10000", "1"), roundChallenge(realm, "10000", "2")
	server := &roundServer{}
	payer := &roundPayer{}
	session := connect(t, server.serve(creditHandler(t, false, proof, charge, next)), mcpclient.Wrap(newClient(), roundConfig(payer)))

	params := &mcp.CallToolParams{Name: "premium", Arguments: map[string]any{"query": "paid", "page": 2}}
	result, err := session.CallTool(context.Background(), params)
	require.NoError(t, err)
	require.False(t, result.IsError)

	calls := server.received()
	require.Len(t, calls, 3)
	for i, call := range calls {
		assert.Equal(t, "premium", call.name, "call %d", i)
		assert.JSONEq(t, `{"query":"paid","page":2}`, call.arguments, "call %d", i)
	}
	assert.Equal(t, []string{"", proof.ID, next.ID}, []string{calls[0].challengeID, calls[1].challengeID, calls[2].challengeID})
	assert.NotContains(t, params.Meta, mpp.MCPCredentialMetaKey, "the caller's params get no Credential")
}

// paymentError is a payment error with code offering challenges; httpStatus 0
// leaves httpStatus out.
func paymentError(t *testing.T, code int64, httpStatus int, problemType string, challenges ...*mpp.Challenge) error {
	t.Helper()
	data := map[string]any{"challenges": challenges, "problem": map[string]any{"type": problemType}}
	if httpStatus != 0 {
		data["httpStatus"] = httpStatus
	}
	raw, err := json.Marshal(data)
	require.NoError(t, err)
	return &jsonrpc.Error{Code: code, Message: "payment error", Data: raw}
}

// A payment-required answer that offers again the Challenge just answered
// gets the same Credential again, as in mppx: exactly one signing, and
// display fields such as the description do not make it a new Challenge.
// At most three Credentials are sent.
func TestMCPNewChallengeRound_SameChallengeResendsSameCredential(t *testing.T) {
	charge := roundChallenge(realm, "10000", "1")
	reoffered := *charge
	reoffered.Description = "Pay again"
	for name, answer := range map[string]func(t *testing.T) error{
		"payment required": func(t *testing.T) error {
			return paymentError(t, mpp.MCPPaymentRequiredCode, 402, string(mpp.ErrorTypeVerificationFailed), &reoffered)
		},
		"verification failed 402": func(t *testing.T) error {
			return paymentError(t, mpp.MCPPaymentVerificationFailedCode, 402, string(mpp.ErrorTypeInvalidPayload), &reoffered)
		},
		"verification failed without httpStatus": func(t *testing.T) error {
			return paymentError(t, mpp.MCPPaymentVerificationFailedCode, 0, string(mpp.ErrorTypeVerificationFailed), &reoffered)
		},
	} {
		t.Run(name, func(t *testing.T) {
			server := &roundServer{}
			payer := &roundPayer{}
			srv := server.serve(func(_ int, challengeID string) error {
				if challengeID == "" {
					return paymentRequiredError(t, "", charge)
				}
				return answer(t)
			})
			session := connect(t, srv, mcpclient.Wrap(newClient(), roundConfig(payer)))

			_, err := callPremium(context.Background(), session, nil)
			var wireErr *jsonrpc.Error
			require.ErrorAs(t, err, &wireErr, "the last payment-required answer is the result")
			assert.Equal(t, []string{charge.ID}, payer.paidIDs(), "exactly one signing")
			calls := server.received()
			require.Len(t, calls, 4, "one unpaid call and at most three Credentials")
			for _, call := range calls[1:] {
				assert.Equal(t, charge.ID, call.challengeID, "the same Credential is resent")
			}
		})
	}
}

// Only the Challenge the last call answered is resent: one answered in an
// earlier round and offered again gets a new Credential, as in mppx.
func TestMCPNewChallengeRound_EarlierChallengeGetsNewCredential(t *testing.T) {
	first, second := roundChallenge(realm, "10000", "1"), roundChallenge(realm, "10000", "2")
	server := &roundServer{}
	payer := &roundPayer{}
	srv := server.serve(func(n int, _ string) error {
		switch n {
		case 0:
			return paymentRequiredError(t, "", first)
		case 1:
			return paymentRequiredError(t, string(mpp.ErrorTypeVerificationFailed), second)
		case 2:
			return paymentRequiredError(t, string(mpp.ErrorTypeVerificationFailed), first)
		}
		return nil
	})
	session := connect(t, srv, mcpclient.Wrap(newClient(), roundConfig(payer)))

	result, err := callPremium(context.Background(), session, nil)
	require.NoError(t, err)
	require.False(t, result.IsError)
	assert.Equal(t, []string{first.ID, second.ID, first.ID}, payer.paidIDs())
	assert.Len(t, server.received(), 4)
}

// Only a payment-required answer starts a new round: -32042, or -32043 whose
// httpStatus is 402 or absent. Any other error is the call's final answer,
// even when it offers a new Challenge.
func TestMCPNewChallengeRound_NonPaymentRequiredIsFinal(t *testing.T) {
	charge, next := roundChallenge(realm, "10000", "1"), roundChallenge(realm, "10000", "2")
	for name, final := range map[string]func(t *testing.T) error{
		"verification failed 400": func(t *testing.T) error {
			return paymentError(t, mpp.MCPPaymentVerificationFailedCode, 400, string(mpp.ErrorTypeVerificationFailed), next)
		},
		"verification failed 410": func(t *testing.T) error {
			return paymentError(t, mpp.MCPPaymentVerificationFailedCode, 410, string(mpp.ErrorTypeVerificationFailed), next)
		},
		"invalid params": func(t *testing.T) error {
			return paymentError(t, mpp.MCPInvalidParamsCode, 402, string(mpp.ErrorTypeInvalidPayload), next)
		},
		"internal error": func(t *testing.T) error {
			return paymentError(t, mpp.MCPInternalErrorCode, 500, string(mpp.ErrorTypeVerificationFailed), next)
		},
	} {
		t.Run(name, func(t *testing.T) {
			server := &roundServer{}
			payer := &roundPayer{}
			srv := server.serve(func(_ int, challengeID string) error {
				if challengeID == "" {
					return paymentRequiredError(t, "", charge)
				}
				return final(t)
			})
			session := connect(t, srv, mcpclient.Wrap(newClient(), roundConfig(payer)))

			_, err := callPremium(context.Background(), session, nil)
			var wireErr *jsonrpc.Error
			require.ErrorAs(t, err, &wireErr, "the server's error is returned")
			assert.Equal(t, []string{charge.ID}, payer.paidIDs())
			assert.Len(t, server.received(), 2)
		})
	}
}
