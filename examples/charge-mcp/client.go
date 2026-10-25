package main

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/tempoxyz/mpp-go/examples/internal/devnet"
	"github.com/tempoxyz/mpp-go/pkg/client"
	mcpclient "github.com/tempoxyz/mpp-go/pkg/mcp/client"
	charge "github.com/tempoxyz/mpp-go/pkg/tempo/client"
)

type clientResult struct {
	Ping    string
	Fortune string
	Receipt string
}

// runClient connects a payment-aware MCP client to url, calls the free tool,
// then calls the paid tool, which pays its challenge and retries.
func runClient(ctx context.Context, url, rpcURL string, chainID int64) (*clientResult, error) {
	method, err := charge.New(charge.Config{
		PaymentPolicy: devnet.PaymentPolicy(chainID, devnet.Recipient),
		ChainID:       chainID,
		PrivateKey:    devnet.PayerPrivateKey,
		RPCURL:        rpcURL,
	})
	if err != nil {
		return nil, err
	}

	c := mcpclient.Wrap(
		mcp.NewClient(&mcp.Implementation{Name: "fortune-client", Version: "1.0.0"}, nil),
		mcpclient.Config{Methods: []client.Method{method}},
	)
	session, err := c.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: url}, nil)
	if err != nil {
		return nil, err
	}
	defer session.Close()

	ping, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "ping"})
	if err != nil {
		return nil, err
	}
	fortune, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "fortune"})
	if err != nil {
		return nil, err
	}
	receipt, err := mcpclient.Receipt(fortune)
	if err != nil {
		return nil, err
	}
	if receipt == nil {
		return nil, fmt.Errorf("fortune result has no payment receipt")
	}

	return &clientResult{
		Ping:    text(ping),
		Fortune: text(fortune),
		Receipt: receipt.Reference,
	}, nil
}

func text(result *mcp.CallToolResult) string {
	if len(result.Content) == 0 {
		return ""
	}
	if content, ok := result.Content[0].(*mcp.TextContent); ok {
		return content.Text
	}
	return ""
}
