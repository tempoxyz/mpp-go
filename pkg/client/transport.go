package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/tempoxyz/mpp-go/pkg/internal/tempoproof"
	"github.com/tempoxyz/mpp-go/pkg/mpp"
)

// Transport is an http.RoundTripper that handles 402 Payment Required responses.
// It wraps an inner transport and automatically negotiates payment.
type Transport struct {
	methods            map[methodKey][]Method
	inner              http.RoundTripper
	paymentPreferences []paymentPreference
	acceptPayment      string
	configErr          error
	onChallenge        OnChallengeFunc
	maxRetries         int
	orderChallenges    OrderChallenges
	// payRedirects lets a Transport driven without a Client pay a redirected
	// request when it stays on the origin of the original request. Only
	// NewHTTPClient sets it, since its CheckRedirect refuses other origins.
	payRedirects bool
}

const defaultMaxPaymentRetries = 3

type credentialKey struct {
	id     string
	method methodKey
}

type paymentStateContextKey struct{}

// paymentState is shared, through the request context, between one Client.Do
// call, its CheckRedirect hook and the Transport rounds it drives.
type paymentState struct {
	// origin is the origin the caller asked Client.Do to fetch.
	origin string

	mu sync.Mutex
	// unknown is the last PaymentOutcomeUnknownError the Transport returned,
	// kept so Client.Do can restore it when net/http replaces the error.
	unknown *PaymentOutcomeUnknownError
}

type transportConfig struct {
	paymentPreferences PaymentPreferences
	onChallenge        OnChallengeFunc
	maxRetries         *int
	orderChallenges    OrderChallenges
}

// OnChallengeFunc is called when the transport has selected a 402 challenge it
// can pay, before any credential is created. It mirrors mppx's onChallenge.
//
// createCredential creates a credential with the selected method; ctx can carry
// method-specific context for this request. It runs the method at most once per
// challenge: later calls return the first result. The hook can:
//
//   - return (nil, nil) to let the transport create the credential itself, or
//     to use the one createCredential already made,
//   - return a credential, from createCredential or built another way, to send it,
//   - return an error to decline the payment: RoundTrip returns that error,
//     wrapped, and does not pay. It matches ErrPaymentDeclined only when no
//     credential was created: after createCredential succeeded the payment
//     may already have been made, so the error is not reported as a decline.
//
// It runs once per challenge: a credential the transport already created for
// the same challenge is reused without calling it again.
type OnChallengeFunc func(
	ctx context.Context,
	challenge *mpp.Challenge,
	createCredential func(ctx context.Context) (*mpp.Credential, error),
) (*mpp.Credential, error)

func newTransport(methods []Method, inner http.RoundTripper, config *transportConfig) *Transport {
	if inner == nil {
		inner = http.DefaultTransport
	}
	m := make(map[methodKey][]Method, len(methods))
	for _, method := range methods {
		key := keyForMethod(method)
		m[key] = append(m[key], method)
	}
	preferences, header, err := resolvePaymentPreferences(methods, config.paymentPreferences)
	maxRetries := defaultMaxPaymentRetries
	if config.maxRetries != nil {
		maxRetries = *config.maxRetries
		if maxRetries < 0 && err == nil {
			err = fmt.Errorf("mpp: max payment retries must not be negative, got %d", maxRetries)
		}
	}
	return &Transport{
		methods:            m,
		inner:              inner,
		paymentPreferences: preferences,
		acceptPayment:      header,
		configErr:          err,
		onChallenge:        config.onChallenge,
		maxRetries:         maxRetries,
		orderChallenges:    config.orderChallenges,
	}
}

// RoundTrip implements http.RoundTripper with automatic 402 handling.
func (t *Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	if t.configErr != nil {
		closeRequestBody(req)
		return nil, t.configErr
	}

	baseRequest, preferences := t.prepareRequest(req)
	credentials := make(map[credentialKey]*mpp.Credential)
	request := baseRequest
	var sentCredential credentialKey
	// A credential the caller set on the request is sent with the first
	// round, so its outcome is as unknown as one the Transport created.
	sent, sentChallenge, sentCred := callerCredential(baseRequest.Header)

	for retries := 0; ; retries++ {
		resp, err := t.inner.RoundTrip(request)
		if err != nil {
			if sent {
				// The server may have settled the payment before the
				// connection failed: retrying could pay twice.
				unknown := &PaymentOutcomeUnknownError{
					Challenge:  sentChallenge,
					Credential: sentCred,
					Err:        err,
				}
				if state := paymentStateFrom(req.Context()); state != nil {
					state.mu.Lock()
					state.unknown = unknown
					state.mu.Unlock()
				}
				return nil, unknown
			}
			return nil, err
		}
		if resp.StatusCode != http.StatusPaymentRequired || retries >= t.maxRetries {
			return resp, nil
		}

		problem := responseProblemType(resp)
		if problem == mpp.ErrorTypePaymentOutcomeUnknown {
			// The server cannot tell whether the credential settled. Paying
			// again could pay twice: hand the response to the caller.
			return resp, nil
		}

		challenges, errs := t.parseChallenges(resp.Header)
		_ = errs // Non-Payment or malformed headers are silently skipped.
		candidates := t.challengeCandidates(challenges, preferences, time.Now().UTC())
		if len(candidates) > 0 && t.orderChallenges != nil {
			candidates, err = t.orderCandidates(baseRequest.Context(), candidates)
			if err != nil {
				drainAndClose(resp.Body)
				return nil, err
			}
		}
		if len(candidates) == 0 {
			return resp, nil
		}
		var selected challengeCandidate
		var rejected error
		for _, candidate := range candidates {
			if err := t.validatePaymentOrigin(request, candidate); err != nil {
				rejected = err
				continue
			}
			selected = candidate
			break
		}
		if selected.challenge == nil {
			drainAndClose(resp.Body)
			return nil, rejected
		}

		if problem == mpp.ErrorTypeInvalidPayload {
			delete(credentials, sentCredential)
		}
		drainAndClose(resp.Body)

		// Prepare the retry before paying for it. A credential created for a
		// request that cannot be sent again is spent without ever reaching
		// the server.
		retry, err := t.cloneRequest(baseRequest)
		if err != nil {
			// The inner RoundTripper already received, and closed, the
			// request body: closing it again here would double-close it.
			return nil, fmt.Errorf("mpp: cloning request for retry: %w", err)
		}

		key := credentialKey{
			id: selected.challenge.ID,
			method: methodKey{
				name:   selected.challenge.Method,
				intent: selected.challenge.Intent,
			},
		}
		cred := credentials[key]
		if cred == nil {
			cred, err = t.createCredential(baseRequest.Context(), selected)
			if err != nil {
				if retry.Body != nil {
					_ = retry.Body.Close()
				}
				return nil, err
			}
			credentials[key] = cred
		}

		authorization, err := mpp.FormatCredential(cred)
		if err != nil {
			if retry.Body != nil {
				_ = retry.Body.Close()
			}
			return nil, err
		}
		retry.Header.Set(selected.challenge.CredentialHeader(), authorization)
		request = retry
		sentCredential = key
		sent, sentChallenge, sentCred = true, selected.challenge, cred
	}
}

// PaymentOutcomeUnknownError is returned by Transport.RoundTrip, and so by
// Client.Do wrapped in a *url.Error, when the request carrying a payment
// credential failed without a response. That includes a credential the caller
// set on the request itself (any header whose value uses the Payment scheme).
// The server may have settled the payment, so do not blindly retry: reconcile
// it using Challenge and Credential. It matches mppx's
// PaymentOutcomeUnknownError.
//
// When an http.Client with a Timeout times out, net/http replaces the
// RoundTrip error with its own timeout error, so a bare Transport inside such
// a client cannot preserve this type. client.Client restores it: its Do
// returns a *url.Error whose Err is a PaymentOutcomeUnknownError wrapping the
// timeout, and Timeout still reports true.
type PaymentOutcomeUnknownError struct {
	// Challenge is the challenge the credential paid for. For a credential
	// the caller set on the request, it holds the challenge fields the
	// credential echoes (the request in RequestB64), or is nil when the
	// credential cannot be parsed.
	Challenge *mpp.Challenge
	// Credential is the credential sent with the failed request, or nil
	// when a caller-set credential cannot be parsed.
	Credential *mpp.Credential
	// Err is the error the paid request failed with.
	Err error
}

func (e *PaymentOutcomeUnknownError) Error() string {
	id := ""
	if e.Challenge != nil {
		id = e.Challenge.ID
	}
	msg := fmt.Sprintf("mpp: payment outcome is unknown after sending a credential for challenge %q; do not blindly retry", id)
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
}

// Unwrap returns the error the paid request failed with.
func (e *PaymentOutcomeUnknownError) Unwrap() error { return e.Err }

// Timeout reports whether the paid request failed because it timed out.
func (e *PaymentOutcomeUnknownError) Timeout() bool {
	var timeout interface{ Timeout() bool }
	return errors.As(e.Err, &timeout) && timeout.Timeout()
}

// callerCredential reports whether header carries a Payment credential the
// caller set, in any field, and parses it when it can.
func callerCredential(header http.Header) (bool, *mpp.Challenge, *mpp.Credential) {
	value, ok := paymentCredentialValue(header)
	if !ok {
		return false, nil, nil
	}
	cred, err := mpp.ParseCredential(value)
	if err != nil {
		return true, nil, nil
	}
	echo := cred.Challenge
	return true, &mpp.Challenge{
		ID:         echo.ID,
		Method:     echo.Method,
		Intent:     echo.Intent,
		Realm:      echo.Realm,
		RequestB64: echo.Request,
		Digest:     echo.Digest,
		Expires:    echo.Expires,
		Opaque:     echo.Opaque,
		Header:     echo.Header,
	}, cred
}

// paymentCredentialValue returns the first header element, in any field
// regardless of its name's case, that uses the Payment auth scheme. A
// challenge may name a custom credential field, so every field is checked.
func paymentCredentialValue(header http.Header) (string, bool) {
	for _, values := range header {
		for _, value := range values {
			for _, part := range strings.Split(value, ",") {
				part = strings.TrimSpace(part)
				scheme, token, _ := strings.Cut(part, " ")
				token = strings.TrimSpace(token)
				// A credential is the scheme and one token; this keeps
				// prose such as "payment received" in other fields out.
				if strings.EqualFold(scheme, mpp.SchemePayment) && token != "" && !strings.ContainsAny(token, " \t") {
					return part, true
				}
			}
		}
	}
	return "", false
}

// closeRequestBody closes req.Body, as http.RoundTripper requires even when
// RoundTrip returns an error.
func closeRequestBody(req *http.Request) {
	if req.Body != nil {
		_ = req.Body.Close()
	}
}

// createCredential runs the OnChallenge hook, when set, and otherwise creates
// the credential with the selected method.
func (t *Transport) createCredential(ctx context.Context, selected challengeCandidate) (*mpp.Credential, error) {
	// create runs the method at most once: a method may broadcast a payment on
	// every call, so a hook that calls createCredential and then returns
	// (nil, nil), or calls it twice, must not pay twice.
	var (
		created    bool
		createdErr error
		createdVal *mpp.Credential
	)
	create := func(ctx context.Context) (*mpp.Credential, error) {
		if created {
			return createdVal, createdErr
		}
		created = true
		createdVal, createdErr = selected.method.CreateCredential(ctx, selected.challenge)
		if createdErr != nil {
			createdVal = nil
			createdErr = fmt.Errorf("mpp: creating credential for method %q: %w", selected.challenge.Method, createdErr)
		}
		return createdVal, createdErr
	}
	if t.onChallenge == nil {
		return create(ctx)
	}
	cred, err := t.onChallenge(ctx, selected.challenge, create)
	if err != nil {
		// Pass a payment-method failure the hook forwarded through unchanged;
		// only errors the hook made itself are reported as a decline.
		if created && createdErr != nil && errors.Is(err, createdErr) {
			return nil, err
		}
		// A hook that failed after createCredential made a credential did not
		// decline: the method may already have paid.
		if created && createdErr == nil {
			return nil, fmt.Errorf("mpp: onChallenge after creating credential: %w", err)
		}
		return nil, &declinedError{err: err}
	}
	if cred == nil {
		return create(ctx)
	}
	return cred, nil
}

// declinedError is an error an OnChallengeFunc returned to decline a
// payment. It matches both ErrPaymentDeclined and the hook's error.
type declinedError struct {
	err error
}

func (e *declinedError) Error() string { return "mpp: onChallenge: " + e.err.Error() }

func (e *declinedError) Unwrap() []error { return []error{ErrPaymentDeclined, e.err} }

// orderCandidates runs the OrderChallenges hook on the ranked candidates and
// returns its order. Entries without a Challenge or a Method for its method
// and intent are dropped.
func (t *Transport) orderCandidates(ctx context.Context, ranked []challengeCandidate) ([]challengeCandidate, error) {
	exported := make([]ChallengeCandidate, len(ranked))
	for i, candidate := range ranked {
		exported[i] = ChallengeCandidate{Challenge: candidate.challenge, Method: candidate.method, Quality: candidate.quality}
	}
	ordered, err := t.orderChallenges(ctx, exported)
	if err != nil {
		return nil, fmt.Errorf("mpp: orderChallenges: %w", err)
	}
	result := make([]challengeCandidate, 0, len(ordered))
	for _, candidate := range ordered {
		if candidate.Challenge == nil || candidate.Method == nil ||
			candidate.Method.Name() != candidate.Challenge.Method || candidate.Method.Intent() != candidate.Challenge.Intent {
			continue
		}
		result = append(result, challengeCandidate{challenge: candidate.Challenge, method: candidate.Method, quality: candidate.Quality})
	}
	return result, nil
}

// responseProblemType returns the problem type of a 402 response body, or ""
// when it is not problem details. It restores the body it reads, so the
// response can still be returned to the caller. An invalid-payload problem
// drops the cached credential before a retry; other 402 responses retain it to
// avoid paying twice.
func responseProblemType(resp *http.Response) mpp.ErrorType {
	mediaType, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/problem+json" || resp.Body == nil {
		return ""
	}
	prefix, readErr := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	resp.Body = &peekedBody{Reader: io.MultiReader(bytes.NewReader(prefix), resp.Body), Closer: resp.Body}
	if readErr != nil {
		return ""
	}
	var problem struct {
		Type mpp.ErrorType `json:"type"`
	}
	if err := json.NewDecoder(bytes.NewReader(prefix)).Decode(&problem); err != nil {
		return ""
	}
	return problem.Type
}

// peekedBody replays a response body prefix that was read for inspection.
type peekedBody struct {
	io.Reader
	io.Closer
}

func (t *Transport) challengeCandidates(challenges []mpp.Challenge, preferences []paymentPreference, now time.Time) []challengeCandidate {
	candidates := make([]challengeCandidate, 0, len(challenges))
	for i := range challenges {
		ch := &challenges[i]
		if ch.Expires != "" {
			expiry, err := mpp.ParseExpires(ch.Expires)
			if err != nil {
				// Unparseable expiry: the issuing server rejects such a
				// credential (server.VerifyOrChallenge returns "invalid expires
				// format"), so don't waste a payment on a challenge it would
				// refuse. Skip it.
				continue
			}
			if expiry.Before(now) {
				continue
			}
		}
		methods := t.methods[methodKey{name: ch.Method, intent: ch.Intent}]
		if len(methods) == 0 {
			continue
		}
		preference, ok := bestPaymentPreference(ch, preferences)
		if !ok || preference.quality == 0 {
			continue
		}
		for _, method := range methods {
			if matcher, ok := method.(ChallengeMatcher); ok && !matcher.CanHandleChallenge(ch) {
				continue
			}
			candidates = append(candidates, challengeCandidate{
				challenge: ch,
				method:    method,
				quality:   preference.quality,
			})
		}
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		return candidates[i].quality > candidates[j].quality
	})
	return candidates
}

type challengeCandidate struct {
	challenge *mpp.Challenge
	method    Method
	quality   float64
}

func (t *Transport) prepareRequest(req *http.Request) (*http.Request, []paymentPreference) {
	if header, ok := headerValue(req.Header, mpp.HeaderAcceptPayment); ok {
		if preferences, err := parseAcceptPayment(header); err == nil {
			return req, preferences
		}
		return req, t.paymentPreferences
	}
	if t.acceptPayment == "" {
		return req, t.paymentPreferences
	}
	clone := req.Clone(req.Context())
	clone.Header = req.Header.Clone()
	if clone.Header == nil {
		clone.Header = make(http.Header)
	}
	clone.Header.Set(mpp.HeaderAcceptPayment, t.acceptPayment)
	return clone, t.paymentPreferences
}

func headerValue(header http.Header, name string) (string, bool) {
	for key, values := range header {
		if strings.EqualFold(key, name) {
			return strings.Join(values, ", "), true
		}
	}
	return "", false
}

func drainAndClose(body io.ReadCloser) {
	_, _ = io.Copy(io.Discard, body)
	_ = body.Close()
}

// cloneRequest creates a copy of the request suitable for retry.
// It uses req.GetBody if available to replay the request body.
func (t *Transport) cloneRequest(req *http.Request) (*http.Request, error) {
	clone := req.Clone(req.Context())
	if req.Body == nil || req.Body == http.NoBody {
		return clone, nil
	}
	if req.GetBody != nil {
		body, err := req.GetBody()
		if err != nil {
			return nil, fmt.Errorf("getting request body for retry: %w", err)
		}
		clone.Body = body
		return clone, nil
	}
	return nil, fmt.Errorf("request body was consumed and GetBody is not set; cannot retry")
}

// parseChallenges extracts Payment challenges from WWW-Authenticate headers.
// Returns successfully parsed challenges and any parse errors.
func (t *Transport) parseChallenges(header http.Header) ([]mpp.Challenge, []error) {
	var challenges []mpp.Challenge
	var errs []error
	for _, h := range header.Values(mpp.HeaderWWWAuthenticate) {
		for _, part := range mpp.SplitAuthenticate(h) {
			part = strings.TrimSpace(part)
			scheme, _, ok := strings.Cut(part, " ")
			if !ok || !strings.EqualFold(scheme, mpp.SchemePayment) {
				continue
			}
			ch, err := mpp.ParseChallenge(part)
			if err != nil {
				errs = append(errs, err)
				continue
			}
			challenges = append(challenges, *ch)
		}
	}
	return challenges, errs
}

func withPaymentOrigin(ctx context.Context, origin string) context.Context {
	return withPaymentState(ctx, &paymentState{origin: origin})
}

func withPaymentState(ctx context.Context, state *paymentState) context.Context {
	return context.WithValue(ctx, paymentStateContextKey{}, state)
}

func paymentStateFrom(ctx context.Context) *paymentState {
	state, _ := ctx.Value(paymentStateContextKey{}).(*paymentState)
	return state
}

func paymentOrigin(ctx context.Context) string {
	if state := paymentStateFrom(ctx); state != nil {
		return state.origin
	}
	return ""
}

// requestOrigin returns the origin of requestURL with its scheme and host
// lowercased and a default port (443 for https, 80 for http) dropped, so
// https://h and https://h:443 compare equal.
func requestOrigin(requestURL *url.URL) string {
	return tempoproof.Origin(requestURL)
}

func sameOriginURL(requestURL *url.URL, origin string) bool {
	return requestOrigin(requestURL) == origin
}

func (t *Transport) validatePaymentOrigin(req *http.Request, candidate challengeCandidate) error {
	challenge := candidate.challenge
	origin := paymentOrigin(req.Context())
	if origin == "" && t.payRedirects {
		// NewHTTPClient's CheckRedirect keeps redirects on the origin of the
		// request the caller sent, the first in the redirect chain.
		first := req
		for first.Response != nil {
			if first.Response.Request == nil {
				// A broken chain hides the request the caller sent: fail
				// closed rather than trust this redirected request's origin.
				return fmt.Errorf("mpp: refusing payment after redirect to %q; the redirect chain does not lead back to the original request", requestOrigin(req.URL))
			}
			first = first.Response.Request
		}
		origin = requestOrigin(first.URL)
	}
	if origin == "" {
		// Standalone Transport: no trusted origin was pinned into the context
		// (that only happens when the Transport is driven through Client.Do).
		// We cannot compare against the caller's intended origin, so fall back
		// to the request URL — but first refuse if this request was produced by
		// a redirect. net/http sets Request.Response only on redirect
		// follow-ups, so its presence means the caller never chose this origin
		// and we must not auto-pay it.
		if req.Response != nil {
			return fmt.Errorf("mpp: refusing payment after redirect to %q; drive the Transport through client.Client for redirect-safe payments", requestOrigin(req.URL))
		}
		origin = requestOrigin(req.URL)
	}
	if !sameOriginURL(req.URL, origin) {
		return fmt.Errorf("mpp: refusing payment for redirected origin %q", requestOrigin(req.URL))
	}
	// The method decides whether the server at this origin may receive its
	// credential, such as a Tempo proof that authenticates the payer at the
	// challenge realm.
	if validator, ok := candidate.method.(OriginValidator); ok {
		return validator.ValidateOrigin(req.URL, challenge)
	}
	// Methods that do not validate origins keep the built-in rule for
	// zero-amount tempo/charge proofs: the server must own the realm.
	if tempoproof.IsProofChallenge(challenge) && !tempoproof.OriginOwnsRealm(req.URL, challenge.Realm) {
		return tempoproof.RealmError(challenge.Realm, requestOrigin(req.URL), "")
	}
	return nil
}
