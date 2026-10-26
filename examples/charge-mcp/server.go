package main

import (
	"context"
	"net/http"
	"net/http/httptest"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/tempoxyz/mpp-go/examples/internal/devnet"
	mcpserver "github.com/tempoxyz/mpp-go/pkg/mcp/server"
	"github.com/tempoxyz/mpp-go/pkg/server"
	"github.com/tempoxyz/mpp-go/pkg/tempo"
	charge "github.com/tempoxyz/mpp-go/pkg/tempo/server"
)

type exampleServer struct {
	url    string
	server *httptest.Server
}

// startServer serves an MCP server over Streamable HTTP with a free "ping"
// tool and a paid "fortune" tool.
func startServer(rpcURL string, chainID int64) (*exampleServer, error) {
	method, err := charge.New(charge.Config{
		// Development only: replicas need a shared persistent store.
		Store:      tempo.NewMemoryStore(),
		RPCURL:     rpcURL,
		ChainID:    chainID,
		Currencies: []string{devnet.Currency},
		Recipient:  devnet.Recipient,
	})
	if err != nil {
		return nil, err
	}
	payment, err := server.New(method, devnet.Realm, "example-secret-minimum-32-byte-secret")
	if err != nil {
		return nil, err
	}

	srv := mcp.NewServer(&mcp.Implementation{Name: "fortune", Version: "1.0.0"}, nil)
	schema := map[string]any{"type": "object"}

	srv.AddTool(&mcp.Tool{Name: "ping", Description: "Free", InputSchema: schema},
		func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "pong"}}}, nil
		})

	srv.AddTool(&mcp.Tool{Name: "fortune", Description: "Costs 0.50", InputSchema: schema},
		func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			res, err := mcpserver.Charge(ctx, payment, req, server.ChargeParams{
				Amount:      "0.50",
				Description: "MCP fortune",
			})
			if err != nil {
				return nil, err
			}
			if res.IsChallenge() {
				// The SDK sends the payment error (-32042) to the client as is.
				return nil, res.Challenge
			}
			return res.WithReceipt(&mcp.CallToolResult{
				Content: []mcp.Content{&mcp.TextContent{Text: "A golden egg of opportunity falls into your lap this month."}},
			}), nil
		})

	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil)
	httpServer := httptest.NewServer(handler)
	return &exampleServer{url: httpServer.URL, server: httpServer}, nil
}

func (s *exampleServer) Close() {
	s.server.Close()
}
