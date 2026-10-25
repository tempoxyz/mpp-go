package mpp

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
)

// MCP JSON-RPC error codes for payment failures, matching mppx's Mcp module.
const (
	// MCPPaymentRequiredCode reports that a tool call requires payment.
	MCPPaymentRequiredCode = -32042
	// MCPPaymentVerificationFailedCode reports that a submitted Credential was rejected.
	MCPPaymentVerificationFailedCode = -32043
	// MCPInvalidParamsCode reports a malformed Credential or payload.
	MCPInvalidParamsCode = -32602
	// MCPInternalErrorCode reports a server-side payment processing failure.
	MCPInternalErrorCode = -32603
)

// MCP _meta keys for payment data.
const (
	// MCPCredentialMetaKey carries the Credential in a tool call's params._meta.
	MCPCredentialMetaKey = "org.paymentauth/credential"
	// MCPPaymentRequiredMetaKey carries MCPPaymentRequiredData in a tool result's _meta.
	MCPPaymentRequiredMetaKey = "org.paymentauth/payment-required"
	// MCPReceiptMetaKey carries the MCPReceipt in a tool result's _meta.
	MCPReceiptMetaKey = "org.paymentauth/receipt"
)

// MCPErrorCode maps a payment error to its MCP JSON-RPC error code. A nil
// error or a payment-required error maps to MCPPaymentRequiredCode, malformed
// credentials and invalid payloads to MCPInvalidParamsCode, internal payment
// errors and non-payment errors to MCPInternalErrorCode, and every other
// payment error to MCPPaymentVerificationFailedCode.
func MCPErrorCode(err error) int {
	if err == nil {
		return MCPPaymentRequiredCode
	}
	var pe *PaymentError
	if !errors.As(err, &pe) {
		return MCPInternalErrorCode
	}
	switch pe.Type {
	case ErrorTypePaymentRequired:
		return MCPPaymentRequiredCode
	case ErrorTypeMalformedCredential, ErrorTypeInvalidPayload:
		return MCPInvalidParamsCode
	case ErrorTypeInternalPayment:
		return MCPInternalErrorCode
	default:
		return MCPPaymentVerificationFailedCode
	}
}

// MCPPaymentRequiredData is the data of an MCP payment error, or the value of
// MCPPaymentRequiredMetaKey in a tool result.
type MCPPaymentRequiredData struct {
	// HTTPStatus is the HTTP status the same failure would have, such as 402.
	HTTPStatus int `json:"httpStatus"`
	// Challenges lists the offered Challenges in presentation order.
	Challenges []*Challenge `json:"challenges"`
	// Problem holds RFC 9457 problem details when a Credential was rejected.
	Problem map[string]any `json:"problem,omitempty"`
}

// MCPReceipt is a Receipt carried in MCP tool result metadata, bound to the
// Challenge it settles.
type MCPReceipt struct {
	Receipt
	// ChallengeID is the ID of the Challenge the Credential answered.
	ChallengeID string
}

// MarshalJSON emits the canonical receipt fields plus challengeId.
func (r MCPReceipt) MarshalJSON() ([]byte, error) {
	data := receiptFields(&r.Receipt)
	data["challengeId"] = r.ChallengeID
	return encodeStableJSON(data)
}

// UnmarshalJSON validates the receipt fields and reads challengeId.
func (r *MCPReceipt) UnmarshalJSON(data []byte) error {
	fields, err := decodeJSONMap(data)
	if err != nil {
		return fmt.Errorf("mpp: invalid MCP receipt: %w", err)
	}
	challengeID, _ := fields["challengeId"].(string)
	if challengeID == "" {
		return fmt.Errorf("mpp: MCP receipt missing challengeId")
	}
	fields = maps.Clone(fields)
	delete(fields, "challengeId")
	receipt, err := receiptFromFields(fields)
	if err != nil {
		return err
	}
	*r = MCPReceipt{Receipt: *receipt, ChallengeID: challengeID}
	return nil
}

var _ json.Marshaler = MCPReceipt{}
