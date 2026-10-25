// Package server charges for MCP tools served with the official MCP Go SDK
// (github.com/modelcontextprotocol/go-sdk), matching the mppx/mcp/server
// transport (Transport.mcpSdk):
//
//   - Credentials are read from the tool call's _meta["org.paymentauth/credential"].
//   - Payment errors become JSON-RPC errors with the specification-defined
//     codes (-32042 payment required, -32043 verification failed, -32602
//     malformed credential, -32603 internal error) carrying
//     {httpStatus, challenges, problem} as data.
//   - Receipts are attached as _meta["org.paymentauth/receipt"] on the tool result.
//
// A tool handler charges like this:
//
//	res, err := mcpserver.Charge(ctx, payment, req, mppserver.ChargeParams{Amount: "0.01"})
//	if err != nil {
//		return nil, err
//	}
//	if res.IsChallenge() {
//		return nil, res.Challenge
//	}
//	return res.WithReceipt(&mcp.CallToolResult{Content: content}), nil
package server
