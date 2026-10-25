package jsonrpc_test

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tempoxyz/mpp-go/pkg/mcp/jsonrpc"
	"github.com/tempoxyz/mpp-go/pkg/mpp"
	"github.com/tempoxyz/mpp-go/pkg/server"
)

const (
	realm     = "example.test"
	secretKey = "test-secret-key-test-secret-key-32"
)

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

// decodeRequest parses a request from the wire, as a server would.
func decodeRequest(t *testing.T, raw string) *jsonrpc.Request {
	t.Helper()
	var req jsonrpc.Request
	require.NoError(t, json.Unmarshal([]byte(raw), &req))
	return &req
}

// paidRequest builds a tools/call request carrying credential on the wire.
func paidRequest(t *testing.T, id string, credential any) *jsonrpc.Request {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      json.RawMessage(id),
		"method":  "tools/call",
		"params": map[string]any{
			"name":  "premium",
			"_meta": map[string]any{mpp.MCPCredentialMetaKey: credential},
		},
	})
	require.NoError(t, err)
	return decodeRequest(t, string(raw))
}

func paymentData(t *testing.T, res *jsonrpc.Result) *mpp.MCPPaymentRequiredData {
	t.Helper()
	require.True(t, res.IsChallenge())
	require.NotNil(t, res.Challenge.Error)
	data, ok := res.Challenge.Error.PaymentRequired()
	require.True(t, ok)
	return data
}

func TestChargePreservesRequestIDAndReceiptMetadata(t *testing.T) {
	method := &testMethod{}
	payment, err := server.New(method, realm, secretKey)
	require.NoError(t, err)
	params := server.ChargeParams{Amount: "1", Currency: "A"}

	offered, err := jsonrpc.Charge(context.Background(), payment, decodeRequest(t, `{"jsonrpc":"2.0","id":42,"method":"tools/call","params":{"name":"premium"}}`), params)
	require.NoError(t, err)
	assert.Equal(t, "2.0", offered.Challenge.JSONRPC)
	assert.JSONEq(t, "42", string(offered.Challenge.ID))
	assert.EqualValues(t, mpp.MCPPaymentRequiredCode, offered.Challenge.Error.Code)
	data := paymentData(t, offered)
	assert.Equal(t, 402, data.HTTPStatus)
	require.Len(t, data.Challenges, 1)
	assert.Equal(t, string(mpp.ErrorTypePaymentRequired), data.Problem["type"])
	assert.Empty(t, method.recorded())

	credential := data.Challenges[0].NewCredential(map[string]any{"token": "valid"})
	paid, err := jsonrpc.Charge(context.Background(), payment, paidRequest(t, `"call-43"`, credential), params)
	require.NoError(t, err)
	require.False(t, paid.IsChallenge())
	assert.Equal(t, "A", paid.Receipt.Reference)

	response := paid.WithReceipt(&jsonrpc.Response{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`"call-43"`),
		Result:  map[string]any{"content": []any{}, "_meta": map[string]any{"trace": "kept"}},
	})
	encoded, err := json.Marshal(response)
	require.NoError(t, err)
	var wire struct {
		ID     string `json:"id"`
		Result struct {
			Meta map[string]json.RawMessage `json:"_meta"`
		} `json:"result"`
	}
	require.NoError(t, json.Unmarshal(encoded, &wire))
	assert.Equal(t, "call-43", wire.ID)
	assert.JSONEq(t, `"kept"`, string(wire.Result.Meta["trace"]))
	var receipt mpp.MCPReceipt
	require.NoError(t, json.Unmarshal(wire.Result.Meta[mpp.MCPReceiptMetaKey], &receipt))
	assert.Equal(t, credential.Challenge.ID, receipt.ChallengeID)
	assert.Equal(t, "A", receipt.Reference)
	assert.Equal(t, []string{"A"}, method.recorded())

	// Error responses pass through unchanged.
	failure := &jsonrpc.Response{JSONRPC: "2.0", ID: json.RawMessage("43"), Error: &jsonrpc.Error{Code: -32000, Message: "tool failed"}}
	assert.Equal(t, failure, paid.WithReceipt(failure))
}

func TestChargeRejectionsAndMalformedCredentials(t *testing.T) {
	method := &testMethod{}
	payment, err := server.New(method, realm, secretKey)
	require.NoError(t, err)
	params := server.ChargeParams{Amount: "1", Currency: "A"}

	offered, err := jsonrpc.Charge(context.Background(), payment, decodeRequest(t, `{"jsonrpc":"2.0","id":1,"method":"tools/call"}`), params)
	require.NoError(t, err)
	challenge := paymentData(t, offered).Challenges[0]

	declined, err := jsonrpc.Charge(context.Background(), payment, paidRequest(t, "2", challenge.NewCredential(map[string]any{"token": "declined"})), params)
	require.NoError(t, err)
	assert.EqualValues(t, mpp.MCPPaymentVerificationFailedCode, declined.Challenge.Error.Code)
	assert.JSONEq(t, "2", string(declined.Challenge.ID))
	data := paymentData(t, declined)
	assert.Equal(t, string(mpp.ErrorTypeVerificationFailed), data.Problem["type"])

	for _, value := range []any{"invalid", map[string]any{"challenge": map[string]any{}}, nil} {
		malformed, err := jsonrpc.Charge(context.Background(), payment, paidRequest(t, "3", value), params)
		require.NoError(t, err)
		assert.EqualValues(t, mpp.MCPInvalidParamsCode, malformed.Challenge.Error.Code, "value %v", value)
		assert.Equal(t, string(mpp.ErrorTypeMalformedCredential), paymentData(t, malformed).Problem["type"])
	}
	assert.Equal(t, []string{"A"}, method.recorded())
}

func TestRequestKeepsLargeIntegersExact(t *testing.T) {
	req := decodeRequest(t, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"_meta":{"n":9007199254740993}}}`)
	encoded, err := json.Marshal(req.Meta())
	require.NoError(t, err)
	assert.JSONEq(t, `{"n":9007199254740993}`, string(encoded))
	assert.Contains(t, string(encoded), "9007199254740993")
}

func TestComposeSettlesOnlyTheChosenOffer(t *testing.T) {
	method := &testMethod{}
	payment, err := server.New(method, realm, secretKey)
	require.NoError(t, err)
	configs := []server.ComposeConfig{
		{Mpp: payment, Params: server.ChargeParams{Amount: "1", Currency: "A"}},
		{Mpp: payment, Params: server.ChargeParams{Amount: "1", Currency: "B"}},
	}

	offered, err := jsonrpc.Compose(context.Background(), decodeRequest(t, `{"jsonrpc":"2.0","id":42,"method":"tools/call"}`), configs...)
	require.NoError(t, err)
	data := paymentData(t, offered)
	require.Len(t, data.Challenges, 2)

	credential := data.Challenges[1].NewCredential(map[string]any{"token": "valid"})
	paid, err := jsonrpc.Compose(context.Background(), paidRequest(t, "43", credential), configs...)
	require.NoError(t, err)
	require.False(t, paid.IsChallenge())
	assert.Equal(t, "B", paid.Receipt.Reference)
	assert.Equal(t, []string{"B"}, method.recorded())

	// Like mppx, a malformed Credential is answered with the first offer only.
	malformed, err := jsonrpc.Compose(context.Background(), paidRequest(t, "44", "invalid"), configs...)
	require.NoError(t, err)
	assert.EqualValues(t, mpp.MCPInvalidParamsCode, malformed.Challenge.Error.Code)
	offers := paymentData(t, malformed).Challenges
	require.Len(t, offers, 1)
	assert.Equal(t, "A", offers[0].Request["currency"])
}
