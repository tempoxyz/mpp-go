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
	credential, source, err := composePaymentCredential(f.entries, r.Header)
	if err != nil {
		// The client sent a Payment credential that cannot be read, which a
		// direct Charge reports to PaymentFailed too.
		err = mpp.ErrBadRequest(err.Error())
		f.paymentFailed(r.Context(), nil, err)
		return failedResult(err)
	}
	return f.run(r.Context(), credential, source, ScopeFromHTTPRequest(r, ""), body)
}

// credentialSource is the field a Payment credential arrived in.
type credentialSource int

const (
	// sourceAny is a credential a transport other than HTTP carries, such as
	// MCP, which every offer accepts.
	sourceAny credentialSource = iota
	// sourceAuthorization is the Authorization header, which only offers
	// whose Mpp is not created WithRequiresAuth accept.
	sourceAuthorization
	// sourcePaymentAuthorization is the Payment-Authorization header, which
	// only offers whose Mpp is created WithRequiresAuth accept.
	sourcePaymentAuthorization
)

// accepts reports whether m reads its Payment credential from source.
func (s credentialSource) accepts(m *Mpp) bool {
	switch s {
	case sourceAuthorization:
		return !m.requiresAuth
	case sourcePaymentAuthorization:
		return m.requiresAuth
	default:
		return true
	}
}

// run verifies credential, the Payment credential ("Payment <base64url>") or
// empty when the client sent none, against the offer it echoes, binding scope
// and body into every offer that sets no scope of its own. source is the
// field credential arrived in; an offer that reads its credential from
// another field never accepts it. It always returns a non-nil result whose
// err field is the returned error.
//
// It reports ChallengeCreated for the Challenges the result returns, and only
// once the whole set is issued and can be serialized.
func (f paymentFlow) run(ctx context.Context, credential string, source credentialSource, scope map[string]string, body []byte) (*ChargeResult, error) {
	result, issuers, err := f.charge(ctx, credential, source, scope, body)
	if challenges := result.offers(); len(issuers) == len(challenges) && challengesDelivered(err, challenges) {
		for i, challenge := range challenges {
			issuers[i].challengesCreated(ctx, []*mpp.Challenge{challenge})
		}
	}
	return result, err
}

// charge runs the flow, returning with the result the Mpp that issued each
// Challenge it offers.
func (f paymentFlow) charge(ctx context.Context, credential string, source credentialSource, scope map[string]string, body []byte) (*ChargeResult, []*Mpp, error) {
	if f.err != nil {
		return failedFlow(f.err)
	}
	if credential == "" {
		challenges, issuers, err := collectComposeChallenges(ctx, f.entries, body, scope)
		if err != nil {
			return failedFlow(err)
		}
		result, err := challengeResult(challenges, nil)
		return result, issuers, err
	}

	cred, err := mpp.ParseCredential(credential)
	if err != nil {
		err = mpp.ErrMalformedCredential(err.Error())
		f.paymentFailed(ctx, nil, err)
		return composeRetryResult(ctx, f.entries, nil, body, scope, err)
	}
	entry, ok, err := findMatchingEntry(f.entries, cred, credential, source, scope, body)
	if err != nil {
		f.paymentFailed(ctx, cred, err)
		if isMalformedCredential(err) {
			return composeRetryResult(ctx, f.entries, cred, body, scope, err)
		}
		return failedFlow(err)
	}
	if !ok {
		if offersMethod(f.entries, cred) {
			// The offer for this method reads its credential from the other
			// header: reject it with that offer's Challenges, which name the
			// header to send it in.
			err := mpp.ErrInvalidChallenge(cred.Challenge.ID, "Payment credential sent in a header this offer does not read")
			f.paymentFailed(ctx, cred, err)
			return composeRetryResult(ctx, f.entries, cred, body, scope, err)
		}
		err := mpp.ErrMethodUnsupported(cred.Challenge.Method + "/" + cred.Challenge.Intent)
		f.paymentFailed(ctx, cred, err)
		return failedFlow(err)
	}

	result, err := entry.mpp.chargeWithEvents(ctx, entry.requestParams(credential, source, scope, body), false)
	if result == nil {
		return failedFlow(err)
	}
	return result, issuedBy(entry.mpp, len(result.offers())), err
}

func failedFlow(err error) (*ChargeResult, []*Mpp, error) {
	result, err := failedResult(err)
	return result, nil, err
}

// issuedBy returns n issuers, all m.
func issuedBy(m *Mpp, n int) []*Mpp {
	issuers := make([]*Mpp, n)
	for i := range issuers {
		issuers[i] = m
	}
	return issuers
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
// It always returns a non-nil result. Write it with its ServeHTTP method
// before anything else: when err is non-nil or the result IsChallenge, that
// writes the 402 or problem response. Otherwise the payment is verified, the
// result carries its Credential and Receipt, and ServeHTTP sets the
// Payment-Receipt and Cache-Control: private headers for the resource the
// handler then writes.
func (m *Mpp) ChargeRequest(r *http.Request, params ChargeParams) (*ChargeResult, error) {
	return m.flow(params).serveRequest(r)
}

func (m *Mpp) flow(params ChargeParams) paymentFlow {
	if m == nil {
		return paymentFlow{err: errors.New("server: Mpp is nil")}
	}
	return paymentFlow{entries: []composedEntry{{mpp: m, params: params}}}
}
