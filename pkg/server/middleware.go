package server

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/tempoxyz/mpp-go/pkg/mpp"
)

type contextKey int

const (
	credentialKey contextKey = iota
	receiptKey
)

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

// MiddlewareOption configures the payment middleware.
type MiddlewareOption func(*middlewareConfig)

type middlewareConfig struct {
	intentName string
}

// WithIntent sets the intent name for the middleware (default: "charge").
func WithIntent(name string) MiddlewareOption {
	return func(c *middlewareConfig) {
		c.intentName = name
	}
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

			result, err := m.Charge(r.Context(), chargeParams)
			if err != nil {
				writePaymentError(w, err)
				return
			}

			if result.Challenge != nil {
				writeChallenge(w, result.Challenge, m.realm)
				return
			}

			serveVerified(next, w, r, result.Credential, result.Receipt)
		})
	}
}

// PaymentMiddleware creates an http.Handler middleware that requires payment.
// It intercepts requests, handles the 402 challenge flow, and injects
// Credential and Receipt into the request context on success.
//
// Deprecated: use ChargeMiddleware for the charge-only Tempo flow.
//
// For the common charge intent, prefer ChargeMiddleware so you can pass the
// full ChargeParams instead of only an amount.
func PaymentMiddleware(m *Mpp, amount string, opts ...MiddlewareOption) func(http.Handler) http.Handler {
	cfg := &middlewareConfig{
		intentName: "charge",
	}
	for _, opt := range opts {
		opt(cfg)
	}
	if cfg.intentName == "charge" {
		return ChargeMiddleware(m, ChargeParams{Amount: amount})
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			intent, ok := m.method.Intents()[cfg.intentName]
			if !ok {
				http.Error(w, "unsupported payment intent", http.StatusInternalServerError)
				return
			}

			result, err := VerifyOrChallenge(r.Context(), VerifyParams{
				Authorization: r.Header.Get("Authorization"),
				Intent:        intent,
				Request: map[string]any{
					"amount": amount,
				},
				Realm:     m.realm,
				SecretKey: m.secretKey,
				Method:    m.method.Name(),
			})
			if err != nil {
				writePaymentError(w, err)
				return
			}

			if result.Challenge != nil {
				writeChallenge(w, result.Challenge, m.realm)
				return
			}

			serveVerified(next, w, r, result.Credential, result.Receipt)
		})
	}
}

func serveVerified(next http.Handler, w http.ResponseWriter, r *http.Request, credential *mpp.Credential, receipt *mpp.Receipt) {
	ctx := r.Context()
	ctx = context.WithValue(ctx, credentialKey, credential)
	ctx = context.WithValue(ctx, receiptKey, receipt)

	w.Header().Set("Payment-Receipt", receipt.ToPaymentReceipt())

	next.ServeHTTP(w, r.WithContext(ctx))
}

func writeChallenge(w http.ResponseWriter, challenge *mpp.Challenge, realm string) {
	w.Header().Set("WWW-Authenticate", challenge.ToWWWAuthenticate(realm))
	w.Header().Set("Content-Type", "application/problem+json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusPaymentRequired)

	problem := mpp.ErrPaymentRequired(realm, challenge.Description)
	json.NewEncoder(w).Encode(problem.ProblemDetails(""))
}

func writePaymentError(w http.ResponseWriter, err error) {
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
