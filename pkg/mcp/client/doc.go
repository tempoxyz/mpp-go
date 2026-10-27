// Package client pays for MCP tool calls made with the official MCP Go SDK
// (github.com/modelcontextprotocol/go-sdk), matching mppx's McpClient.wrap.
//
// Wrap installs sending middleware on an *mcp.Client, so every session it
// connects, and every CallTool on them, handles payment challenges whether
// they arrive as payment-required JSON-RPC errors (-32042 or -32043) or as
// tool results carrying _meta["org.paymentauth/payment-required"]. The
// client pays with a configured method and retries the call with the
// Credential in _meta["org.paymentauth/credential"]; Receipt reads the
// receipt from the result:
//
//	c := mcp.NewClient(&mcp.Implementation{Name: "my-client", Version: "1.0.0"}, nil)
//	mcpclient.Wrap(c, mcpclient.Config{Methods: []client.Method{method}})
//	session, err := c.Connect(ctx, transport, nil)
//	...
//	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "premium_tool"})
//	receipt, err := mcpclient.Receipt(result)
//
// Middleware returns the same handling as sending middleware to install
// yourself, without Wrap's per-client registry:
//
//	c.AddSendingMiddleware(mcpclient.Middleware(mcpclient.Config{Methods: methods}))
//
// Zero-amount Tempo proofs additionally require Config.ValidateTempoProofSession.
// Register sessions only after connecting through a locally trusted transport;
// check the session pointer and its allowed realm in that callback. A peer's
// advertised server name or challenge realm is not an authenticated identity.
// A per-call WithOnPaymentRequired override does not bypass this check.
package client
