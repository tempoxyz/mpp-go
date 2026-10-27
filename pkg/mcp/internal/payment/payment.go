// Package payment holds the MCP payment flow shared by the MCP SDK and raw
// JSON-RPC server integrations: reading the Credential from _meta, running
// the charge, and building the payment error.
package payment

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/tempoxyz/mpp-go/internal/serverevents"
	"github.com/tempoxyz/mpp-go/pkg/mpp"
	"github.com/tempoxyz/mpp-go/pkg/server"
)

// Outcome is the result of a charge over MCP: either a payment error to
// return, or a verified (Credential, Receipt) pair.
type Outcome struct {
	// Challenged is true when the call must be answered with a payment error.
	Challenged bool
	// Challenges are the offers in the payment error.
	Challenges []*mpp.Challenge
	// Err is the payment error, nil when payment is simply required.
	Err error
	// Credential and Receipt are set on success.
	Credential *mpp.Credential
	Receipt    *mpp.Receipt
}

// Charge runs payment.Charge with the Credential in meta.
func Charge(ctx context.Context, payment *server.Mpp, meta map[string]any, params server.ChargeParams) (*Outcome, error) {
	authorization, credentialErr := Authorization(meta)
	if credentialErr != nil {
		reportMalformed(ctx, credentialErr, payment)
	}
	params.Authorization = authorization
	params.PaymentAuthorization = authorization
	result, err := payment.Charge(ctx, params)
	if credentialErr != nil && err == nil {
		return rejected(result, credentialErr), nil
	}
	return outcome(result, err)
}

// Compose runs server.Compose with the Credential in meta.
func Compose(ctx context.Context, meta map[string]any, configs ...server.ComposeConfig) (*Outcome, error) {
	authorization, credentialErr := Authorization(meta)
	if credentialErr != nil {
		payments := make([]*server.Mpp, 0, len(configs))
		for _, config := range configs {
			payments = append(payments, config.Mpp)
		}
		reportMalformed(ctx, credentialErr, payments...)
	}
	result, err := server.Compose(ctx, authorization, configs...)
	if credentialErr != nil && err == nil {
		return rejected(result, credentialErr), nil
	}
	return outcome(result, err)
}

// reportMalformed reports a Credential in _meta that cannot be read to the
// PaymentFailed hook of every distinct handler, as the HTTP flow does for a
// malformed Authorization header. The charge then runs without a Credential,
// so it does not report it again.
func reportMalformed(ctx context.Context, err error, payments ...*server.Mpp) {
	handlers := make([]any, 0, len(payments))
	for _, payment := range payments {
		if payment != nil {
			handlers = append(handlers, payment)
		}
	}
	serverevents.PaymentFailed(ctx, handlers, nil, err)
}

// Authorization reads the Credential from meta and formats it as a Payment
// Authorization value. It returns empty when meta carries no Credential, and
// a malformed-credential error when the Credential does not survive the
// canonical wire format (like mppx's McpCredential.parse).
func Authorization(meta map[string]any) (string, error) {
	value, ok := meta[mpp.MCPCredentialMetaKey]
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
	authorization, err := mpp.FormatCredential(&credential)
	if err != nil {
		return "", malformed
	}
	if _, err := mpp.ParseCredential(authorization); err != nil {
		return "", malformed
	}
	return authorization, nil
}

func outcome(result *server.ChargeResult, err error) (*Outcome, error) {
	var paymentErr *mpp.PaymentError
	if err != nil && !errors.As(err, &paymentErr) {
		return nil, err
	}
	if result == nil || result.Challenge == nil {
		if err != nil {
			// No Challenge could be issued, so the client cannot pay again.
			return &Outcome{Challenged: true, Err: err}, nil
		}
		return &Outcome{Credential: result.Credential, Receipt: result.Receipt}, nil
	}
	if err != nil {
		return rejected(result, err), nil
	}
	return &Outcome{Challenged: true, Challenges: result.Challenges}, nil
}

// rejected answers a rejected or malformed Credential with a single offer,
// the one it echoed or else the first, as mppx does, so the client does not
// pay another offer for the same failure.
func rejected(result *server.ChargeResult, err error) *Outcome {
	if server.IsPaymentOutcomeUnknown(err) {
		return &Outcome{Challenged: true, Err: err}
	}
	return &Outcome{Challenged: true, Challenges: []*mpp.Challenge{result.Challenge}, Err: err}
}

// Error returns the JSON-RPC error code, message and data for a challenged
// outcome. Like mppx, the data always carries the problem details.
func (o *Outcome) Error() (int64, string, json.RawMessage) {
	err := o.Err
	code := mpp.MCPErrorCode(err)
	if err == nil {
		description := ""
		if len(o.Challenges) > 0 {
			description = o.Challenges[0].Description
		}
		err = mpp.ErrPaymentRequired("", description)
	}
	data := mpp.MCPPaymentRequiredData{
		HTTPStatus: http.StatusInternalServerError,
		Challenges: o.Challenges,
	}
	message := err.Error()
	var paymentErr *mpp.PaymentError
	if errors.As(err, &paymentErr) {
		data.HTTPStatus = paymentErr.Status
		message = paymentErr.Detail
		challengeID := ""
		if len(o.Challenges) > 0 {
			challengeID = o.Challenges[0].ID
		}
		data.Problem = paymentErr.ProblemDetails(challengeID)
	}
	encoded, marshalErr := json.Marshal(data)
	if marshalErr != nil {
		return mpp.MCPInternalErrorCode, "An internal payment error occurred.", nil
	}
	return int64(code), message, encoded
}

// MCPReceipt returns the MCP receipt for a successful outcome.
func (o *Outcome) MCPReceipt() (mpp.MCPReceipt, bool) {
	if o.Receipt == nil || o.Credential == nil {
		return mpp.MCPReceipt{}, false
	}
	return mpp.MCPReceipt{Receipt: *o.Receipt, ChallengeID: o.Credential.Challenge.ID}, true
}
