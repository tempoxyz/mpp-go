package client

import "net/http"

// newTestTransport builds a Transport from Client options the way
// NewHTTPTransport does, but keeps configuration errors for RoundTrip to
// return, so tests can drive a misconfigured Transport.
func newTestTransport(methods []Method, inner http.RoundTripper, opts ...Option) *Transport {
	return newTransport(methods, inner, New(methods, opts...).transportConfig())
}
