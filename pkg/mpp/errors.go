package mpp

import (
	"errors"
	"fmt"
	"net/http"
)

// ErrVerification is a sentinel error for payment verification failures.
var ErrVerification = errors.New("payment verification failed")

// ErrorType identifies a machine-readable MPP problem type.
type ErrorType string

const (
	ErrorTypePaymentRequired     ErrorType = "https://mpp.dev/errors/payment-required"
	ErrorTypeMalformedCredential ErrorType = "https://mpp.dev/errors/malformed-credential"
	ErrorTypeInvalidChallenge    ErrorType = "https://mpp.dev/errors/invalid-challenge"
	ErrorTypeVerificationFailed  ErrorType = "https://mpp.dev/errors/verification-failed"
	ErrorTypePaymentExpired      ErrorType = "https://mpp.dev/errors/payment-expired"
	ErrorTypeInvalidPayload      ErrorType = "https://mpp.dev/errors/invalid-payload"
	ErrorTypeBadRequest          ErrorType = "https://mpp.dev/errors/bad-request"
	ErrorTypePaymentInsufficient ErrorType = "https://mpp.dev/errors/payment-insufficient"
	ErrorTypeMethodUnsupported   ErrorType = "https://mpp.dev/errors/method-unsupported"
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
	case ErrorTypePaymentRequired, ErrorTypeVerificationFailed, ErrorTypePaymentExpired, ErrorTypePaymentInsufficient:
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
	return m
}

// ErrPaymentRequired returns a 402 Payment Required error.
func ErrPaymentRequired(realm, description string) *PaymentError {
	detail := "Payment is required to access this resource"
	if description != "" {
		detail = description
	}
	return newPaymentError(ErrorTypePaymentRequired, detail)
}

// ErrMalformedCredential returns a 400 error for unparseable credentials.
func ErrMalformedCredential(reason string) *PaymentError {
	return newPaymentError(ErrorTypeMalformedCredential, reason)
}

// ErrInvalidChallenge returns a 400 error for invalid or tampered challenges.
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
	return newPaymentError(ErrorTypeMethodUnsupported, fmt.Sprintf("payment method %q is not supported", method))
}
