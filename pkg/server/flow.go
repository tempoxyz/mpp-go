package server

import (
	"context"
	"errors"
	"net/http"

	"github.com/tempoxyz/mpp-go/pkg/mpp"
)

// paymentFlow is the framework-neutral charge flow behind ChargeMiddleware,
// ComposeMiddleware, ChargeRequest, ComposeRequest, Compose and the Gin, Echo
// and Fiber adapters. A single-offer flow behaves as a composition of one.
//
// The flow returns a *ChargeResult: a verified (Credential, Receipt) pair the
// transport serves the resource for, or Challenges and an error that
// ChargeResult.ServeHTTP writes as the 402 or problem response.
type paymentFlow struct {
	entries []composedEntry
	// err is a configuration error every request reports.
	err error
}

// errNoChargeResult reports a nil *ChargeResult written as a response.
var errNoChargeResult = errors.New("server: no charge result")

// middleware serves next for every request the flow verifies a payment for,
// and writes the payment response for every other request.
func (f paymentFlow) middleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			result, err := f.serveRequest(r)
			if err != nil || result.IsChallenge() {
				result.ServeHTTP(w, r)
				return
			}
			serveVerified(next, w, r, result.Credential, result.Receipt)
		})
	}
}

// serveRequest runs the flow for an HTTP request: the Payment credential from
// its headers, its body (restored for the protected handler) and its scope.
// It always returns a non-nil result.
func (f paymentFlow) serveRequest(r *http.Request) (*ChargeResult, error) {
	body, err := ReadRequestBody(r)
	if err != nil {
		return failedResult(RequestBodyError(err))
	}
	if f.err != nil {
		return failedResult(f.err)
	}
	credential, err := composePaymentCredential(f.entries, r.Header)
	if err != nil {
		// The client sent a Payment credential that cannot be read, which a
		// direct Charge reports to PaymentFailed too.
		err = mpp.ErrBadRequest(err.Error())
		f.paymentFailed(r.Context(), nil, err)
		return failedResult(err)
	}
	return f.run(r.Context(), credential, ScopeFromHTTPRequest(r, ""), body)
}

// run verifies credential, the Payment credential ("Payment <base64url>") or
// empty when the client sent none, against the offer it echoes, binding scope
// and body into every offer that sets no scope of its own. It always returns
// a non-nil result whose err field is the returned error.
func (f paymentFlow) run(ctx context.Context, credential string, scope map[string]string, body []byte) (*ChargeResult, error) {
	if f.err != nil {
		return failedResult(f.err)
	}
	if credential == "" {
		challenges, err := collectComposeChallenges(ctx, f.entries, body, scope)
		if err != nil {
			return failedResult(err)
		}
		return challengeResult(challenges, nil)
	}

	cred, err := mpp.ParseCredential(credential)
	if err != nil {
		err = mpp.ErrMalformedCredential(err.Error())
		f.paymentFailed(ctx, nil, err)
		return composeMalformedCredentialResult(ctx, f.entries, nil, body, scope, err)
	}
	entry, ok, err := findMatchingEntry(f.entries, cred, credential, scope)
	if err != nil {
		f.paymentFailed(ctx, cred, err)
		if isMalformedCredential(err) {
			return composeMalformedCredentialResult(ctx, f.entries, cred, body, scope, err)
		}
		return failedResult(err)
	}
	if !ok {
		err := mpp.ErrMethodUnsupported(cred.Challenge.Method + "/" + cred.Challenge.Intent)
		f.paymentFailed(ctx, cred, err)
		return failedResult(err)
	}

	result, err := entry.mpp.Charge(ctx, entry.requestParams(credential, scope, body))
	if result == nil {
		return failedResult(err)
	}
	return result, err
}

// paymentFailed reports a Credential the flow rejected before dispatching it
// to an offer's Charge, to the PaymentFailed hook of every distinct Mpp.
func (f paymentFlow) paymentFailed(ctx context.Context, credential *mpp.Credential, err error) {
	payments := make([]*Mpp, 0, len(f.entries))
	for _, entry := range f.entries {
		payments = append(payments, entry.mpp)
	}
	paymentFailedOnce(ctx, payments, credential, err)
}

// paymentFailedOnce calls the PaymentFailed hook of every distinct, non-nil
// Mpp in payments once.
func paymentFailedOnce(ctx context.Context, payments []*Mpp, credential *mpp.Credential, err error) {
	seen := make(map[*Mpp]bool, len(payments))
	for _, payment := range payments {
		if payment == nil || seen[payment] {
			continue
		}
		seen[payment] = true
		payment.paymentFailed(ctx, credential, err)
	}
}

func failedResult(err error) (*ChargeResult, error) {
	return &ChargeResult{err: err}, err
}

func challengeResult(challenges []*mpp.Challenge, err error) (*ChargeResult, error) {
	return &ChargeResult{Challenge: challenges[0], Challenges: challenges, err: err}, err
}

func isMalformedCredential(err error) bool {
	return errors.Is(err, mpp.ErrorTypeMalformedCredential)
}

// ChargeRequest runs the charge for an HTTP request, as ChargeMiddleware does
// for each request. It reads the Payment credential from the Authorization
// header (Payment-Authorization for a handler created WithRequiresAuth), the
// body (restoring r.Body for the handler) and, unless params.MppxScope is
// set, the route scope (see ScopeFromHTTPRequest), overriding the matching
// params fields.
//
// It always returns a non-nil result: when err is non-nil or the result
// IsChallenge, write it with its ServeHTTP method. Otherwise the payment is
// verified and the result carries its Credential and Receipt.
func (m *Mpp) ChargeRequest(r *http.Request, params ChargeParams) (*ChargeResult, error) {
	return m.flow(params).serveRequest(r)
}

func (m *Mpp) flow(params ChargeParams) paymentFlow {
	if m == nil {
		return paymentFlow{err: errors.New("server: Mpp is nil")}
	}
	return paymentFlow{entries: []composedEntry{{mpp: m, params: params}}}
}
