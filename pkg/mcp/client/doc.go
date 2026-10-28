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
// A zero-amount Tempo proof authenticates the payer at the challenge realm,
// so the client binds it to the session that received the challenge: it
// signs one only for a session connected over mcp.StreamableClientTransport
// whose endpoint origin the payment method trusts for that realm (by default
// an HTTPS endpoint on the default port whose hostname is the realm; the
// Tempo method's Config.TrustedProofRealms adds others by origin). No
// configuration is needed, and a server cannot relay another server's
// challenge through a shared client. Sessions without an endpoint origin
// (stdio, in-memory) get proofs only when Config.ValidateTempoProofSession
// authorizes them.
package client
