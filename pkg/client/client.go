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

// OriginValidator may be implemented by a Method whose Credentials must only
// be created for a server that is entitled to them. The Tempo charge client
// implements it: a zero-amount Tempo proof authenticates the payer at the
// realm the Challenge names, so it is signed only for a realm the sending
// server owns. The HTTP Transport calls ValidateOrigin with the request URL
// and skips a Challenge it rejects; the MCP client calls it with the endpoint
// URL of a Streamable HTTP session whose HTTP client refuses cross-origin
// redirects (mcpclient.Connect), and with a nil URL for any other session.
//
// A Method that does not implement it gets the Transport's built-in check for
// zero-amount tempo/charge Challenges.
type OriginValidator interface {
	ValidateOrigin(origin *url.URL, challenge *mpp.Challenge) error
}

// ErrPaymentDeclined is returned, wrapped, when a payment is declined before
// a Credential is created: by an OnChallengeFunc that returns an error of its
// own, or by the MCP client's OnPaymentRequired hook. Check it with
// errors.Is.
var ErrPaymentDeclined = errors.New("mpp: payment declined")

// OrderChallenges filters and sorts the payable Challenges before one is
// paid, like mppx's orderChallenges. Candidates arrive ranked by payment
// preference, then offer order. Return them in the order to try: the first
// one returned that passes the origin checks is paid. Returning none pays
// nothing, and the 402 response is returned as is; returning an error fails
// the request with it.
type OrderChallenges func(
	ctx context.Context,
	candidates []ChallengeCandidate,
) ([]ChallengeCandidate, error)

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
	onChallenge        OnChallengeFunc
	maxRetries         *int
	orderChallenges    OrderChallenges
}

// Option configures a Client, and the http.Client and Transport that
// NewHTTPClient and NewHTTPTransport build.
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

// WithOnChallenge sets a hook that runs when Do receives a 402 challenge it can
// pay, before a credential is created. Use it to approve or decline a payment
// or to supply the credential. See OnChallengeFunc.
func WithOnChallenge(onChallenge OnChallengeFunc) Option {
	return func(c *Client) {
		c.onChallenge = onChallenge
	}
}

// WithMaxRetries sets how many times one request pays a 402 Challenge and
// retries, like mppx's maxPaymentRetries. The default is 3; 0 never pays. A
// negative n is a configuration error.
func WithMaxRetries(n int) Option {
	return func(c *Client) {
		c.maxRetries = &n
	}
}

// WithOrderChallenges sets a hook that filters and sorts the payable
// Challenges of each 402 response before one is paid. See OrderChallenges.
func WithOrderChallenges(fn OrderChallenges) Option {
	return func(c *Client) {
		c.orderChallenges = fn
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

	transport := newTransport(c.methods, inner, c.transportConfig())

	// Use a copy of the http.Client with our payment-aware transport.
	hc := *c.httpClient
	hc.Transport = transport
	hc.CheckRedirect = sameOriginCheckRedirect(hc.CheckRedirect)
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

// sameOriginCheckRedirect returns a CheckRedirect hook that runs prior (or
// net/http's 10-redirect limit when prior is nil) and then refuses every
// redirect away from the origin of the request the caller sent.
func sameOriginCheckRedirect(prior func(*http.Request, []*http.Request) error) func(*http.Request, []*http.Request) error {
	return func(redirectReq *http.Request, via []*http.Request) error {
		// Run the caller's hook first: it may change the redirect's headers.
		if prior != nil {
			if err := prior(redirectReq, via); err != nil {
				return err
			}
		} else if len(via) >= maxRedirects {
			// Keep net/http's default limit of 10 redirects.
			return fmt.Errorf("stopped after %d redirects", maxRedirects)
		}
		if len(via) == 0 {
			return nil
		}
		// Refuse every cross-origin redirect, as mppx does: the other origin
		// would choose what this client fetches and may pay for.
		origin := requestOrigin(via[0].URL)
		if !sameOriginURL(redirectReq.URL, origin) {
			return fmt.Errorf("mpp: refusing cross-origin redirect from %q to %q", origin, requestOrigin(redirectReq.URL))
		}
		return nil
	}
}

// transportConfig returns the Transport settings of c's options.
func (c *Client) transportConfig() *transportConfig {
	return &transportConfig{
		paymentPreferences: c.paymentPreferences,
		onChallenge:        c.onChallenge,
		maxRetries:         c.maxRetries,
		orderChallenges:    c.orderChallenges,
	}
}

// NewHTTPClient returns a standard *http.Client that pays 402 Challenges with
// methods, as Client.Do does: its Transport is the payment Transport and its
// CheckRedirect refuses every cross-origin redirect (after the hook and
// Timeout of an http.Client passed with WithHTTPClient, or net/http's
// 10-redirect limit), so a payment is only made to the origin the caller
// asked for. Configuration errors, such as unknown payment preference keys,
// are returned here rather than on the first request.
//
// The http.Client passed with WithHTTPClient is copied, not modified; its
// Transport, or http.DefaultTransport, sends the requests. Unlike Client.Do,
// a request that times out after sending a credential reports net/http's
// timeout error rather than a PaymentOutcomeUnknownError.
func NewHTTPClient(methods []Method, opts ...Option) (*http.Client, error) {
	c := New(methods, opts...)
	base := c.httpClient
	if base == nil {
		base = http.DefaultClient
	}
	transport, err := c.newHTTPTransport(base.Transport)
	if err != nil {
		return nil, err
	}
	// Redirects stay on the original origin, so the Transport may pay them.
	transport.payRedirects = true
	hc := *base
	hc.Transport = transport
	hc.CheckRedirect = sameOriginCheckRedirect(base.CheckRedirect)
	return &hc, nil
}

// NewHTTPTransport returns a payment-aware http.RoundTripper that sends
// requests with inner (http.DefaultTransport when nil). It takes the same
// options as New, except WithHTTPClient, which it ignores, and returns
// configuration errors here rather than from RoundTrip.
//
// A bare Transport refuses to pay after a redirect, since it cannot tell
// whether the caller chose that origin: use NewHTTPClient or Client for
// redirect-safe payments.
func NewHTTPTransport(methods []Method, inner http.RoundTripper, opts ...Option) (*Transport, error) {
	return New(methods, opts...).newHTTPTransport(inner)
}

func (c *Client) newHTTPTransport(inner http.RoundTripper) (*Transport, error) {
	transport := newTransport(c.methods, inner, c.transportConfig())
	if transport.configErr != nil {
		return nil, transport.configErr
	}
	return transport, nil
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
