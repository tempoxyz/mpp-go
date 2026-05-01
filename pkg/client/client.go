// Package client provides an HTTP client with automatic 402 Payment Required handling.
package client

import (
	"context"
	"fmt"
	"io"
	"net/http"

	"github.com/tempoxyz/mpp-go/pkg/mpp"
)

// Method is the interface that payment methods must implement for client-side use.
type Method interface {
	// Name returns the method name this handler supports (e.g., "tempo").
	Name() string
	// CreateCredential creates a payment credential for the given challenge.
	CreateCredential(ctx context.Context, challenge *mpp.Challenge) (*mpp.Credential, error)
}

// Client is an HTTP client with automatic 402 payment handling.
type Client struct {
	methods    map[string]Method
	httpClient *http.Client
}

// Option configures the Client.
type Option func(*Client)

// WithHTTPClient sets a custom http.Client.
func WithHTTPClient(c *http.Client) Option {
	return func(cl *Client) {
		cl.httpClient = c
	}
}

// New creates a Client with the given payment methods.
func New(methods []Method, opts ...Option) *Client {
	c := &Client{
		methods:    make(map[string]Method, len(methods)),
		httpClient: http.DefaultClient,
	}
	for _, m := range methods {
		c.methods[m.Name()] = m
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
	req = req.WithContext(withPaymentOrigin(req.Context(), origin))

	transport := &Transport{
		methods: c.methods,
		inner:   inner,
	}

	// Use a copy of the http.Client with our payment-aware transport.
	hc := *c.httpClient
	hc.Transport = transport
	priorCheckRedirect := hc.CheckRedirect
	hc.CheckRedirect = func(redirectReq *http.Request, via []*http.Request) error {
		if len(via) > 0 && !sameOriginURL(redirectReq.URL, origin) {
			return fmt.Errorf("mpp: refusing cross-origin redirect from %q to %q", origin, requestOrigin(redirectReq.URL))
		}
		if priorCheckRedirect != nil {
			return priorCheckRedirect(redirectReq, via)
		}
		return nil
	}
	return hc.Do(req)
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
