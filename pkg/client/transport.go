package client

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/tempoxyz/mpp-go/pkg/mpp"
)

// Transport is an http.RoundTripper that handles 402 Payment Required responses.
// It wraps an inner transport and automatically negotiates payment.
type Transport struct {
	methods map[string]Method
	inner   http.RoundTripper
}

type paymentOriginContextKey struct{}

// NewTransport creates a payment-aware transport.
func NewTransport(methods []Method, inner http.RoundTripper) *Transport {
	if inner == nil {
		inner = http.DefaultTransport
	}
	m := make(map[string]Method, len(methods))
	for _, method := range methods {
		m[method.Name()] = method
	}
	return &Transport{
		methods: m,
		inner:   inner,
	}
}

// RoundTrip implements http.RoundTripper with automatic 402 handling.
func (t *Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.inner.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusPaymentRequired {
		return resp, nil
	}

	// Parse all WWW-Authenticate headers looking for Payment challenges (RFC 9110).
	challenges, errs := t.parseChallenges(resp.Header)
	_ = errs // Non-Payment or malformed headers are silently skipped.

	// Find first challenge with a matching method that hasn't expired.
	var matched *mpp.Challenge
	var method Method
	now := time.Now().UTC()
	for i := range challenges {
		ch := &challenges[i]
		if ch.Expires != "" {
			expiry, err := time.Parse(time.RFC3339, ch.Expires)
			if err == nil && expiry.Before(now) {
				continue
			}
			// Also try the millisecond format used by mpp.Expires helpers.
			if err != nil {
				expiry, err = time.Parse("2006-01-02T15:04:05.000Z", ch.Expires)
				if err == nil && expiry.Before(now) {
					continue
				}
			}
		}
		if m, ok := t.methods[ch.Method]; ok {
			matched = ch
			method = m
			break
		}
	}

	if matched == nil {
		// No matching method found — return original 402 response as-is.
		return resp, nil
	}
	if err := validatePaymentOrigin(req, matched); err != nil {
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		return nil, err
	}

	// Drain and close the 402 response body so the connection can be reused.
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	// Create payment credential.
	cred, err := method.CreateCredential(req.Context(), matched)
	if err != nil {
		return nil, fmt.Errorf("mpp: creating credential for method %q: %w", matched.Method, err)
	}

	// Clone the original request for retry.
	retry, err := t.cloneRequest(req)
	if err != nil {
		return nil, fmt.Errorf("mpp: cloning request for retry: %w", err)
	}
	retry.Header.Set("Authorization", cred.ToAuthorization())

	return t.inner.RoundTrip(retry)
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
	for _, h := range header.Values("WWW-Authenticate") {
		for _, part := range mpp.SplitAuthenticate(h) {
			part = strings.TrimSpace(part)
			scheme, _, ok := strings.Cut(part, " ")
			if !ok || !strings.EqualFold(scheme, "Payment") {
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

func validatePaymentOrigin(req *http.Request, challenge *mpp.Challenge) error {
	origin := paymentOrigin(req.Context())
	if origin == "" {
		origin = requestOrigin(req.URL)
	}
	if !sameOriginURL(req.URL, origin) {
		return fmt.Errorf("mpp: refusing payment for redirected origin %q", requestOrigin(req.URL))
	}
	if challenge.Realm != "" && !challengeRealmMatchesRequest(challenge.Realm, req.URL) {
		return fmt.Errorf("mpp: challenge realm %q does not match request host %q", challenge.Realm, req.URL.Host)
	}
	return nil
}

func challengeRealmMatchesRequest(realm string, requestURL *url.URL) bool {
	parsedRealm, err := url.Parse(realm)
	if err == nil && parsedRealm.Host != "" {
		realm = parsedRealm.Host
	}
	if strings.EqualFold(realm, requestURL.Host) {
		return true
	}
	return strings.EqualFold(realm, requestURL.Hostname())
}
