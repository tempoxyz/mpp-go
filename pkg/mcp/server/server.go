package server

import (
	"context"
	"maps"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	mcppayment "github.com/tempoxyz/mpp-go/pkg/mcp/internal/payment"
	"github.com/tempoxyz/mpp-go/pkg/mpp"
	mppserver "github.com/tempoxyz/mpp-go/pkg/server"
)

// Result is either a payment Challenge to return from the tool handler or a
// verified (Credential, Receipt) pair.
type Result struct {
	// Challenge is the JSON-RPC error to return from the tool handler when the
	// client still needs to pay or its Credential was rejected. The SDK sends
	// it to the client unchanged.
	Challenge *jsonrpc.Error
	// Credential is the verified client Credential on success.
	Credential *mpp.Credential
	// Receipt acknowledges the verified payment on success.
	Receipt *mpp.Receipt

	outcome *mcppayment.Outcome
}

// IsChallenge reports whether the tool handler must return Challenge.
func (r *Result) IsChallenge() bool {
	return r.Challenge != nil
}

// WithReceipt returns a copy of result with the payment receipt attached as
// _meta["org.paymentauth/receipt"], keeping its other metadata. A nil result
// becomes an empty tool result.
func (r *Result) WithReceipt(result *mcp.CallToolResult) *mcp.CallToolResult {
	var out mcp.CallToolResult
	if result != nil {
		out = *result
	}
	if out.Content == nil {
		out.Content = []mcp.Content{}
	}
	if r.outcome == nil {
		return &out
	}
	receipt, ok := r.outcome.MCPReceipt()
	if !ok {
		return &out
	}
	meta := maps.Clone(out.Meta)
	if meta == nil {
		meta = mcp.Meta{}
	}
	meta[mpp.MCPReceiptMetaKey] = receipt
	out.Meta = meta
	return &out
}

// Charge runs payment.Charge for an MCP tool call, reading the Credential from
// the request's _meta instead of HTTP headers. Leave the credential fields of
// params empty. It returns a Result whose Challenge the handler returns when
// payment is still required or was rejected, and an error only when no
// Challenge could be issued, such as for invalid params.
func Charge(
	ctx context.Context,
	payment *mppserver.Mpp,
	req *mcp.CallToolRequest,
	params mppserver.ChargeParams,
) (*Result, error) {
	return toResult(mcppayment.Charge(ctx, payment, meta(req), params))
}

// Compose offers several payment options on one tool, like
// server.ComposeMiddleware for HTTP and mppx's compose: without a Credential
// the Challenge lists every configured offer, and a Credential is verified
// against the offer it echoes, so only that offer is settled.
func Compose(
	ctx context.Context,
	req *mcp.CallToolRequest,
	configs ...mppserver.ComposeConfig,
) (*Result, error) {
	return toResult(mcppayment.Compose(ctx, meta(req), configs...))
}

func meta(req *mcp.CallToolRequest) map[string]any {
	if req == nil || req.Params == nil {
		return nil
	}
	return req.Params.Meta
}

func toResult(outcome *mcppayment.Outcome, err error) (*Result, error) {
	if err != nil {
		return nil, err
	}
	if outcome.Challenged {
		code, message, data := outcome.Error()
		return &Result{Challenge: &jsonrpc.Error{Code: code, Message: message, Data: data}}, nil
	}
	return &Result{Credential: outcome.Credential, Receipt: outcome.Receipt, outcome: outcome}, nil
}
