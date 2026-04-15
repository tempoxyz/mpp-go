package mpp

import (
	"errors"
	"fmt"
	"net/http"
)

// ErrVerification is a sentinel error for payment verification failures.
var ErrVerification = errors.New("payment verification failed")

// PaymentError represents an MPP error using RFC 9457 Problem Details.
type PaymentError struct {
	Type   string `json:"type"`
	Title  string `json:"title"`
	Status int    `json:"status"`
	Detail string `json:"detail"`
}

func (e *PaymentError) Error() string {
	return fmt.Sprintf("%s: %s", e.Title, e.Detail)
}

// Unwrap lets errors.Is match against ErrVerification for verification errors.
func (e *PaymentError) Unwrap() error {
	if e.Type == "https://mpp.dev/errors/verification-failed" {
		return ErrVerification
	}
	return nil
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
	return &PaymentError{
		Type:   "https://mpp.dev/errors/payment-required",
		Title:  "Payment Required",
		Status: http.StatusPaymentRequired,
		Detail: detail,
	}
}

// ErrMalformedCredential returns a 400 error for unparseable credentials.
func ErrMalformedCredential(reason string) *PaymentError {
	return &PaymentError{
		Type:   "https://mpp.dev/errors/malformed-credential",
		Title:  "Malformed Credential",
		Status: http.StatusBadRequest,
		Detail: reason,
	}
}

// ErrInvalidChallenge returns a 400 error for invalid or tampered challenges.
func ErrInvalidChallenge(challengeID, reason string) *PaymentError {
	return &PaymentError{
		Type:   "https://mpp.dev/errors/invalid-challenge",
		Title:  "Invalid Challenge",
		Status: http.StatusBadRequest,
		Detail: fmt.Sprintf("challenge %s: %s", challengeID, reason),
	}
}

// ErrVerificationFailed returns a 402 error for failed payment verification.
func ErrVerificationFailed(reason string) *PaymentError {
	return &PaymentError{
		Type:   "https://mpp.dev/errors/verification-failed",
		Title:  "Verification Failed",
		Status: http.StatusPaymentRequired,
		Detail: reason,
	}
}

// ErrPaymentExpired returns a 402 error for expired payment challenges.
func ErrPaymentExpired(expires string) *PaymentError {
	return &PaymentError{
		Type:   "https://mpp.dev/errors/payment-expired",
		Title:  "Payment Expired",
		Status: http.StatusPaymentRequired,
		Detail: fmt.Sprintf("payment expired at %s", expires),
	}
}

// ErrInvalidPayload returns a 400 error for invalid payment payloads.
func ErrInvalidPayload(reason string) *PaymentError {
	return &PaymentError{
		Type:   "https://mpp.dev/errors/invalid-payload",
		Title:  "Invalid Payload",
		Status: http.StatusBadRequest,
		Detail: reason,
	}
}

// ErrBadRequest returns a generic 400 error.
func ErrBadRequest(reason string) *PaymentError {
	return &PaymentError{
		Type:   "https://mpp.dev/errors/bad-request",
		Title:  "Bad Request",
		Status: http.StatusBadRequest,
		Detail: reason,
	}
}

// ErrPaymentInsufficient returns a 402 error for insufficient payment amounts.
func ErrPaymentInsufficient(reason string) *PaymentError {
	return &PaymentError{
		Type:   "https://mpp.dev/errors/payment-insufficient",
		Title:  "Payment Insufficient",
		Status: http.StatusPaymentRequired,
		Detail: reason,
	}
}

// ErrMethodUnsupported returns a 400 error for unsupported payment methods.
func ErrMethodUnsupported(method string) *PaymentError {
	return &PaymentError{
		Type:   "https://mpp.dev/errors/method-unsupported",
		Title:  "Method Unsupported",
		Status: http.StatusBadRequest,
		Detail: fmt.Sprintf("payment method %q is not supported", method),
	}
}
