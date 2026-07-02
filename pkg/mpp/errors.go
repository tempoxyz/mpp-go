package mpp

import (
	"errors"
	"fmt"
	"net/http"
)

// ErrVerification is a sentinel error for payment verification failures.
var ErrVerification = errors.New("payment verification failed")

// Default hints for error types that support them.
const (
	HintPaymentRequired     = "Use a supported wallet to pay for this resource using one of the supported payment methods returned in the WWW-Authenticate header. See https://mpp.dev/tools/wallet.md"
	HintMalformedCredential = "Use a supported wallet to construct valid credentials for one of the supported payment methods returned in the WWW-Authenticate header. See https://mpp.dev/tools/wallet.md"
	HintMethodUnsupported   = HintPaymentRequired
)

// ErrorType identifies a machine-readable MPP problem type.
type ErrorType string

const (
	ErrorTypePaymentRequired     ErrorType = "https://paymentauth.org/problems/payment-required"
	ErrorTypeMalformedCredential ErrorType = "https://paymentauth.org/problems/malformed-credential"
	ErrorTypeInvalidChallenge    ErrorType = "https://paymentauth.org/problems/invalid-challenge"
	ErrorTypeVerificationFailed  ErrorType = "https://paymentauth.org/problems/verification-failed"
	ErrorTypePaymentExpired      ErrorType = "https://paymentauth.org/problems/payment-expired"
	ErrorTypeInvalidPayload      ErrorType = "https://mpp.dev/errors/invalid-payload"
	ErrorTypeBadRequest          ErrorType = "https://mpp.dev/errors/bad-request"
	ErrorTypePaymentInsufficient ErrorType = "https://paymentauth.org/problems/payment-insufficient"
	ErrorTypeMethodUnsupported   ErrorType = "https://paymentauth.org/problems/method-unsupported"
)

// Title returns the default RFC 9457 title for the error type.
func (t ErrorType) Title() string {
	switch t {
	case ErrorTypePaymentRequired:
		return "Payment Required"
	case ErrorTypeMalformedCredential:
		return "Malformed Credential"
	case ErrorTypeInvalidChallenge:
		return "Invalid Challenge"
	case ErrorTypeVerificationFailed:
		return "Verification Failed"
	case ErrorTypePaymentExpired:
		return "Payment Expired"
	case ErrorTypeInvalidPayload:
		return "Invalid Payload"
	case ErrorTypeBadRequest:
		return "Bad Request"
	case ErrorTypePaymentInsufficient:
		return "Payment Insufficient"
	case ErrorTypeMethodUnsupported:
		return "Method Unsupported"
	default:
		return "MPP Error"
	}
}

// Status returns the default HTTP status for the error type.
func (t ErrorType) Status() int {
	switch t {
	case ErrorTypePaymentRequired, ErrorTypeMalformedCredential, ErrorTypeInvalidChallenge, ErrorTypeVerificationFailed, ErrorTypePaymentExpired, ErrorTypePaymentInsufficient:
		return http.StatusPaymentRequired
	default:
		return http.StatusBadRequest
	}
}

// PaymentError represents an MPP error using RFC 9457 Problem Details.
type PaymentError struct {
	Type   ErrorType `json:"type"`
	Title  string    `json:"title"`
	Status int       `json:"status"`
	Detail string    `json:"detail"`
	Hint   string    `json:"hint,omitempty"`
}

func (e *PaymentError) Error() string {
	return fmt.Sprintf("%s: %s", e.Title, e.Detail)
}

// Unwrap lets errors.Is match against ErrVerification for verification errors.
func (e *PaymentError) Unwrap() error {
	if e.Type == ErrorTypeVerificationFailed {
		return ErrVerification
	}
	return nil
}

func newPaymentError(kind ErrorType, detail string) *PaymentError {
	return &PaymentError{
		Type:   kind,
		Title:  kind.Title(),
		Status: kind.Status(),
		Detail: detail,
	}
}

// ProblemDetails returns an RFC 9457 problem details map, optionally including
// a challengeID if non-empty.
func (e *PaymentError) ProblemDetails(challengeID string) map[string]any {
	m := map[string]any{
		"type":   e.Type,
		"title":  e.Title,
		"status": e.Status,
		"detail": e.Detail,
	}
	if challengeID != "" {
		m["challengeId"] = challengeID
	}
	if e.Hint != "" {
		m["hint"] = e.Hint
	}
	return m
}

// ErrPaymentRequired returns a 402 Payment Required error.
func ErrPaymentRequired(realm, description string) *PaymentError {
	detail := "Payment is required to access this resource"
	if description != "" {
		detail = description
	}
	pe := newPaymentError(ErrorTypePaymentRequired, detail)
	pe.Hint = HintPaymentRequired
	return pe
}

// ErrMalformedCredential returns a 402 error for unparseable credentials.
func ErrMalformedCredential(reason string) *PaymentError {
	pe := newPaymentError(ErrorTypeMalformedCredential, reason)
	pe.Hint = HintMalformedCredential
	return pe
}

// ErrInvalidChallenge returns a 402 error for invalid or tampered challenges.
func ErrInvalidChallenge(challengeID, reason string) *PaymentError {
	return newPaymentError(ErrorTypeInvalidChallenge, fmt.Sprintf("challenge %s: %s", challengeID, reason))
}

// ErrVerificationFailed returns a 402 error for failed payment verification.
func ErrVerificationFailed(reason string) *PaymentError {
	return newPaymentError(ErrorTypeVerificationFailed, reason)
}

// ErrPaymentExpired returns a 402 error for expired payment challenges.
func ErrPaymentExpired(expires string) *PaymentError {
	return newPaymentError(ErrorTypePaymentExpired, fmt.Sprintf("payment expired at %s", expires))
}

// ErrInvalidPayload returns a 400 error for invalid payment payloads.
func ErrInvalidPayload(reason string) *PaymentError {
	return newPaymentError(ErrorTypeInvalidPayload, reason)
}

// ErrBadRequest returns a generic 400 error.
func ErrBadRequest(reason string) *PaymentError {
	return newPaymentError(ErrorTypeBadRequest, reason)
}

// ErrPaymentInsufficient returns a 402 error for insufficient payment amounts.
func ErrPaymentInsufficient(reason string) *PaymentError {
	return newPaymentError(ErrorTypePaymentInsufficient, reason)
}

// ErrMethodUnsupported returns a 400 error for unsupported payment methods.
func ErrMethodUnsupported(method string) *PaymentError {
	pe := newPaymentError(ErrorTypeMethodUnsupported, fmt.Sprintf("payment method %q is not supported", method))
	pe.Hint = HintMethodUnsupported
	return pe
}
