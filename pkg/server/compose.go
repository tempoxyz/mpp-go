package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"

	"github.com/tempoxyz/mpp-go/pkg/mpp"
)

// ComposeConfig pairs an Mpp instance with the ChargeParams for one payment
// method. Pass one or more of these to ComposeMiddleware to advertise multiple
// payment options on a single route.
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
	if configs[0].Mpp == nil {
		panic("server: ComposeConfig[0].Mpp is nil")
	}

	realm := configs[0].Mpp.realm
	entries := make([]composedEntry, len(configs))
	for i, cfg := range configs {
		if cfg.Mpp == nil {
			panic(fmt.Sprintf("server: ComposeConfig[%d].Mpp is nil", i))
		}
		if cfg.Mpp.realm != realm {
			panic(fmt.Sprintf("server: ComposeConfig[%d] realm %q differs from [0] realm %q", i, cfg.Mpp.realm, realm))
		}
		if _, err := cfg.Mpp.buildChargeRequest(cfg.Params); err != nil {
			panic(fmt.Sprintf("server: ComposeConfig[%d] buildChargeRequest: %v", i, err))
		}
		entries[i] = composedEntry{
			mpp:    cfg.Mpp,
			params: cfg.Params,
		}
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			paymentAuth, err := composePaymentCredential(entries, r)
			if err != nil {
				WritePaymentError(w, mpp.ErrBadRequest(err.Error()))
				return
			}
			body, err := ReadRequestBody(r)
			if err != nil {
				WritePaymentError(w, mpp.ErrBadRequest("failed to read request body"))
				return
			}
			scope := ScopeFromHTTPRequest(r, "")

			// No credential — fan out and merge all challenges.
			if paymentAuth == "" {
				composeChallenges(w, r, entries, realm, body, scope)
				return
			}

			// Credential present — find the matching entry.
			cred, err := mpp.ParseCredential(paymentAuth)
			if err != nil {
				writeComposeMalformedCredentialError(w, r, entries, realm, body, scope, nil, mpp.ErrMalformedCredential(err.Error()))
				return
			}

			entry, ok, err := findMatchingEntry(entries, cred, scope)
			if err != nil {
				if isMalformedCredential(err) {
					writeComposeMalformedCredentialError(w, r, entries, realm, body, scope, cred, err)
					return
				}
				WritePaymentError(w, err)
				return
			}
			if !ok {
				WritePaymentError(w, mpp.ErrMethodUnsupported(cred.Challenge.Method+"/"+cred.Challenge.Intent))
				return
			}

			params := entry.params
			params.Authorization = r.Header.Get(mpp.HeaderAuthorization)
			params.PaymentAuthorization = r.Header.Get(mpp.HeaderPaymentAuthorization)
			if len(scope) > 0 {
				params.MppxScope = scope
			}
			if len(body) > 0 {
				params.Body = body
			}

			result, err := entry.mpp.Charge(r.Context(), params)
			if err != nil {
				if result != nil && result.Challenge != nil {
					WritePaymentErrorWithChallenge(w, err, result.Challenge, realm)
					return
				}
				WritePaymentError(w, err)
				return
			}
			if result.Challenge != nil {
				WriteChallenge(w, result.Challenge, realm)
				return
			}

			serveVerified(next, w, r, result.Credential, result.Receipt)
		})
	}
}

// composeChallenges issues a 402 with all configured challenges merged into
// separate WWW-Authenticate header values.
func composeChallenges(w http.ResponseWriter, r *http.Request, entries []composedEntry, realm string, body []byte, scope map[string]string) {
	challenges, err := collectComposeChallenges(r.Context(), entries, body, scope)
	if err != nil {
		WritePaymentError(w, err)
		return
	}
	writeComposeChallengeResponse(w, challenges, realm, nil)
}

func collectComposeChallenges(ctx context.Context, entries []composedEntry, body []byte, scope map[string]string) ([]*mpp.Challenge, error) {
	var challenges []*mpp.Challenge
	for _, entry := range entries {
		challenge, err := freshComposeChallenge(ctx, entry, body, scope)
		if err != nil {
			return nil, err
		}
		if challenge != nil {
			challenges = append(challenges, challenge)
		}
	}
	if len(challenges) == 0 {
		return nil, mpp.ErrBadRequest("no challenges could be generated")
	}
	return challenges, nil
}

func freshComposeChallenge(ctx context.Context, entry composedEntry, body []byte, scope map[string]string) (*mpp.Challenge, error) {
	params := entry.params
	params.Authorization = ""
	if len(scope) > 0 {
		params.MppxScope = scope
	}
	if len(body) > 0 {
		params.Body = body
	}

	result, err := entry.mpp.Charge(ctx, params)
	if err != nil {
		return nil, err
	}
	return result.Challenge, nil
}

func writeComposeMalformedCredentialError(
	w http.ResponseWriter,
	r *http.Request,
	entries []composedEntry,
	realm string,
	body []byte,
	scope map[string]string,
	cred *mpp.Credential,
	err error,
) {
	var challenges []*mpp.Challenge
	if cred != nil {
		for _, entry := range findEntriesByMethodIntent(entries, cred) {
			challenge, chErr := freshComposeChallenge(r.Context(), entry, body, scope)
			if chErr != nil || challenge == nil {
				continue
			}
			challenges = append(challenges, challenge)
		}
	}
	if len(challenges) == 0 {
		var collectErr error
		challenges, collectErr = collectComposeChallenges(r.Context(), entries, body, scope)
		if collectErr != nil {
			WritePaymentError(w, err)
			return
		}
	}
	writeComposeChallengeResponse(w, challenges, realm, err)
}

func writeComposeChallengeResponse(w http.ResponseWriter, challenges []*mpp.Challenge, realm string, err error) {
	// Serialize every challenge before writing headers so a later
	// ToAuthenticateStrict failure cannot leave a partial usable set.
	headers := make([]string, 0, len(challenges))
	for _, challenge := range challenges {
		header, headerErr := challenge.ToAuthenticateStrict(realm)
		if headerErr != nil {
			WritePaymentError(w, mpp.ErrBadRequest(headerErr.Error()))
			return
		}
		headers = append(headers, header)
	}
	for _, header := range headers {
		w.Header().Add(mpp.HeaderWWWAuthenticate, header)
	}

	if err != nil {
		WritePaymentError(w, err)
		return
	}

	w.Header().Set("Content-Type", "application/problem+json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusPaymentRequired)

	problem := mpp.ErrPaymentRequired(realm, "")
	json.NewEncoder(w).Encode(problem.ProblemDetails(""))
}

func isMalformedCredential(err error) bool {
	pe, ok := err.(*mpp.PaymentError)
	return ok && pe.Type == mpp.ErrorTypeMalformedCredential
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
func findMatchingEntry(entries []composedEntry, cred *mpp.Credential, scope map[string]string) (composedEntry, bool, error) {
	echoedRequest, err := echoedRequestMap(cred)
	if err != nil {
		return composedEntry{}, false, mpp.ErrMalformedCredential(fmt.Sprintf("invalid echoed request: %v", err))
	}

	// Prefer an exact match on method + intent + request.
	for _, entry := range entries {
		method := entry.mpp.method
		if cred.Challenge.Method != method.Name() {
			continue
		}
		if _, ok := method.Intents()[cred.Challenge.Intent]; !ok {
			continue
		}
		request, err := entry.scopedRequest(scope)
		if err != nil {
			return composedEntry{}, false, err
		}
		if mpp.ChallengeBoundJSONEqual(echoedRequest, request) && reflect.DeepEqual(cred.Challenge.Opaque, entry.params.Meta) {
			return entry, true, nil
		}
	}

	// Fall back to method + intent only (let Charge return the precise error).
	for _, entry := range entries {
		method := entry.mpp.method
		if cred.Challenge.Method != method.Name() {
			continue
		}
		if _, ok := method.Intents()[cred.Challenge.Intent]; !ok {
			continue
		}
		return entry, true, nil
	}

	return composedEntry{}, false, nil
}

func (entry composedEntry) scopedRequest(scope map[string]string) (map[string]any, error) {
	params := entry.params
	if len(scope) > 0 {
		params.MppxScope = scope
	}
	return entry.mpp.buildChargeRequest(params)
}

func composePaymentCredential(entries []composedEntry, r *http.Request) (string, error) {
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
		authorization, err = mpp.FindPaymentAuthorizationStrict(r.Header.Get(mpp.HeaderAuthorization))
		if err != nil {
			return "", err
		}
	}
	if needsAuth {
		paymentAuthorization, err = mpp.FindPaymentAuthorizationStrict(r.Header.Get(mpp.HeaderPaymentAuthorization))
		if err != nil {
			return "", err
		}
	}
	if authorization != "" && paymentAuthorization != "" {
		return "", fmt.Errorf("mpp: multiple Payment credentials")
	}
	if paymentAuthorization != "" {
		return paymentAuthorization, nil
	}
	return authorization, nil
}
