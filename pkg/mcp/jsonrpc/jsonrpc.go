// Package jsonrpc charges for MCP tool calls handled as raw JSON-RPC
// messages, without an MCP SDK, matching mppx's raw MCP server transport
// (Transport.mcp()):
//
//   - Credentials are read from params._meta["org.paymentauth/credential"].
//   - Payment errors are JSON-RPC error responses with the request's id and
//     codes -32042 (payment required), -32043 (verification failed), -32602
//     (malformed credential) or -32603 (internal error), carrying
//     {httpStatus, challenges, problem} as data.
//   - Receipts are attached as result._meta["org.paymentauth/receipt"];
//     error responses are returned unchanged.
//
// For servers built on the official MCP Go SDK, use pkg/mcp/server instead.
//
//	res, err := jsonrpc.Charge(ctx, payment, req, server.ChargeParams{Amount: "0.01"})
//	if err != nil {
//		return nil, err
//	}
//	if res.IsChallenge() {
//		return res.Challenge, nil
//	}
//	return res.WithReceipt(&jsonrpc.Response{JSONRPC: "2.0", ID: req.ID, Result: result}), nil
package jsonrpc

import (
	"bytes"
	"context"
	"encoding/json"
	"maps"

	mcppayment "github.com/tempoxyz/mpp-go/pkg/mcp/internal/payment"
	"github.com/tempoxyz/mpp-go/pkg/mpp"
	"github.com/tempoxyz/mpp-go/pkg/server"
)

// Version is the JSON-RPC protocol version.
const Version = "2.0"

// Request is a JSON-RPC request carrying an MCP method call, such as
// tools/call. Params holds the method's params, including _meta.
type Request struct {
	JSONRPC string          `json:"jsonrpc,omitempty"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  map[string]any  `json:"params,omitempty"`
}

// UnmarshalJSON decodes numbers as json.Number, so integers above 2^53 in the
// Credential survive re-encoding and still match the Challenge they echo.
func (r *Request) UnmarshalJSON(data []byte) error {
	type request Request
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var decoded request
	if err := decoder.Decode(&decoded); err != nil {
		return err
	}
	*r = Request(decoded)
	return nil
}

// Meta returns the request's params._meta, or nil when it has none.
func (r *Request) Meta() map[string]any {
	if r == nil {
		return nil
	}
	meta, _ := r.Params["_meta"].(map[string]any)
	return meta
}

// Response is a JSON-RPC response: Result on success, Error on failure.
type Response struct {
	JSONRPC string          `json:"jsonrpc,omitempty"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  map[string]any  `json:"result,omitempty"`
	Error   *Error          `json:"error,omitempty"`
}

// Error is a JSON-RPC error object. For payment errors Data holds
// mpp.MCPPaymentRequiredData.
type Error struct {
	Code    int64           `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

// PaymentRequired decodes the payment data of a payment error, reporting
// false for other errors.
func (e *Error) PaymentRequired() (*mpp.MCPPaymentRequiredData, bool) {
	if e == nil || len(e.Data) == 0 {
		return nil, false
	}
	if e.Code != mpp.MCPPaymentRequiredCode &&
		e.Code != mpp.MCPPaymentVerificationFailedCode &&
		e.Code != mpp.MCPInvalidParamsCode &&
		e.Code != mpp.MCPInternalErrorCode {
		return nil, false
	}
	var data mpp.MCPPaymentRequiredData
	if err := json.Unmarshal(e.Data, &data); err != nil {
		return nil, false
	}
	return &data, true
}

// Result is either a payment error response to send or a verified
// (Credential, Receipt) pair.
type Result struct {
	// Challenge is the JSON-RPC error response to send when the client still
	// needs to pay or its Credential was rejected. It carries the request's id.
	Challenge *Response
	// Credential is the verified client Credential on success.
	Credential *mpp.Credential
	// Receipt acknowledges the verified payment on success.
	Receipt *mpp.Receipt

	outcome *mcppayment.Outcome
}

// IsChallenge reports whether the server must send Challenge.
func (r *Result) IsChallenge() bool {
	return r.Challenge != nil
}

// WithReceipt returns a copy of response with the payment receipt attached as
// result._meta["org.paymentauth/receipt"], keeping its other metadata. Error
// responses are returned unchanged, as in mppx.
func (r *Result) WithReceipt(response *Response) *Response {
	if response == nil {
		return nil
	}
	out := *response
	if out.Error != nil || r.outcome == nil {
		return &out
	}
	receipt, ok := r.outcome.MCPReceipt()
	if !ok {
		return &out
	}
	out.Result = maps.Clone(out.Result)
	if out.Result == nil {
		out.Result = map[string]any{}
	}
	meta, _ := out.Result["_meta"].(map[string]any)
	meta = maps.Clone(meta)
	if meta == nil {
		meta = map[string]any{}
	}
	meta[mpp.MCPReceiptMetaKey] = receipt
	out.Result["_meta"] = meta
	return &out
}

// Charge runs payment.Charge for a JSON-RPC request, reading the Credential
// from params._meta instead of HTTP headers. Leave the credential fields of
// params empty. It returns an error only when no Challenge could be issued,
// such as for invalid params.
func Charge(ctx context.Context, payment *server.Mpp, req *Request, params server.ChargeParams) (*Result, error) {
	outcome, err := mcppayment.Charge(ctx, payment, req.Meta(), params)
	return toResult(req, outcome, err)
}

// Compose offers several payment options for one request, like mppx's
// compose: without a Credential the error lists every configured offer, and a
// Credential is verified against the offer it echoes, so only that offer is
// settled.
func Compose(ctx context.Context, req *Request, configs ...server.ComposeConfig) (*Result, error) {
	outcome, err := mcppayment.Compose(ctx, req.Meta(), configs...)
	return toResult(req, outcome, err)
}

func toResult(req *Request, outcome *mcppayment.Outcome, err error) (*Result, error) {
	if err != nil {
		return nil, err
	}
	if outcome.Challenged {
		code, message, data := outcome.Error()
		var id json.RawMessage
		if req != nil {
			id = req.ID
		}
		return &Result{Challenge: &Response{
			JSONRPC: Version,
			ID:      id,
			Error:   &Error{Code: code, Message: message, Data: data},
		}}, nil
	}
	return &Result{Credential: outcome.Credential, Receipt: outcome.Receipt, outcome: outcome}, nil
}
