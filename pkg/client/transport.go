package client

import (
	"bytes"
	"context"
	"encoding/base64"
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

	"github.com/tempoxyz/mpp-go/pkg/internal/paymentctx"
	"github.com/tempoxyz/mpp-go/pkg/internal/tempoproof"
	"github.com/tempoxyz/mpp-go/pkg/mpp"
)

// Transport is an http.RoundTripper that handles 402 Payment Required responses.
// It wraps an inner transport and automatically negotiates payment.
type Transport struct {
	methods            map[methodKey][]Method
	methodList         []Method
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

// credentialKey identifies a Challenge by its ID and the fields a Credential
// echoes, which the ID binds for a native MPP server, leaving out display
// fields such as the description.
type credentialKey struct {
	id     string
	method methodKey
	echo   string
}

func newCredentialKey(echo mpp.ChallengeEcho) credentialKey {
	echo.Header = mpp.AdvertisedCredentialHeader(echo.Header)
	encoded, _ := json.Marshal(echo)
	return credentialKey{
		id:     echo.ID,
		method: methodKey{name: echo.Method, intent: echo.Intent},
		echo:   string(encoded),
	}
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
// It is not called when a 402 offers again the challenge the last request
// answered: the transport resends that request's credential instead, unless
// the 402 carries an invalid-payload problem.
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
		methodList:         append([]Method(nil), methods...),
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
	request := baseRequest
	// sentCredential identifies the Challenge sentCred answers: the
	// Credential the last request carried, which is the only one a later
	// round may send again, as in mppx's client Fetch.
	var sentCredential credentialKey
	// A credential the caller set on the request is sent with the first
	// round, so its outcome is as unknown as one the Transport created.
	// A Payment credential the caller set in Authorization or
	// Payment-Authorization that the client cannot parse (callerUnreadable)
	// may name a payment already made, so the client does not pay again for
	// it.
	sent, callerUnreadable, sentChallenge, sentCred := callerCredential(baseRequest.Header)
	callerPaid, callerCred := sent, sentCred
	if sentChallenge != nil && sentCred != nil {
		sentCredential = newCredentialKey(sentCred.Challenge)
	}

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
		if resp.StatusCode != http.StatusPaymentRequired {
			return resp, nil
		}

		problem := responseProblem(resp)
		if problem != nil && problem.Type == mpp.ErrorTypePaymentOutcomeUnknown {
			// Preserve the sent credential for explicit reconciliation without
			// authorizing a different payment from any accompanying offers.
			if sent {
				drainAndClose(resp.Body)
				unknown := &PaymentOutcomeUnknownError{
					Challenge: sentChallenge, Credential: sentCred,
					Err: problem,
				}
				if state := paymentStateFrom(req.Context()); state != nil {
					state.mu.Lock()
					state.unknown = unknown
					state.mu.Unlock()
				}
				return nil, unknown
			}
			return resp, nil
		}

		// An expired caller credential may already have settled. Let the caller
		// reconcile it instead of automatically authorizing a replacement.
		if callerPaid && problem != nil && problem.Type == mpp.ErrorTypePaymentExpired {
			return resp, nil
		}

		if retries >= t.maxRetries {
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
			// A 402 answering a paid retry without a payable challenge (such
			// as a rejected credential) is the server's final answer. A 402
			// to the unpaid request is an error, as in mppx.
			if sent {
				return resp, nil
			}
			drainAndClose(resp.Body)
			return nil, newNoMethodFoundError(challenges, t.configuredMethods())
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

		key := newCredentialKey(selected.challenge.ToEcho())
		// The Challenge the last request answered is offered again: send the
		// same Credential, without paying again, as mppx does. Any other
		// Challenge gets a new Credential, and so does the same Challenge
		// after an invalid-payload problem: the server rejected the
		// Credential itself, so sending it again cannot succeed.
		invalidPayload := problem != nil && problem.Type == mpp.ErrorTypeInvalidPayload
		var cred *mpp.Credential
		if sent && sentCred != nil && key == sentCredential && !invalidPayload {
			cred = sentCred
		}
		if cred == nil && sent && ((sentCred != nil && isPushCredential(sentCred)) || callerUnreadable) {
			// A push credential's payment (a hash credential's transfer or a
			// zone credential's withdrawal) is already on chain: resending it
			// is safe, but paying a new challenge would pay twice. A caller's
			// credential the client cannot parse may be one too, so the
			// caller reconciles it rather than the client paying again.
			return resp, nil
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

		if cred == nil {
			cred, err = t.createCredential(baseRequest.Context(), selected, challengeOrigin(request.URL))
			if err != nil {
				if retry.Body != nil {
					_ = retry.Body.Close()
				}
				return nil, err
			}
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
		// Only the caller's own credential is left for the caller to reconcile.
		callerPaid = callerCred != nil && cred == callerCred
	}
}

// PaymentOutcomeUnknownError is returned by Transport.RoundTrip, and so by
// Client.Do wrapped in a *url.Error, when the request carrying a payment
// credential failed without a response or the server reported an unknown
// settlement outcome. That includes a credential the caller
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
// caller set, and parses it when it can. Authorization and
// Payment-Authorization, the fields servers read credentials from, come first,
// and every value of both is checked: of those that parse, a push credential
// is preferred, since its payment is already on chain. unreadable reports a
// Payment credential in either field that does not parse: it may name a
// payment already made. Only when neither field holds a Payment credential is
// another field used (a challenge may name a custom credential field), and a
// Payment-shaped value there that does not parse is not unreadable, so
// unrelated metadata such as "X-Status: Payment pending" cannot block payment.
func callerCredential(header http.Header) (sent, unreadable bool, challenge *mpp.Challenge, cred *mpp.Credential) {
	supported, other := paymentCredentialValues(header)
	if len(supported) > 0 {
		for _, value := range supported {
			parsed, err := parseCallerCredential(value)
			if err != nil {
				unreadable = true
				continue
			}
			if cred == nil || (!isPushCredential(cred) && isPushCredential(parsed)) {
				cred = parsed
			}
		}
		return true, unreadable, challengeFromEcho(cred), cred
	}
	if len(other) == 0 {
		return false, false, nil, nil
	}
	cred, err := parseCallerCredential(other[0])
	if err != nil {
		return true, false, nil, nil
	}
	return true, false, challengeFromEcho(cred), cred
}

// parseCallerCredential parses a Payment credential the caller set.
func parseCallerCredential(value string) (*mpp.Credential, error) {
	cred, err := mpp.ParseCredential(value)
	if errors.Is(err, mpp.ErrUnsupportedCredentialHeader) {
		// The caller's own credential echoes a header a received challenge
		// may not select. It still names the payment the caller made (a push
		// credential's is already on chain), so read it without that header.
		cred, err = parseCredentialWithoutHeader(value)
	}
	return cred, err
}

// challengeFromEcho returns the Challenge cred echoes, or nil for a nil cred.
func challengeFromEcho(cred *mpp.Credential) *mpp.Challenge {
	if cred == nil {
		return nil
	}
	echo := cred.Challenge
	return &mpp.Challenge{
		ID:         echo.ID,
		Method:     echo.Method,
		Intent:     echo.Intent,
		Realm:      echo.Realm,
		RequestB64: echo.Request,
		Digest:     echo.Digest,
		Expires:    echo.Expires,
		Opaque:     echo.Opaque,
		Header:     echo.Header,
	}
}

// parseCredentialWithoutHeader parses a Payment credential after removing the
// header member of its challenge echo.
func parseCredentialWithoutHeader(value string) (*mpp.Credential, error) {
	_, token, _ := strings.Cut(value, " ")
	data, err := mpp.B64Decode(strings.TrimSpace(token))
	if err != nil {
		return nil, err
	}
	challenge, ok := data["challenge"].(map[string]any)
	if !ok {
		return nil, errors.New("mpp: credential challenge must be an object")
	}
	delete(challenge, "header")
	encoded, err := json.Marshal(data)
	if err != nil {
		return nil, err
	}
	return mpp.ParseCredential(mpp.SchemePayment + " " + base64.RawURLEncoding.EncodeToString(encoded))
}

// paymentCredentialValues returns every header element that uses the Payment
// auth scheme: supported holds those in Authorization and
// Payment-Authorization (field names in any case), other those in every other
// field, since a challenge may name a custom credential field. Values are
// split as servers split them (mpp.SplitAuthenticate), so a comma inside a
// quoted auth-param of another scheme does not start a Payment element.
func paymentCredentialValues(header http.Header) (supported, other []string) {
	for name, values := range header {
		isSupported := strings.EqualFold(name, mpp.HeaderAuthorization) || strings.EqualFold(name, mpp.HeaderPaymentAuthorization)
		for _, value := range values {
			for _, part := range mpp.SplitAuthenticate(value) {
				part = strings.TrimSpace(part)
				scheme, token, _ := strings.Cut(part, " ")
				token = strings.TrimSpace(token)
				// SplitAuthenticate keeps auth-params that follow a token
				// ("Payment <token>, realm=x") in the same part. Such a part
				// is still the caller's credential: it is kept whole, so
				// ParseCredential reports it unreadable and the 402 is
				// returned instead of paying again.
				token, _, _ = strings.Cut(token, ",")
				token = strings.TrimSpace(token)
				// A credential is the scheme and one token; this keeps
				// prose such as "payment received" in other fields out.
				if !strings.EqualFold(scheme, mpp.SchemePayment) || token == "" || strings.ContainsAny(token, " \t") {
					continue
				}
				if isSupported {
					supported = append(supported, part)
				} else {
					other = append(other, part)
				}
			}
		}
	}
	return supported, other
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
//
// origin is the origin of the server that issued the challenge, or "" when
// unknown. A method that fails with paymentctx.ErrSettlementPending is asked
// again for the same challenge, up to the payment-retry limit.
func (t *Transport) createCredential(ctx context.Context, selected challengeCandidate, origin string) (*mpp.Credential, error) {
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
		// The method learns which server issued the challenge, and a payment
		// still settling is resumed for the same challenge rather than paid
		// again under a new one.
		ctx = paymentctx.WithOrigin(ctx, origin)
		createdVal, createdErr = paymentctx.RetryPending(ctx, max(t.maxRetries, 1), func() (*mpp.Credential, error) {
			return selected.method.CreateCredential(ctx, selected.challenge)
		})
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

// responseProblem returns the decoded problem of a 402 response body, or nil
// when it is not problem details. It restores the body it reads, so the
// response can still be returned to the caller. An invalid-payload problem
// makes the Transport create a new credential for a re-offered challenge;
// other 402 responses resend the credential to avoid paying twice.
func responseProblem(resp *http.Response) *mpp.PaymentError {
	mediaType, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/problem+json" || resp.Body == nil {
		return nil
	}
	prefix, readErr := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	resp.Body = &peekedBody{Reader: io.MultiReader(bytes.NewReader(prefix), resp.Body), Closer: resp.Body}
	if readErr != nil {
		return nil
	}
	var problem mpp.PaymentError
	if err := json.NewDecoder(bytes.NewReader(prefix)).Decode(&problem); err != nil {
		// Optional peer fields must not hide the terminal payment outcome.
		var kind struct {
			Type mpp.ErrorType `json:"type"`
		}
		if err := json.Unmarshal(prefix, &kind); err != nil {
			return nil
		}
		return &mpp.PaymentError{Type: kind.Type}
	}
	return &problem
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
// Returns successfully parsed challenges and any parse errors. A challenge
// that fails to parse, including one that selects an unsupported credential
// header (mpp.ErrUnsupportedCredentialHeader), gets no credential.
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

// challengeOrigin returns the origin of the server that answered requestURL
// with a challenge, or "" when the URL names no host.
func challengeOrigin(requestURL *url.URL) string {
	if requestURL == nil || requestURL.Scheme == "" || requestURL.Host == "" {
		return ""
	}
	return requestOrigin(requestURL)
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

// configuredMethods returns the methods the Transport was built with, in
// configuration order.
func (t *Transport) configuredMethods() []Method {
	return t.methodList
}

// isPushCredential reports whether a credential names a payment the client
// already made on chain: a Tempo hash or zone credential.
func isPushCredential(credential *mpp.Credential) bool {
	switch credential.Payload["type"] {
	case "hash", "zone":
		return true
	}
	return false
}
