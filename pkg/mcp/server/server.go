package server

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"

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
	if r.Receipt == nil || r.Credential == nil {
		return &out
	}
	meta := maps.Clone(out.Meta)
	if meta == nil {
		meta = mcp.Meta{}
	}
	meta[mpp.MCPReceiptMetaKey] = mpp.MCPReceipt{
		Receipt:     *r.Receipt,
		ChallengeID: r.Credential.Challenge.ID,
	}
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
	authorization, credentialErr := credentialAuthorization(req)
	params.Authorization = authorization
	params.PaymentAuthorization = authorization
	result, err := payment.Charge(ctx, params)
	if credentialErr != nil && err == nil {
		return challengeResult(result.Challenges, credentialErr), nil
	}
	return toResult(result, err)
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
	authorization, credentialErr := credentialAuthorization(req)
	result, err := mppserver.ComposeCharge(ctx, authorization, configs...)
	if credentialErr != nil && err == nil {
		return challengeResult(result.Challenges, credentialErr), nil
	}
	return toResult(result, err)
}

// credentialAuthorization reads the Credential from the tool call's metadata
// and formats it as a Payment Authorization value. It returns empty when the
// call carries no Credential, and a malformed-credential error, with an empty
// value, when it carries one that does not survive the canonical wire format.
func credentialAuthorization(req *mcp.CallToolRequest) (string, error) {
	if req == nil || req.Params == nil {
		return "", nil
	}
	value, ok := req.Params.Meta[mpp.MCPCredentialMetaKey]
	if !ok {
		return "", nil
	}
	malformed := mpp.ErrMalformedCredential("invalid credential encoding")
	raw, err := json.Marshal(value)
	if err != nil {
		return "", malformed
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return "", malformed
	}
	if _, ok := fields["challenge"]; !ok {
		return "", malformed
	}
	if _, ok := fields["payload"]; !ok {
		return "", malformed
	}
	var credential mpp.Credential
	if err := json.Unmarshal(raw, &credential); err != nil {
		return "", malformed
	}
	authorization, err := mpp.FormatAuthorizationStrict(&credential)
	if err != nil {
		return "", malformed
	}
	if _, err := mpp.ParseCredential(authorization); err != nil {
		return "", malformed
	}
	return authorization, nil
}

func toResult(result *mppserver.ChargeResult, err error) (*Result, error) {
	var paymentErr *mpp.PaymentError
	if err != nil && !errors.As(err, &paymentErr) {
		return nil, err
	}
	if result == nil || result.Challenge == nil {
		if err != nil {
			// No Challenge could be issued, so the client cannot pay again.
			return &Result{Challenge: paymentError(nil, err)}, nil
		}
		return &Result{Credential: result.Credential, Receipt: result.Receipt}, nil
	}
	challenges := result.Challenges
	if len(challenges) == 0 || (err != nil && paymentErr.Type != mpp.ErrorTypeMalformedCredential) {
		// A rejected Credential is answered with the offer it echoed, so the
		// client does not pay another offer for the same failure.
		challenges = []*mpp.Challenge{result.Challenge}
	}
	return challengeResult(challenges, err), nil
}

func challengeResult(challenges []*mpp.Challenge, err error) *Result {
	return &Result{Challenge: paymentError(challenges, err)}
}

// paymentError builds the MCP JSON-RPC error for challenges and err, which is
// nil when payment is simply required. Like mppx, the data always carries the
// problem details.
func paymentError(challenges []*mpp.Challenge, err error) *jsonrpc.Error {
	code := mpp.MCPErrorCode(err)
	if err == nil {
		description := ""
		if len(challenges) > 0 {
			description = challenges[0].Description
		}
		err = mpp.ErrPaymentRequired("", description)
	}
	data := mpp.MCPPaymentRequiredData{
		HTTPStatus: http.StatusInternalServerError,
		Challenges: challenges,
	}
	message := err.Error()
	var paymentErr *mpp.PaymentError
	if errors.As(err, &paymentErr) {
		data.HTTPStatus = paymentErr.Status
		message = paymentErr.Detail
		challengeID := ""
		if len(challenges) > 0 {
			challengeID = challenges[0].ID
		}
		data.Problem = paymentErr.ProblemDetails(challengeID)
	}
	encoded, marshalErr := json.Marshal(data)
	if marshalErr != nil {
		return &jsonrpc.Error{Code: mpp.MCPInternalErrorCode, Message: "An internal payment error occurred."}
	}
	return &jsonrpc.Error{Code: int64(code), Message: message, Data: encoded}
}
