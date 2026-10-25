//go:build integration

package tests

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"
	mppclient "github.com/tempoxyz/mpp-go/pkg/client"
	mcpclient "github.com/tempoxyz/mpp-go/pkg/mcp/client"
	mcpserver "github.com/tempoxyz/mpp-go/pkg/mcp/server"
	mppserver "github.com/tempoxyz/mpp-go/pkg/server"
	"github.com/tempoxyz/mpp-go/pkg/tempo"
)

// TestIntegrationMCPPaidToolCall pays an MCP tool call over Streamable HTTP
// with pkg/mcp/server and pkg/mcp/client against the live node.
func TestIntegrationMCPPaidToolCall(t *testing.T) {
	ctx := context.Background()
	payment := newLiveCharge(t, tempo.NewMemoryStore())

	srv := mcp.NewServer(&mcp.Implementation{Name: "paid-tools", Version: "1.0.0"}, nil)
	srv.AddTool(&mcp.Tool{Name: "fortune", InputSchema: map[string]any{"type": "object"}},
		func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			result, err := mcpserver.Charge(ctx, payment, req, mppserver.ChargeParams{Amount: "0.50", Description: "MCP integration"})
			if err != nil {
				return nil, err
			}
			if result.IsChallenge() {
				return nil, result.Challenge
			}
			return result.WithReceipt(&mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "paid fortune"}}}), nil
		})
	httpServer := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil))
	defer httpServer.Close()

	// A client without payment methods sees the payment-required error.
	plain, err := mcp.NewClient(&mcp.Implementation{Name: "plain", Version: "1.0.0"}, nil).
		Connect(ctx, &mcp.StreamableClientTransport{Endpoint: httpServer.URL}, nil)
	require.NoError(t, err)
	_, err = plain.CallTool(ctx, &mcp.CallToolParams{Name: "fortune"})
	require.Error(t, err)
	require.True(t, mcpclient.IsPaymentRequiredError(err), "error = %v", err)
	require.NoError(t, plain.Close())

	client := mcpclient.Wrap(
		mcp.NewClient(&mcp.Implementation{Name: "payer", Version: "1.0.0"}, nil),
		mcpclient.Config{Methods: []mppclient.Method{newSharedPayerMethod(t, "")}},
	)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: httpServer.URL}, nil)
	require.NoError(t, err)
	defer session.Close()

	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "fortune"})
	require.NoError(t, err)
	require.False(t, result.IsError)
	require.Len(t, result.Content, 1)
	text, ok := result.Content[0].(*mcp.TextContent)
	require.True(t, ok)
	require.Equal(t, "paid fortune", text.Text)

	receipt, err := mcpclient.Receipt(result)
	require.NoError(t, err)
	require.NotNil(t, receipt)
	require.Equal(t, "success", receipt.Status)
	require.NotEmpty(t, receipt.Reference)
	waitForReceipt(t, ctx, tempo.NewRPCClient(integrationRPCURL(t)), receipt.Reference)
}
