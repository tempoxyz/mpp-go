package server

import (
	"context"
	"fmt"
	"maps"
	"net/http"

	"github.com/tempoxyz/mpp-go/pkg/mpp"
)

// Offer pairs an Mpp instance with the ChargeParams of one payment option.
// Pass several to Compose, ComposeRequest or ComposeMiddleware to offer
// multiple payment options for one resource. It is the preferred name for
// ComposeConfig.
type Offer = ComposeConfig

// ComposeConfig pairs an Mpp instance with the ChargeParams for one payment
// method. Pass one or more of these to ComposeMiddleware to advertise multiple
// payment options on a single route. New code can use its alias Offer.
type ComposeConfig struct {
	Mpp    *Mpp
	Params ChargeParams
}

// composedEntry is a frozen, pre-resolved config entry used at request time.
type composedEntry struct {
	mpp    *Mpp
	params ChargeParams
}

// ComposeMiddleware creates an http.Handler middleware that supports multiple
// payment methods on a single route.
//
// When no credential is present, it fans out to every configured method and
// returns a merged 402 response with one WWW-Authenticate header per method.
// When a credential is present, it dispatches to the matching method by
// comparing the credential's echoed method, intent, and canonical request.
//
// All configs must share the same realm. ComposeMiddleware panics if configs
// is empty, any Mpp is nil, or realms differ.
func ComposeMiddleware(configs ...ComposeConfig) func(http.Handler) http.Handler {
	if len(configs) == 0 {
		panic("server: ComposeMiddleware requires at least one ComposeConfig")
	}
	entries, _, err := newComposedEntries(configs, true)
	if err != nil {
		panic(err.Error())
	}
	return paymentFlow{entries: entries}.middleware()
}

// ComposeRequest runs a composed charge for an HTTP request, like
// ComposeMiddleware does for each request, reading the Payment credential,
// body and route scope from r (see Mpp.ChargeRequest). An offer whose
// Params.MppxScope is set keeps that scope.
//
// It always returns a non-nil result. Write it with its ServeHTTP method
// before anything else, as for Mpp.ChargeRequest: that writes the 402 or
// problem response, or, for a verified payment, sets the Payment-Receipt
// header for the resource the handler then writes.
func ComposeRequest(r *http.Request, offers ...Offer) (*ChargeResult, error) {
	entries, _, err := newComposedEntries(offers, false)
	return paymentFlow{entries: entries, err: err}.serveRequest(r)
}

// newComposedEntries validates configs and freezes them into entries that
// share one realm. With buildRequests it also checks that every config can
// issue its offers now, which ComposeMiddleware does once at construction.
func newComposedEntries(configs []ComposeConfig, buildRequests bool) ([]composedEntry, string, error) {
	if len(configs) == 0 {
		return nil, "", fmt.Errorf("server: compose requires at least one ComposeConfig")
	}
	if configs[0].Mpp == nil {
		return nil, "", fmt.Errorf("server: ComposeConfig[0].Mpp is nil")
	}

	realm := configs[0].Mpp.realm
	entries := make([]composedEntry, len(configs))
	for i, cfg := range configs {
		if cfg.Mpp == nil {
			return nil, "", fmt.Errorf("server: ComposeConfig[%d].Mpp is nil", i)
		}
		if cfg.Mpp.realm != realm {
			return nil, "", fmt.Errorf("server: ComposeConfig[%d] realm %q differs from [0] realm %q", i, cfg.Mpp.realm, realm)
		}
		if buildRequests {
			if _, err := cfg.Mpp.buildChargeRequests(cfg.Params); err != nil {
				return nil, "", fmt.Errorf("server: ComposeConfig[%d] buildChargeRequest: %w", i, err)
			}
		}
		entries[i] = composedEntry{
			mpp:    cfg.Mpp,
			params: cfg.Params,
		}
	}
	return entries, realm, nil
}

// Compose runs a composed charge for transports other than HTTP, such as
// MCP, that carry the Payment credential themselves. authorization is the
// credential formatted as an Authorization value ("Payment <base64url>"), or
// empty when the client sent none.
//
// Without a credential it returns every offered Challenge in offer order.
// With one it verifies it against the offer whose method, intent and request
// it echoes, like ComposeMiddleware. A malformed credential returns every
// Challenge its method and intent offer (or every Challenge) with a
// malformed-credential error. Each offer's ChargeParams are used as given:
// set Body or MppxScope there to bind them. The result is nil only when err
// is non-nil and no Challenge could be issued.
func Compose(ctx context.Context, authorization string, offers ...Offer) (*ChargeResult, error) {
	// Offers are not built up front: an offer that can no longer be issued,
	// such as a sponsored one close to its fixed Expires, must not reject a
	// Credential for another offer.
	entries, _, err := newComposedEntries(offers, false)
	if err != nil {
		return nil, err
	}
	result, err := paymentFlow{entries: entries}.run(ctx, authorization, sourceAny, nil, nil)
	if err != nil && !result.IsChallenge() {
		return nil, err
	}
	return result, err
}

// ComposeCharge runs a composed charge for transports other than HTTP.
//
// Deprecated: Use Compose instead, which takes the same arguments.
func ComposeCharge(ctx context.Context, authorization string, configs ...ComposeConfig) (*ChargeResult, error) {
	return Compose(ctx, authorization, configs...)
}

// composeRetryResult answers a credential rejected with err, such as a
// malformed one, with every Challenge its method and intent offer, or every
// Challenge when none match, and the Mpp that issued each.
func composeRetryResult(
	ctx context.Context,
	entries []composedEntry,
	cred *mpp.Credential,
	body []byte,
	scope map[string]string,
	err error,
) (*ChargeResult, []*Mpp, error) {
	var challenges []*mpp.Challenge
	var issuers []*Mpp
	if cred != nil {
		for _, entry := range findEntriesByMethodIntent(entries, cred) {
			offered, chErr := freshComposeChallenges(ctx, entry, body, scope)
			if chErr != nil {
				continue
			}
			challenges = append(challenges, offered...)
			issuers = append(issuers, issuedBy(entry.mpp, len(offered))...)
		}
	}
	if len(challenges) == 0 {
		var collectErr error
		challenges, issuers, collectErr = collectComposeChallenges(ctx, entries, body, scope)
		if collectErr != nil {
			return failedFlow(composeChallengeCollectionError(err, collectErr))
		}
	}
	result, err := challengeResult(challenges, err)
	return result, issuers, err
}

// collectComposeChallenges issues every offer's Challenges, in offer order,
// and returns the Mpp that issued each. It fails when any offer fails.
func collectComposeChallenges(ctx context.Context, entries []composedEntry, body []byte, scope map[string]string) ([]*mpp.Challenge, []*Mpp, error) {
	var challenges []*mpp.Challenge
	var issuers []*Mpp
	for _, entry := range entries {
		offered, err := freshComposeChallenges(ctx, entry, body, scope)
		if err != nil {
			return nil, nil, err
		}
		challenges = append(challenges, offered...)
		issuers = append(issuers, issuedBy(entry.mpp, len(offered))...)
	}
	if len(challenges) == 0 {
		return nil, nil, errNoChallenges
	}
	return challenges, issuers, nil
}

// freshComposeChallenges issues every Challenge offered by one compose entry.
// The flow reports them to ChallengeCreated once it returns them.
func freshComposeChallenges(ctx context.Context, entry composedEntry, body []byte, scope map[string]string) ([]*mpp.Challenge, error) {
	params := entry.requestParams("", sourceAny, scope, body)
	result, err := entry.mpp.chargeWithEvents(ctx, params, false)
	if err != nil {
		return nil, err
	}
	return result.Challenges, nil
}

// requestParams returns the entry's ChargeParams bound to one request: the
// Payment credential, the request scope unless the params set their own, and
// a non-empty body.
func (entry composedEntry) requestParams(credential string, source credentialSource, scope map[string]string, body []byte) ChargeParams {
	params := entry.params
	params.Authorization, params.PaymentAuthorization = credentialFields(credential, source)
	if len(scope) > 0 && len(params.MppxScope) == 0 {
		params.MppxScope = scope
	}
	if len(body) > 0 {
		params.Body = body
	}
	return params
}

// credentialFields returns the Authorization and Payment-Authorization values
// that carry credential as it arrived from source: only the field it was sent
// in, or both for a transport other than HTTP.
func credentialFields(credential string, source credentialSource) (authorization, paymentAuthorization string) {
	switch source {
	case sourceAuthorization:
		return credential, ""
	case sourcePaymentAuthorization:
		return "", credential
	default:
		return credential, credential
	}
}

// composeChallengeCollectionError reports that no challenge could be issued in
// answer to a malformed credential. The collection failure is the primary
// error: the client's malformed-credential PaymentError is kept only as text,
// so the response is the server failure, not a client error with no offer.
func composeChallengeCollectionError(malformed, collectErr error) error {
	return fmt.Errorf("server: issuing challenges after malformed credential (%v): %w", malformed, collectErr)
}

// findEntriesByMethodIntent returns every compose entry whose method and intent
// match the credential. Multiple configs can share method+intent while differing
// by amount, currency, or opaque metadata; reissue all of them so the client can
// retry the option it originally selected.
func findEntriesByMethodIntent(entries []composedEntry, cred *mpp.Credential) []composedEntry {
	var matched []composedEntry
	for _, entry := range entries {
		method := entry.mpp.method
		if cred.Challenge.Method != method.Name() {
			continue
		}
		if _, ok := method.Intents()[cred.Challenge.Intent]; !ok {
			continue
		}
		matched = append(matched, entry)
	}
	return matched
}

// findMatchingEntry selects the entry whose method, intent, and canonical
// request match the credential. This allows multiple entries with the same
// method+intent but different amounts, currencies, or opaque metadata.
// authorization is the Payment credential cred was parsed from; offers are
// built against it as received, not a re-encoding of cred, which can grow
// past the header size limit. Only entries that read their credential from
// source are considered. Without an exact match it falls back to an entry
// for the credential's method and intent, then for its method alone, whose
// Charge then rejects the credential with retry Challenges.
func findMatchingEntry(entries []composedEntry, cred *mpp.Credential, authorization string, source credentialSource, scope map[string]string) (composedEntry, bool, error) {
	echoedRequest, err := echoedRequestMap(cred)
	if err != nil {
		return composedEntry{}, false, mpp.ErrMalformedCredential(fmt.Sprintf("invalid echoed request: %v", err)).WithCause(err)
	}

	// Prefer an exact match on method + intent + request.
	for _, entry := range entries {
		if !source.accepts(entry.mpp) || !entry.offersMethodIntent(cred) {
			continue
		}
		requests, err := entry.scopedRequests(scope, authorization, source)
		if err != nil {
			return composedEntry{}, false, err
		}
		if !maps.Equal(applicationMeta(cred.Challenge.Opaque), entry.params.Meta) {
			continue
		}
		for _, request := range requests {
			if mpp.ChallengeBoundJSONEqual(echoedRequest, request) {
				return entry, true, nil
			}
		}
	}

	// Fall back to method + intent, then to method only (let Charge return
	// the precise error with retry Challenges).
	for _, entry := range entries {
		if source.accepts(entry.mpp) && entry.offersMethodIntent(cred) {
			return entry, true, nil
		}
	}
	for _, entry := range entries {
		if source.accepts(entry.mpp) && cred.Challenge.Method == entry.mpp.method.Name() {
			return entry, true, nil
		}
	}

	return composedEntry{}, false, nil
}

// offersMethodIntent reports whether the entry offers the credential's
// method and intent.
func (entry composedEntry) offersMethodIntent(cred *mpp.Credential) bool {
	method := entry.mpp.method
	if cred.Challenge.Method != method.Name() {
		return false
	}
	_, ok := method.Intents()[cred.Challenge.Intent]
	return ok
}

// offersMethod reports whether any entry offers the credential's method,
// whatever field it reads its credential from.
func offersMethod(entries []composedEntry, cred *mpp.Credential) bool {
	for _, entry := range entries {
		if cred.Challenge.Method == entry.mpp.method.Name() {
			return true
		}
	}
	return false
}

// scopedRequests builds an entry's offers to match against the Payment
// credential in authorization. Building them with the Credential present
// skips issuance-only checks, such as the sponsored-expiry floor, that do not
// apply to verifying a Credential.
func (entry composedEntry) scopedRequests(scope map[string]string, authorization string, source credentialSource) ([]map[string]any, error) {
	params := entry.params
	if len(scope) > 0 && len(params.MppxScope) == 0 {
		params.MppxScope = scope
	}
	if authorization != "" {
		params.Authorization, params.PaymentAuthorization = credentialFields(authorization, source)
	}
	return entry.mpp.buildChargeRequests(params)
}

// composePaymentCredential returns the one Payment credential header carries
// in the fields the entries read it from: Authorization, and
// Payment-Authorization for entries created WithRequiresAuth, and the field
// it arrived in.
func composePaymentCredential(entries []composedEntry, header http.Header) (string, credentialSource, error) {
	needsDefault := false
	needsAuth := false
	for _, entry := range entries {
		if entry.mpp.requiresAuth {
			needsAuth = true
		} else {
			needsDefault = true
		}
	}

	var authorization, paymentAuthorization string
	var err error
	if needsDefault {
		authorization, err = mpp.FindPaymentAuthorizationStrict(header.Get(mpp.HeaderAuthorization))
		if err != nil {
			return "", sourceAny, err
		}
	}
	if needsAuth {
		paymentAuthorization, err = mpp.FindPaymentAuthorizationStrict(header.Get(mpp.HeaderPaymentAuthorization))
		if err != nil {
			return "", sourceAny, err
		}
	}
	if authorization != "" && paymentAuthorization != "" {
		return "", sourceAny, fmt.Errorf("mpp: multiple Payment credentials")
	}
	if paymentAuthorization != "" {
		return paymentAuthorization, sourcePaymentAuthorization, nil
	}
	return authorization, sourceAuthorization, nil
}
