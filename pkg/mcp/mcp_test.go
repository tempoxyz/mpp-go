package mcp_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tempoxyz/mpp-go/pkg/client"
	mcpclient "github.com/tempoxyz/mpp-go/pkg/mcp/client"
	mcpserver "github.com/tempoxyz/mpp-go/pkg/mcp/server"
	"github.com/tempoxyz/mpp-go/pkg/mpp"
	"github.com/tempoxyz/mpp-go/pkg/server"
)

const (
	realm     = "example.test"
	secretKey = "test-secret-key-test-secret-key-32"
)

// testMethod settles a "test" charge when the Credential's token is "valid"
// and records the currency of every verification attempt.
type testMethod struct {
	mu       sync.Mutex
	payments []string
}

func (m *testMethod) Name() string { return "test" }

func (m *testMethod) Intents() map[string]server.Intent {
	intent, err := server.NewIntent("charge", server.IntentHooks{
		Verify: func(_ context.Context, credential *mpp.Credential, request map[string]any) (*mpp.Receipt, error) {
			currency, _ := request["currency"].(string)
			m.mu.Lock()
			m.payments = append(m.payments, currency)
			m.mu.Unlock()
			if credential.Payload["token"] != "valid" {
				return nil, mpp.ErrVerificationFailed("Payment was declined")
			}
			return mpp.Success("test", currency), nil
		},
	})
	if err != nil {
		panic(err)
	}
	return map[string]server.Intent{"charge": intent}
}

func (m *testMethod) recorded() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.payments...)
}

// testClientMethod pays "test" charges with a fixed token.
type testClientMethod struct {
	token   string
	mu      sync.Mutex
	created []string
}

func (m *testClientMethod) Name() string   { return "test" }
func (m *testClientMethod) Intent() string { return "charge" }

func (m *testClientMethod) CreateCredential(_ context.Context, challenge *mpp.Challenge) (*mpp.Credential, error) {
	m.mu.Lock()
	m.created = append(m.created, challenge.Request["currency"].(string))
	m.mu.Unlock()
	return challenge.NewCredential(map[string]any{"token": m.token}), nil
}

func (m *testClientMethod) credentials() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.created...)
}

func newPayment(t *testing.T, method *testMethod) *server.Mpp {
	t.Helper()
	payment, err := server.New(method, realm, secretKey)
	require.NoError(t, err)
	return payment
}

func newServer(handler mcp.ToolHandler) *mcp.Server {
	srv := mcp.NewServer(&mcp.Implementation{Name: "paid", Version: "1.0.0"}, nil)
	srv.AddTool(&mcp.Tool{Name: "premium", InputSchema: map[string]any{"type": "object"}}, handler)
	return srv
}

// chargeHandler charges 1 in currency A, the way mppx's docs show it.
func chargeHandler(payment *server.Mpp) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		res, err := mcpserver.Charge(ctx, payment, req, server.ChargeParams{Amount: "1", Currency: "A"})
		if err != nil {
			return nil, err
		}
		if res.IsChallenge() {
			return nil, res.Challenge
		}
		return res.WithReceipt(&mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: "paid"}},
			Meta:    mcp.Meta{"trace": "kept"},
		}), nil
	}
}

func connect(t *testing.T, srv *mcp.Server, c *mcp.Client) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	serverSession, err := srv.Connect(ctx, serverTransport, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = serverSession.Close() })
	session, err := c.Connect(ctx, clientTransport, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = session.Close() })
	return session
}

func newClient() *mcp.Client {
	return mcp.NewClient(&mcp.Implementation{Name: "payer", Version: "1.0.0"}, nil)
}

func paymentData(t *testing.T, err error) (*jsonrpc.Error, mpp.MCPPaymentRequiredData) {
	t.Helper()
	var wireErr *jsonrpc.Error
	require.ErrorAs(t, err, &wireErr)
	var data mpp.MCPPaymentRequiredData
	require.NoError(t, json.Unmarshal(wireErr.Data, &data))
	return wireErr, data
}

func callPremium(ctx context.Context, session *mcp.ClientSession, meta mcp.Meta) (*mcp.CallToolResult, error) {
	return session.CallTool(ctx, &mcp.CallToolParams{Name: "premium", Meta: meta})
}

func TestChargeIssuesChallengeError(t *testing.T) {
	method := &testMethod{}
	session := connect(t, newServer(chargeHandler(newPayment(t, method))), newClient())

	_, err := callPremium(context.Background(), session, nil)

	wireErr, data := paymentData(t, err)
	assert.EqualValues(t, mpp.MCPPaymentRequiredCode, wireErr.Code)
	assert.Equal(t, "Payment is required to access this resource", wireErr.Message)
	assert.Equal(t, 402, data.HTTPStatus)
	require.Len(t, data.Challenges, 1)
	assert.Equal(t, realm, data.Challenges[0].Realm)
	assert.Equal(t, "A", data.Challenges[0].Request["currency"])
	assert.Equal(t, string(mpp.ErrorTypePaymentRequired), data.Problem["type"])
	assert.Equal(t, data.Challenges[0].ID, data.Problem["challengeId"])
	assert.True(t, mcpclient.IsPaymentRequiredError(err))
	assert.Empty(t, method.recorded())
}

func TestWrappedClientPaysAndReadsReceipt(t *testing.T) {
	method := &testMethod{}
	payer := &testClientMethod{token: "valid"}
	c := mcpclient.Wrap(newClient(), mcpclient.Config{Methods: []client.Method{payer}})
	session := connect(t, newServer(chargeHandler(newPayment(t, method))), c)

	result, err := callPremium(context.Background(), session, mcp.Meta{"trace": "sent"})
	require.NoError(t, err)

	require.Len(t, result.Content, 1)
	assert.Equal(t, "paid", result.Content[0].(*mcp.TextContent).Text)
	assert.Equal(t, "kept", result.Meta["trace"])
	receipt, err := mcpclient.Receipt(result)
	require.NoError(t, err)
	require.NotNil(t, receipt)
	assert.Equal(t, "success", receipt.Status)
	assert.Equal(t, "test", receipt.Method)
	assert.Equal(t, "A", receipt.Reference)
	assert.NotEmpty(t, receipt.ChallengeID)
	assert.Equal(t, []string{"A"}, method.recorded())
	assert.Equal(t, []string{"A"}, payer.credentials())
}

func TestTypedToolHandlerReturnsChallengeError(t *testing.T) {
	method := &testMethod{}
	payment := newPayment(t, method)
	srv := mcp.NewServer(&mcp.Implementation{Name: "paid", Version: "1.0.0"}, nil)
	type input struct {
		Query string `json:"query"`
	}
	mcp.AddTool(srv, &mcp.Tool{Name: "premium"}, func(ctx context.Context, req *mcp.CallToolRequest, in input) (*mcp.CallToolResult, any, error) {
		res, err := mcpserver.Charge(ctx, payment, req, server.ChargeParams{Amount: "1", Currency: "A"})
		if err != nil {
			return nil, nil, err
		}
		if res.IsChallenge() {
			return nil, nil, res.Challenge
		}
		return res.WithReceipt(&mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: in.Query}}}), nil, nil
	})
	payer := &testClientMethod{token: "valid"}
	session := connect(t, srv, mcpclient.Wrap(newClient(), mcpclient.Config{Methods: []client.Method{payer}}))

	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "premium",
		Arguments: map[string]any{"query": "hello"},
	})
	require.NoError(t, err)
	assert.Equal(t, "hello", result.Content[0].(*mcp.TextContent).Text)
	receipt, err := mcpclient.Receipt(result)
	require.NoError(t, err)
	require.NotNil(t, receipt)
	assert.Equal(t, []string{"A"}, method.recorded())
}

func TestRejectedCredentialKeepsVerificationFailedCode(t *testing.T) {
	method := &testMethod{}
	payer := &testClientMethod{token: "declined"}
	c := mcpclient.Wrap(newClient(), mcpclient.Config{Methods: []client.Method{payer}})
	session := connect(t, newServer(chargeHandler(newPayment(t, method))), c)

	_, err := callPremium(context.Background(), session, nil)

	wireErr, data := paymentData(t, err)
	assert.EqualValues(t, mpp.MCPPaymentVerificationFailedCode, wireErr.Code)
	assert.Equal(t, 402, data.HTTPStatus)
	require.Len(t, data.Challenges, 1)
	require.NotNil(t, data.Problem)
	assert.EqualValues(t, 402, data.Problem["status"])
	assert.Equal(t, string(mpp.ErrorTypeVerificationFailed), data.Problem["type"])
	assert.Equal(t, data.Challenges[0].ID, data.Problem["challengeId"])
	// The client retries at most three times and, like the HTTP Transport,
	// resends the Credential it created instead of paying again.
	assert.Equal(t, []string{"A", "A", "A"}, method.recorded())
	assert.Equal(t, []string{"A"}, payer.credentials())
}

func TestMalformedCredentialUsesInvalidParamsCode(t *testing.T) {
	method := &testMethod{}
	session := connect(t, newServer(chargeHandler(newPayment(t, method))), newClient())

	for _, value := range []any{"invalid", map[string]any{"challenge": map[string]any{}}, nil} {
		_, err := callPremium(context.Background(), session, mcp.Meta{mpp.MCPCredentialMetaKey: value})

		wireErr, data := paymentData(t, err)
		assert.EqualValues(t, mpp.MCPInvalidParamsCode, wireErr.Code, "value %v", value)
		require.Len(t, data.Challenges, 1)
		assert.Equal(t, string(mpp.ErrorTypeMalformedCredential), data.Problem["type"])
	}
	assert.Empty(t, method.recorded())
}

func composeHandler(configs ...server.ComposeConfig) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		res, err := mcpserver.Compose(ctx, req, configs...)
		if err != nil {
			return nil, err
		}
		if res.IsChallenge() {
			return nil, res.Challenge
		}
		return res.WithReceipt(&mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "paid"}}}), nil
	}
}

func TestComposeOffersAllAndSettlesOnlyChosenOffer(t *testing.T) {
	method := &testMethod{}
	payment := newPayment(t, method)
	srv := newServer(composeHandler(
		server.ComposeConfig{Mpp: payment, Params: server.ChargeParams{Amount: "1", Currency: "A"}},
		server.ComposeConfig{Mpp: payment, Params: server.ChargeParams{Amount: "1", Currency: "B"}},
	))

	_, err := callPremium(context.Background(), connect(t, srv, newClient()), nil)
	_, data := paymentData(t, err)
	require.Len(t, data.Challenges, 2)
	assert.Equal(t, "A", data.Challenges[0].Request["currency"])
	assert.Equal(t, "B", data.Challenges[1].Request["currency"])

	payer := &testClientMethod{token: "valid"}
	c := mcpclient.Wrap(newClient(), mcpclient.Config{
		Methods: []client.Method{payer},
		OrderChallenges: func(_ context.Context, candidates []client.ChallengeCandidate) ([]client.ChallengeCandidate, error) {
			return candidates[1:], nil
		},
	})
	result, err := callPremium(context.Background(), connect(t, srv, c), nil)
	require.NoError(t, err)
	receipt, err := mcpclient.Receipt(result)
	require.NoError(t, err)
	assert.Equal(t, "B", receipt.Reference)
	assert.Equal(t, []string{"B"}, method.recorded())
}

func TestComposeRejectionAnswersWithTheEchoedOffer(t *testing.T) {
	method := &testMethod{}
	payment := newPayment(t, method)
	srv := newServer(composeHandler(
		server.ComposeConfig{Mpp: payment, Params: server.ChargeParams{Amount: "1", Currency: "A"}},
		server.ComposeConfig{Mpp: payment, Params: server.ChargeParams{Amount: "1", Currency: "B"}},
	))
	session := connect(t, srv, newClient())
	_, err := callPremium(context.Background(), session, nil)
	_, offered := paymentData(t, err)

	credential := offered.Challenges[1].NewCredential(map[string]any{"token": "declined"})
	_, err = callPremium(context.Background(), session, mcp.Meta{mpp.MCPCredentialMetaKey: credential})

	wireErr, data := paymentData(t, err)
	assert.EqualValues(t, mpp.MCPPaymentVerificationFailedCode, wireErr.Code)
	require.Len(t, data.Challenges, 1)
	assert.Equal(t, "B", data.Challenges[0].Request["currency"])
	assert.Equal(t, []string{"B"}, method.recorded())
}

func TestClientPaysChallengeInToolResultMeta(t *testing.T) {
	method := &testMethod{}
	payment := newPayment(t, method)
	srv := newServer(func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		res, err := mcpserver.Charge(ctx, payment, req, server.ChargeParams{Amount: "1", Currency: "A"})
		if err != nil {
			return nil, err
		}
		if res.IsChallenge() {
			// Some servers report payment-required as a tool result.
			return &mcp.CallToolResult{
				Content: []mcp.Content{&mcp.TextContent{Text: "payment required"}},
				IsError: true,
				Meta:    mcp.Meta{mpp.MCPPaymentRequiredMetaKey: json.RawMessage(res.Challenge.Data)},
			}, nil
		}
		return res.WithReceipt(nil), nil
	})
	payer := &testClientMethod{token: "valid"}
	session := connect(t, srv, mcpclient.Wrap(newClient(), mcpclient.Config{Methods: []client.Method{payer}}))

	result, err := callPremium(context.Background(), session, nil)
	require.NoError(t, err)
	assert.False(t, result.IsError)
	receipt, err := mcpclient.Receipt(result)
	require.NoError(t, err)
	require.NotNil(t, receipt)
	assert.Equal(t, []string{"A"}, method.recorded())
}

func TestApprovalHook(t *testing.T) {
	method := &testMethod{}
	payer := &testClientMethod{token: "valid"}
	var asked []string
	c := mcpclient.Wrap(newClient(), mcpclient.Config{
		Methods: []client.Method{payer},
		OnPaymentRequired: func(_ context.Context, challenge *mpp.Challenge) (bool, error) {
			asked = append(asked, challenge.ID)
			return false, nil
		},
	})
	session := connect(t, newServer(chargeHandler(newPayment(t, method))), c)

	_, err := callPremium(context.Background(), session, nil)
	require.ErrorIs(t, err, mcpclient.ErrPaymentDeclined)
	assert.Len(t, asked, 1)
	assert.Empty(t, payer.credentials())

	// A per-call nil hook pays without asking.
	ctx := mcpclient.WithOnPaymentRequired(context.Background(), nil)
	_, err = callPremium(ctx, session, nil)
	require.NoError(t, err)
	assert.Len(t, asked, 1)

	hookErr := errors.New("wallet locked")
	ctx = mcpclient.WithOnPaymentRequired(context.Background(), func(context.Context, *mpp.Challenge) (bool, error) {
		return false, hookErr
	})
	_, err = callPremium(ctx, session, nil)
	require.ErrorIs(t, err, hookErr)
	assert.Equal(t, []string{"A"}, method.recorded())
}

func TestNoCompatibleMethod(t *testing.T) {
	method := &testMethod{}
	other := &otherClientMethod{}
	c := mcpclient.Wrap(newClient(), mcpclient.Config{Methods: []client.Method{other}})
	session := connect(t, newServer(chargeHandler(newPayment(t, method))), c)

	_, err := callPremium(context.Background(), session, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "server offers: test.charge; client has: other.charge")
	assert.True(t, mcpclient.IsPaymentRequiredError(err), "the server's error stays reachable")
}

func TestWrapAgainReplacesConfig(t *testing.T) {
	method := &testMethod{}
	declined := &testClientMethod{token: "declined"}
	valid := &testClientMethod{token: "valid"}
	c := newClient()
	mcpclient.Wrap(c, mcpclient.Config{Methods: []client.Method{declined}})
	session := connect(t, newServer(chargeHandler(newPayment(t, method))), c)
	mcpclient.Wrap(c, mcpclient.Config{Methods: []client.Method{valid}})

	_, err := callPremium(context.Background(), session, nil)
	require.NoError(t, err)
	assert.Empty(t, declined.credentials())
	assert.Equal(t, []string{"A"}, valid.credentials())
	assert.Equal(t, []string{"A"}, method.recorded())
}

func TestUnpaidToolsAreUntouched(t *testing.T) {
	srv := newServer(func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return nil, &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: "bad input"}
	})
	payer := &testClientMethod{token: "valid"}
	session := connect(t, srv, mcpclient.Wrap(newClient(), mcpclient.Config{Methods: []client.Method{payer}}))

	_, err := callPremium(context.Background(), session, nil)
	var wireErr *jsonrpc.Error
	require.ErrorAs(t, err, &wireErr)
	assert.EqualValues(t, jsonrpc.CodeInvalidParams, wireErr.Code)
	assert.Empty(t, payer.credentials())
}

// tokenSequenceMethod pays with the next token on each credential it creates.
type tokenSequenceMethod struct {
	testClientMethod
	tokens []string
}

func (m *tokenSequenceMethod) CreateCredential(ctx context.Context, challenge *mpp.Challenge) (*mpp.Credential, error) {
	m.mu.Lock()
	token := m.tokens[len(m.created)]
	m.mu.Unlock()
	m.token = token
	return m.testClientMethod.CreateCredential(ctx, challenge)
}

// invalidPayloadMethod rejects "stale" payloads as invalid and settles "valid" ones.
type invalidPayloadMethod struct{ testMethod }

func (m *invalidPayloadMethod) Intents() map[string]server.Intent {
	intent, err := server.NewIntent("charge", server.IntentHooks{
		Verify: func(_ context.Context, credential *mpp.Credential, request map[string]any) (*mpp.Receipt, error) {
			if credential.Payload["token"] == "stale" {
				return nil, mpp.ErrInvalidPayload("stale transaction")
			}
			return mpp.Success("test", request["currency"].(string)), nil
		},
	})
	if err != nil {
		panic(err)
	}
	return map[string]server.Intent{"charge": intent}
}

func TestInvalidPayloadRecreatesCredential(t *testing.T) {
	payment, err := server.New(&invalidPayloadMethod{}, realm, secretKey)
	require.NoError(t, err)
	payer := &tokenSequenceMethod{tokens: []string{"stale", "valid"}}
	session := connect(t, newServer(chargeHandler(payment)), mcpclient.Wrap(newClient(), mcpclient.Config{Methods: []client.Method{payer}}))

	result, err := callPremium(context.Background(), session, nil)
	require.NoError(t, err)
	receipt, err := mcpclient.Receipt(result)
	require.NoError(t, err)
	require.NotNil(t, receipt)
	assert.Len(t, payer.credentials(), 2)
}

// zeroTempoChallengeServer offers a zero-amount tempo/charge Challenge for
// challengeRealm on every call, like a server relaying another realm's proof.
func zeroTempoChallengeServer(t *testing.T, challengeRealm string) *mcp.Server {
	t.Helper()
	challenge := mpp.NewChallenge(secretKey, challengeRealm, "tempo", "charge", map[string]any{"amount": "0", "currency": "0x20c0"}, mpp.WithExpires(mpp.Expires.Minutes(5)))
	data, err := json.Marshal(mpp.MCPPaymentRequiredData{HTTPStatus: 402, Challenges: []*mpp.Challenge{challenge}})
	require.NoError(t, err)
	return newServer(func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return nil, &jsonrpc.Error{Code: mpp.MCPPaymentRequiredCode, Message: "Payment Required", Data: data}
	})
}

type tempoProofMethod struct{ testClientMethod }

func (m *tempoProofMethod) Name() string { return "tempo" }

func TestZeroAmountTempoProofRequiresTrustedRealm(t *testing.T) {
	refused := &tempoProofMethod{}
	session := connect(t, zeroTempoChallengeServer(t, "victim.example"), mcpclient.Wrap(newClient(), mcpclient.Config{Methods: []client.Method{refused}}))
	_, err := callPremium(context.Background(), session, nil)
	require.ErrorContains(t, err, "refusing to sign Tempo proof")
	assert.Empty(t, refused.credentials())

	trusted := &tempoProofMethod{}
	session = connect(t, zeroTempoChallengeServer(t, "victim.example"), mcpclient.Wrap(newClient(), mcpclient.Config{
		Methods:                 []client.Method{trusted},
		TrustedTempoProofRealms: []string{"victim.example"},
	}))
	_, err = callPremium(context.Background(), session, nil)
	require.Error(t, err)
	// The proof is created once and resent on the server's repeated offer.
	assert.Len(t, trusted.credentials(), 1)
}

type otherClientMethod struct{}

func (otherClientMethod) Name() string   { return "other" }
func (otherClientMethod) Intent() string { return "charge" }
func (otherClientMethod) CreateCredential(context.Context, *mpp.Challenge) (*mpp.Credential, error) {
	return nil, errors.New("unexpected payment")
}
