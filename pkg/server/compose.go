package server

import (
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
			auth := r.Header.Get("Authorization")
			paymentAuth := mpp.FindPaymentAuthorization(auth)
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
				WritePaymentError(w, mpp.ErrMalformedCredential(err.Error()))
				return
			}

			entry, ok, err := findMatchingEntry(entries, cred, scope)
			if err != nil {
				WritePaymentError(w, err)
				return
			}
			if !ok {
				WritePaymentError(w, mpp.ErrMethodUnsupported(cred.Challenge.Method+"/"+cred.Challenge.Intent))
				return
			}

			params := entry.params
			params.Authorization = paymentAuth
			if len(scope) > 0 {
				params.MppxScope = scope
			}
			if len(body) > 0 {
				params.Body = body
			}

			result, err := entry.mpp.Charge(r.Context(), params)
			if err != nil {
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
	var challenges []*mpp.Challenge
	for _, entry := range entries {
		params := entry.params
		params.Authorization = ""
		if len(scope) > 0 {
			params.MppxScope = scope
		}
		if len(body) > 0 {
			params.Body = body
		}

		result, err := entry.mpp.Charge(r.Context(), params)
		if err != nil {
			WritePaymentError(w, err)
			return
		}
		if result.Challenge != nil {
			challenges = append(challenges, result.Challenge)
		}
	}

	if len(challenges) == 0 {
		WritePaymentError(w, mpp.ErrBadRequest("no challenges could be generated"))
		return
	}

	var headers []string
	for _, challenge := range challenges {
		header, err := challenge.ToAuthenticateStrict(realm)
		if err != nil {
			WritePaymentError(w, mpp.ErrBadRequest(err.Error()))
			return
		}
		headers = append(headers, header)
	}

	for _, header := range headers {
		w.Header().Add("WWW-Authenticate", header)
	}
	w.Header().Set("Content-Type", "application/problem+json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusPaymentRequired)

	problem := mpp.ErrPaymentRequired(realm, "")
	json.NewEncoder(w).Encode(problem.ProblemDetails(""))
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
		if mpp.JSONEqual(echoedRequest, request) && reflect.DeepEqual(cred.Challenge.Opaque, entry.params.Meta) {
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
