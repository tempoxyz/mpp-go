package mcp_test

import (
	"context"
	"encoding/json"
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

// unsupportedHeaderChallenge is a test/charge Challenge that selects a
// credential header other than Payment-Authorization.
const unsupportedHeaderChallenge = `{"id":"bad-header","realm":"example.test","method":"test","intent":"charge","request":{"amount":"1","currency":"B"},"header":"Authorization"}`

// withUnsupportedHeaderChallenge returns payment-required data that offers
// unsupportedHeaderChallenge ahead of the Challenges in data.
func withUnsupportedHeaderChallenge(t *testing.T, data json.RawMessage) json.RawMessage {
	t.Helper()
	var fields map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(data, &fields))
	var challenges []json.RawMessage
	require.NoError(t, json.Unmarshal(fields["challenges"], &challenges))
	challenges = append([]json.RawMessage{json.RawMessage(unsupportedHeaderChallenge)}, challenges...)
	encoded, err := json.Marshal(challenges)
	require.NoError(t, err)
	fields["challenges"] = encoded
	out, err := json.Marshal(fields)
	require.NoError(t, err)
	return out
}

func TestClientSkipsChallengeWithUnsupportedCredentialHeader(t *testing.T) {
	method := &testMethod{}
	payment := newPayment(t, method)
	srv := newServer(func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		res, err := mcpserver.Charge(ctx, payment, req, server.ChargeParams{Amount: "1", Currency: "A"})
		if err != nil {
			return nil, err
		}
		if res.IsChallenge() {
			return nil, &jsonrpc.Error{Code: res.Challenge.Code, Message: res.Challenge.Message, Data: withUnsupportedHeaderChallenge(t, res.Challenge.Data)}
		}
		return res.WithReceipt(nil), nil
	})
	payer := &testClientMethod{token: "valid"}
	session := connect(t, srv, mcpclient.Wrap(newClient(), mcpclient.Config{Methods: []client.Method{payer}}))

	_, err := callPremium(context.Background(), session, nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"A"}, payer.credentials(), "only the valid Challenge is paid")
	assert.Equal(t, []string{"A"}, method.recorded())
}

func TestClientPaysNoChallengeWithOnlyUnsupportedCredentialHeader(t *testing.T) {
	data := json.RawMessage(`{"httpStatus":402,"challenges":[` + unsupportedHeaderChallenge + `]}`)
	for name, handler := range map[string]mcp.ToolHandler{
		"error": func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return nil, &jsonrpc.Error{Code: mpp.MCPPaymentRequiredCode, Message: "Payment Required", Data: data}
		},
		"result meta": func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{
				Content: []mcp.Content{&mcp.TextContent{Text: "payment required"}},
				IsError: true,
				Meta:    mcp.Meta{mpp.MCPPaymentRequiredMetaKey: data},
			}, nil
		},
	} {
		t.Run(name, func(t *testing.T) {
			payer := &testClientMethod{token: "valid"}
			session := connect(t, newServer(handler), mcpclient.Wrap(newClient(), mcpclient.Config{Methods: []client.Method{payer}}))

			result, err := callPremium(context.Background(), session, nil)
			assert.Nil(t, result)
			require.ErrorIs(t, err, client.ErrNoMethodFound)
			require.ErrorIs(t, err, mpp.ErrUnsupportedCredentialHeader)
			assert.Empty(t, payer.credentials())
		})
	}

	var wireErr error = &jsonrpc.Error{Code: mpp.MCPPaymentRequiredCode, Message: "Payment Required", Data: data}
	assert.True(t, mcpclient.IsPaymentRequiredError(wireErr))
}
