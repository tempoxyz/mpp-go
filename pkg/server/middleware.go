package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/tempoxyz/mpp-go/pkg/mpp"
)

type contextKey int

const (
	credentialKey contextKey = iota
	receiptKey
)

// ScopeFromHTTPRequest returns the framework scope bound into charge requests.
func ScopeFromHTTPRequest(r *http.Request, route string) map[string]string {
	scope := map[string]string{}
	if route == "" {
		route = r.Pattern
	}
	if method, path, ok := strings.Cut(route, " "); ok && method != "" && path != "" {
		route = path
	}
	if route != "" {
		scope["route"] = route
	}
	if r.URL != nil {
		if r.URL.Path != "" {
			scope["resource"] = r.URL.Path
		}
		if r.URL.RawQuery != "" {
			scope["query"] = r.URL.RawQuery
		}
	}
	if len(scope) == 0 {
		return nil
	}
	return scope
}

// ContextWithPayment stores the verified payment objects on a request context.
func ContextWithPayment(ctx context.Context, credential *mpp.Credential, receipt *mpp.Receipt) context.Context {
	ctx = context.WithValue(ctx, credentialKey, credential)
	ctx = context.WithValue(ctx, receiptKey, receipt)
	return ctx
}

// CredentialFromContext extracts the Credential from the request context.
func CredentialFromContext(ctx context.Context) *mpp.Credential {
	v, _ := ctx.Value(credentialKey).(*mpp.Credential)
	return v
}

// ReceiptFromContext extracts the Receipt from the request context.
func ReceiptFromContext(ctx context.Context) *mpp.Receipt {
	v, _ := ctx.Value(receiptKey).(*mpp.Receipt)
	return v
}

// ChargeMiddleware creates an http.Handler middleware for the charge intent.
//
// It calls Mpp.Charge with the provided ChargeParams, injects the incoming
// Authorization header automatically, returns a 402 challenge when payment is
// required, and stores the verified Credential and Receipt in the request
// context on success.
func ChargeMiddleware(m *Mpp, params ChargeParams) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			chargeParams := params
			chargeParams.Authorization = r.Header.Get("Authorization")
			chargeParams.MppxScope = ScopeFromHTTPRequest(r, "")
			body, err := ReadRequestBody(r)
			if err != nil {
				WritePaymentError(w, mpp.ErrBadRequest("failed to read request body"))
				return
			}
			if len(body) > 0 {
				chargeParams.Body = body
			}

			result, err := m.Charge(r.Context(), chargeParams)
			if err != nil {
				if result != nil && result.Challenge != nil {
					WritePaymentErrorWithChallenge(w, err, result.Challenge, m.realm)
					return
				}
				WritePaymentError(w, err)
				return
			}

			if result.Challenge != nil {
				WriteChallenge(w, result.Challenge, m.realm)
				return
			}

			serveVerified(next, w, r, result.Credential, result.Receipt)
		})
	}
}

// ReadRequestBody reads and restores r.Body so middleware can verify body digests
// without consuming the body before the protected handler runs.
func ReadRequestBody(r *http.Request) ([]byte, error) {
	if r.Body == nil {
		return nil, nil
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, err
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	return body, nil
}

func serveVerified(next http.Handler, w http.ResponseWriter, r *http.Request, credential *mpp.Credential, receipt *mpp.Receipt) {
	ctx := ContextWithPayment(r.Context(), credential, receipt)

	// Mark the paid response as private so shared caches never serve a
	// Payment-Receipt to a different client. This mirrors the MPP spec
	// (mpp.dev/protocol) and the rust reference implementation.
	w.Header().Set("Cache-Control", "private")
	w.Header().Set("Payment-Receipt", receipt.ToPaymentReceipt())

	next.ServeHTTP(w, r.WithContext(ctx))
}

// WritePaymentErrorWithChallenge serializes an MPP error with a fresh retry challenge.
func WritePaymentErrorWithChallenge(w http.ResponseWriter, err error, challenge *mpp.Challenge, realm string) {
	if challenge == nil {
		WritePaymentError(w, err)
		return
	}

	header, headerErr := challenge.ToAuthenticateStrict(realm)
	if headerErr != nil {
		WritePaymentError(w, mpp.ErrInvalidChallenge(challenge.ID, headerErr.Error()))
		return
	}

	w.Header().Set("WWW-Authenticate", header)
	WritePaymentError(w, err)
}

// WriteChallenge serializes a 402 challenge response using RFC 9457 problem details.
func WriteChallenge(w http.ResponseWriter, challenge *mpp.Challenge, realm string) {
	header, err := challenge.ToAuthenticateStrict(realm)
	if err != nil {
		WritePaymentError(w, mpp.ErrBadRequest(err.Error()))
		return
	}

	w.Header().Set("WWW-Authenticate", header)
	w.Header().Set("Content-Type", "application/problem+json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusPaymentRequired)

	problem := mpp.ErrPaymentRequired(realm, challenge.Description)
	json.NewEncoder(w).Encode(problem.ProblemDetails(""))
}

// WritePaymentError serializes MPP verification errors as problem details.
func WritePaymentError(w http.ResponseWriter, err error) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.Header().Set("Cache-Control", "no-store")

	if pe, ok := err.(*mpp.PaymentError); ok {
		w.WriteHeader(pe.Status)
		json.NewEncoder(w).Encode(pe.ProblemDetails(""))
		return
	}

	w.WriteHeader(http.StatusPaymentRequired)
	problem := mpp.ErrVerificationFailed(err.Error())
	json.NewEncoder(w).Encode(problem.ProblemDetails(""))
}
