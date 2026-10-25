// Package client provides an HTTP client with automatic 402 Payment Required handling.
package client

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"

	"github.com/tempoxyz/mpp-go/pkg/mpp"
)

// Method is the interface that payment methods must implement for client-side use.
type Method interface {
	// Name returns the method name this handler supports (e.g., "tempo").
	Name() string
	// Intent returns the payment intent this handler supports (e.g., "charge").
	Intent() string
	// CreateCredential creates a payment credential for the given challenge.
	CreateCredential(ctx context.Context, challenge *mpp.Challenge) (*mpp.Credential, error)
}

// ChallengeMatcher may be implemented by a Method that only handles some
// challenges for its method and intent. It allows multiple handlers for the
// same method and intent to select between method-specific request details.
type ChallengeMatcher interface {
	CanHandleChallenge(challenge *mpp.Challenge) bool
}

// PaymentPreferences assigns HTTP q-values to configured payment methods.
// Keys use the "method/intent" form. Omitted methods default to q=1.
type PaymentPreferences map[string]float64

type methodKey struct {
	name   string
	intent string
}

func keyForMethod(method Method) methodKey {
	return methodKey{name: method.Name(), intent: method.Intent()}
}

// Client is an HTTP client with automatic 402 payment handling.
type Client struct {
	methods            []Method
	httpClient         *http.Client
	paymentPreferences PaymentPreferences
	trustedProofRealms map[string]string
	onChallenge        OnChallengeFunc
}

// Option configures the Client.
type Option func(*Client)

// WithHTTPClient sets a custom http.Client.
func WithHTTPClient(c *http.Client) Option {
	return func(cl *Client) {
		cl.httpClient = c
	}
}

// WithPaymentPreferences sets client payment preferences. The preferences are
// advertised in Accept-Payment and used to rank server challenges. Unknown
// keys and invalid q-values cause Do to fail before sending the request.
func WithPaymentPreferences(preferences PaymentPreferences) Option {
	return func(c *Client) {
		c.paymentPreferences = clonePaymentPreferences(preferences)
	}
}

// WithTrustedTempoProofRealm pins the proof realm of a server at a known origin.
// Use this when the server's configured realm differs from its public hostname.
func WithTrustedTempoProofRealm(origin, realm string) Option {
	return func(c *Client) {
		if c.trustedProofRealms == nil {
			c.trustedProofRealms = make(map[string]string)
		}
		c.trustedProofRealms[origin] = realm
	}
}

// WithOnChallenge sets a hook that runs when Do receives a 402 challenge it can
// pay, before a credential is created. Use it to approve or decline a payment
// or to supply the credential. See OnChallengeFunc.
func WithOnChallenge(onChallenge OnChallengeFunc) Option {
	return func(c *Client) {
		c.onChallenge = onChallenge
	}
}

// New creates a Client with the given payment methods.
func New(methods []Method, opts ...Option) *Client {
	c := &Client{
		methods:    append([]Method(nil), methods...),
		httpClient: http.DefaultClient,
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// Do sends an HTTP request, handling 402 responses automatically.
// If a 402 is received with a WWW-Authenticate: Payment header,
// it finds a matching method, creates credentials, and retries.
func (c *Client) Do(req *http.Request) (*http.Response, error) {
	inner := c.httpClient.Transport
	if inner == nil {
		inner = http.DefaultTransport
	}
	origin := requestOrigin(req.URL)
	state := &paymentState{origin: origin}
	req = req.WithContext(withPaymentState(req.Context(), state))

	options := []TransportOption{WithTransportPaymentPreferences(c.paymentPreferences)}
	if c.onChallenge != nil {
		options = append(options, WithTransportOnChallenge(c.onChallenge))
	}
	for trustedOrigin, realm := range c.trustedProofRealms {
		options = append(options, WithTransportTrustedProofRealm(trustedOrigin, realm))
	}
	transport := NewTransport(c.methods, inner, options...)

	// Use a copy of the http.Client with our payment-aware transport.
	hc := *c.httpClient
	hc.Transport = transport
	priorCheckRedirect := hc.CheckRedirect
	hc.CheckRedirect = func(redirectReq *http.Request, via []*http.Request) error {
		// Run the caller's hook first: it may change the redirect's headers.
		if priorCheckRedirect != nil {
			if err := priorCheckRedirect(redirectReq, via); err != nil {
				return err
			}
		} else if len(via) >= maxRedirects {
			// Keep net/http's default limit of 10 redirects.
			return fmt.Errorf("stopped after %d redirects", maxRedirects)
		}
		if sameOriginURL(redirectReq.URL, origin) {
			return nil
		}
		// The Transport attaches credentials to its own retry requests, so
		// net/http never copies them into a redirect. A Payment credential the
		// caller set on the request itself would be copied, though, in any
		// field a challenge names, and net/http does not strip those: refuse
		// to carry one to another origin. The Transport separately refuses to
		// pay a redirected origin, or this origin once the chain has left it.
		if hasPaymentCredential(redirectReq.Header) {
			return fmt.Errorf("mpp: refusing cross-origin redirect from %q to %q with a Payment credential", origin, requestOrigin(redirectReq.URL))
		}
		state.mu.Lock()
		state.leftOrigin = true
		state.mu.Unlock()
		return nil
	}
	resp, err := hc.Do(req)
	if err != nil {
		err = restorePaymentOutcomeUnknown(err, state)
	}
	return resp, err
}

// restorePaymentOutcomeUnknown puts back the PaymentOutcomeUnknownError the
// Transport returned when net/http replaced it, as it does with its own
// timeout error when the http.Client has a Timeout.
func restorePaymentOutcomeUnknown(err error, state *paymentState) error {
	state.mu.Lock()
	recorded := state.unknown
	state.mu.Unlock()
	var unknown *PaymentOutcomeUnknownError
	if recorded == nil || errors.As(err, &unknown) {
		return err
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		restored := *urlErr
		restored.Err = &PaymentOutcomeUnknownError{
			Challenge:  recorded.Challenge,
			Credential: recorded.Credential,
			Err:        urlErr.Err,
		}
		return &restored
	}
	return &PaymentOutcomeUnknownError{
		Challenge:  recorded.Challenge,
		Credential: recorded.Credential,
		Err:        err,
	}
}

// maxRedirects matches net/http's default redirect limit.
const maxRedirects = 10

// hasPaymentCredential reports whether any header field, whatever its name
// or the case of its name, carries a value in the Payment auth scheme.
func hasPaymentCredential(header http.Header) bool {
	_, ok := paymentCredentialValue(header)
	return ok
}

// Get is a convenience method for GET requests.
func (c *Client) Get(ctx context.Context, url string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	return c.Do(req)
}

// Post is a convenience method for POST requests.
func (c *Client) Post(ctx context.Context, url string, contentType string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", contentType)
	return c.Do(req)
}
