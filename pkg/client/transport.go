package client

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"mime"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

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
	trustedProofRealms map[string]string
}

const defaultMaxPaymentRetries = 3

type credentialKey struct {
	id     string
	method methodKey
}

type paymentOriginContextKey struct{}

// TransportOption configures a Transport.
type TransportOption func(*transportConfig)

type transportConfig struct {
	paymentPreferences PaymentPreferences
	trustedProofRealms map[string]string
}

// WithTransportTrustedProofRealm pins the Tempo proof realm for a known HTTP origin.
// The origin must include scheme and host (and port, when non-default).
func WithTransportTrustedProofRealm(origin, realm string) TransportOption {
	return func(config *transportConfig) {
		if config.trustedProofRealms == nil {
			config.trustedProofRealms = make(map[string]string)
		}
		config.trustedProofRealms[origin] = realm
	}
}

// WithTransportPaymentPreferences sets payment preferences on a standalone
// Transport. Client users should use WithPaymentPreferences instead. Unknown
// keys and invalid q-values cause RoundTrip to fail before sending the request.
func WithTransportPaymentPreferences(preferences PaymentPreferences) TransportOption {
	return func(config *transportConfig) {
		config.paymentPreferences = clonePaymentPreferences(preferences)
	}
}

// NewTransport creates a payment-aware transport.
func NewTransport(methods []Method, inner http.RoundTripper, opts ...TransportOption) *Transport {
	if inner == nil {
		inner = http.DefaultTransport
	}
	config := new(transportConfig)
	for _, opt := range opts {
		opt(config)
	}
	m := make(map[methodKey][]Method, len(methods))
	for _, method := range methods {
		key := keyForMethod(method)
		m[key] = append(m[key], method)
	}
	preferences, header, err := resolvePaymentPreferences(methods, config.paymentPreferences)
	return &Transport{
		methods:            m,
		inner:              inner,
		paymentPreferences: preferences,
		acceptPayment:      header,
		configErr:          err,
		trustedProofRealms: config.trustedProofRealms,
	}
}

// RoundTrip implements http.RoundTripper with automatic 402 handling.
func (t *Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	if t.configErr != nil {
		return nil, t.configErr
	}

	baseRequest, preferences := t.prepareRequest(req)
	credentials := make(map[credentialKey]*mpp.Credential)
	request := baseRequest
	var sentCredential credentialKey

	for retries := 0; ; retries++ {
		resp, err := t.inner.RoundTrip(request)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode != http.StatusPaymentRequired || retries >= defaultMaxPaymentRetries {
			return resp, nil
		}

		challenges, errs := t.parseChallenges(resp.Header)
		_ = errs // Non-Payment or malformed headers are silently skipped.
		candidates := t.challengeCandidates(challenges, preferences, time.Now().UTC())
		if len(candidates) == 0 {
			return resp, nil
		}
		var selected challengeCandidate
		var rejected error
		for _, candidate := range candidates {
			if err := t.validatePaymentOrigin(request, candidate.challenge); err != nil {
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

		if isInvalidPayloadResponse(resp) {
			delete(credentials, sentCredential)
		}
		drainAndClose(resp.Body)

		// Prepare the retry before paying for it. A credential created for a
		// request that cannot be sent again is spent without ever reaching
		// the server.
		retry, err := t.cloneRequest(baseRequest)
		if err != nil {
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
			cred, err = selected.method.CreateCredential(baseRequest.Context(), selected.challenge)
			if err != nil {
				if retry.Body != nil {
					_ = retry.Body.Close()
				}
				return nil, fmt.Errorf("mpp: creating credential for method %q: %w", selected.challenge.Method, err)
			}
			credentials[key] = cred
		}

		authorization, err := mpp.FormatAuthorizationStrict(cred)
		if err != nil {
			if retry.Body != nil {
				_ = retry.Body.Close()
			}
			return nil, err
		}
		retry.Header.Set(selected.challenge.CredentialHeader(), authorization)
		request = retry
		sentCredential = key
	}
}

// isInvalidPayloadResponse inspects only responses that will be consumed for a
// retry. Other 402 responses retain cached credentials to avoid paying twice.
func isInvalidPayloadResponse(resp *http.Response) bool {
	mediaType, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/problem+json" || resp.Body == nil {
		return false
	}
	var problem struct {
		Type mpp.ErrorType `json:"type"`
	}
	err = json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&problem)
	return err == nil && problem.Type == mpp.ErrorTypeInvalidPayload
}

func (t *Transport) challengeCandidates(challenges []mpp.Challenge, preferences []paymentPreference, now time.Time) []challengeCandidate {
	candidates := make([]challengeCandidate, 0, len(challenges))
	for i := range challenges {
		ch := &challenges[i]
		if ch.Expires != "" {
			expiry, err := parseChallengeExpiry(ch.Expires)
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

// parseChallengeExpiry parses a challenge expiry using RFC 3339 and the
// millisecond format emitted by the mpp.Expires helpers.
func parseChallengeExpiry(value string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, value); err == nil {
		return t, nil
	}
	return time.Parse("2006-01-02T15:04:05.000Z", value)
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
	return context.WithValue(ctx, paymentOriginContextKey{}, origin)
}

func paymentOrigin(ctx context.Context) string {
	origin, _ := ctx.Value(paymentOriginContextKey{}).(string)
	return origin
}

func requestOrigin(requestURL *url.URL) string {
	if requestURL == nil {
		return ""
	}
	return strings.ToLower(requestURL.Scheme) + "://" + strings.ToLower(requestURL.Host)
}

func sameOriginURL(requestURL *url.URL, origin string) bool {
	return requestOrigin(requestURL) == origin
}

func (t *Transport) validatePaymentOrigin(req *http.Request, challenge *mpp.Challenge) error {
	origin := paymentOrigin(req.Context())
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
	// A zero-amount Tempo proof can authenticate the payer at the realm named
	// in the challenge. The HTTP server that sent it must own that realm.
	if challenge.Method == "tempo" && challenge.Intent == "charge" &&
		isZeroTempoAmount(challenge.Request["amount"]) &&
		!strings.EqualFold(challenge.Realm, req.URL.Hostname()) &&
		challenge.Realm != t.trustedProofRealms[requestOrigin(req.URL)] {
		return fmt.Errorf("mpp: refusing to sign Tempo proof for realm %q from %q", challenge.Realm, requestOrigin(req.URL))
	}
	return nil
}

func isZeroTempoAmount(value any) bool {
	amount := strings.TrimSpace(fmt.Sprint(value))
	if amount == "" {
		return false
	}
	for _, digit := range amount {
		if digit < '0' || digit > '9' {
			return false
		}
	}
	parsed, ok := new(big.Int).SetString(amount, 10)
	return ok && parsed.Sign() == 0
}
