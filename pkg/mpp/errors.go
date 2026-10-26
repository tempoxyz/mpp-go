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
	ErrorTypeInvalidPayload      ErrorType = "https://paymentauth.org/problems/invalid-payload"
	ErrorTypeBadRequest          ErrorType = "https://paymentauth.org/problems/bad-request"
	ErrorTypePaymentInsufficient ErrorType = "https://paymentauth.org/problems/payment-insufficient"
	ErrorTypeMethodUnsupported   ErrorType = "https://paymentauth.org/problems/method-unsupported"
	ErrorTypeInternalPayment     ErrorType = "https://paymentauth.org/problems/internal-payment-error"
	// ErrorTypePaymentActionRequired reports a payment that needs further
	// action by the payer, such as a Stripe PaymentIntent in requires_action.
	ErrorTypePaymentActionRequired ErrorType = "https://paymentauth.org/problems/payment-action-required"
	// ErrorTypePaymentOutcomeUnknown reports a credential that was sent but
	// whose payment may or may not have settled. Do not blindly retry.
	ErrorTypePaymentOutcomeUnknown ErrorType = "https://paymentauth.org/problems/payment-outcome-unknown"
)

// Error returns the problem type URI, so an ErrorType can be the target of
// errors.Is: errors.Is(err, ErrorTypeMalformedCredential) reports whether err
// wraps a PaymentError of that type.
func (t ErrorType) Error() string {
	return string(t)
}

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
	case ErrorTypeInternalPayment:
		return "Internal Payment Error"
	case ErrorTypePaymentActionRequired:
		return "Payment Action Required"
	case ErrorTypePaymentOutcomeUnknown:
		return "Payment Outcome Unknown"
	default:
		return "MPP Error"
	}
}

// Status returns the default HTTP status for the error type.
func (t ErrorType) Status() int {
	switch t {
	case ErrorTypePaymentRequired, ErrorTypeMalformedCredential, ErrorTypeInvalidChallenge, ErrorTypeVerificationFailed, ErrorTypePaymentExpired, ErrorTypeInvalidPayload, ErrorTypePaymentInsufficient,
		ErrorTypePaymentActionRequired, ErrorTypePaymentOutcomeUnknown:
		return http.StatusPaymentRequired
	case ErrorTypeInternalPayment:
		return http.StatusInternalServerError
	default:
		return http.StatusBadRequest
	}
}

// PaymentError represents an MPP error using RFC 9457 Problem Details.
//
// Match its problem type through wrapping with errors.Is and an ErrorType:
//
//	if errors.Is(err, mpp.ErrorTypePaymentExpired) { ... }
//
// errors.As(err, &pe) reaches the PaymentError itself.
type PaymentError struct {
	Type   ErrorType `json:"type"`
	Title  string    `json:"title"`
	Status int       `json:"status"`
	Detail string    `json:"detail"`
	Hint   string    `json:"hint,omitempty"`
	// Details contains safe machine-readable error metadata.
	Details map[string]any `json:"details,omitempty"`

	// cause is the underlying error, kept for callers and logs but never
	// serialized into the problem details.
	cause error
}

func (e *PaymentError) Error() string {
	return fmt.Sprintf("%s: %s", e.Title, e.Detail)
}

// Is reports whether target is the ErrorType of e, so errors.Is(err,
// ErrorTypeVerificationFailed) matches a wrapped PaymentError of that type.
func (e *PaymentError) Is(target error) bool {
	t, ok := target.(ErrorType)
	return ok && e.Type == t
}

// Unwrap returns the cause attached with WithCause, if any, so errors.Is and
// errors.As reach it. Otherwise verification errors unwrap to ErrVerification.
func (e *PaymentError) Unwrap() error {
	if e.cause != nil {
		return e.cause
	}
	if e.Type == ErrorTypeVerificationFailed {
		return ErrVerification
	}
	return nil
}

// WithCause returns a copy of e that unwraps to cause. The cause is not part
// of the problem details sent to clients.
func (e *PaymentError) WithCause(cause error) *PaymentError {
	c := *e
	c.cause = cause
	return &c
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
	if len(e.Details) > 0 {
		m["details"] = e.Details
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

// ErrCredentialReplayed returns a 403 verification-failed error for a
// credential whose payment was already used: consumed by an earlier request or
// claimed by a concurrent one. The payment itself was valid, so the response
// carries no retry challenge and auto-paying clients do not pay again. The
// problem type stays verification-failed; only the status differs.
func ErrCredentialReplayed(reason string) *PaymentError {
	if reason == "" {
		reason = "credential has already been used"
	}
	pe := newPaymentError(ErrorTypeVerificationFailed, reason)
	pe.Status = http.StatusForbidden
	return pe
}

// ErrPaymentExpired returns a 402 error for expired payment challenges.
func ErrPaymentExpired(expires string) *PaymentError {
	return newPaymentError(ErrorTypePaymentExpired, fmt.Sprintf("payment expired at %s", expires))
}

// ErrInvalidPayload returns a 402 error for invalid payment payloads.
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

// ErrInternalPayment returns a 500 error for unexpected payment processing
// failures. Its detail is fixed so internal error messages are never exposed.
func ErrInternalPayment() *PaymentError {
	return newPaymentError(ErrorTypeInternalPayment, "An internal payment error occurred.")
}

// ErrPaymentActionRequired returns a 402 error for a payment that requires
// further action by the payer. reason may be empty. It matches mppx's
// PaymentActionRequiredError.
func ErrPaymentActionRequired(reason string) *PaymentError {
	detail := "Payment requires action."
	if reason != "" {
		detail = fmt.Sprintf("Payment requires action: %s.", reason)
	}
	return newPaymentError(ErrorTypePaymentActionRequired, detail)
}

// ErrPaymentOutcomeUnknown returns a 402 error for a credential that was sent
// for challengeID without a definitive result, so the payment may have
// settled. It matches mppx's PaymentOutcomeUnknownError. The server
// middleware writers send it without retry challenges, and client.Transport
// returns such a response instead of paying again.
func ErrPaymentOutcomeUnknown(challengeID string) *PaymentError {
	return newPaymentError(ErrorTypePaymentOutcomeUnknown, fmt.Sprintf(
		"Payment outcome is unknown after sending a credential for challenge %q. Do not blindly retry.",
		challengeID,
	))
}
