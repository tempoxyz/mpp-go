// Package paymentctx carries what a client transport knows about the
// Challenge it asks a payment method to pay, and the error by which a method
// asks the transport to create the Credential again for the same Challenge.
package paymentctx

import (
	"context"
	"errors"
)

// ErrSettlementPending is matched, with errors.Is, by an error a method
// returns while a payment it already made for the Challenge is still
// settling, such as the Tempo client's ZoneSettlementPendingError. The HTTP
// Transport and the MCP client then create the Credential again for the same
// Challenge, which resumes waiting for that payment, rather than requesting a
// new Challenge, which would not settle it sooner.
var ErrSettlementPending = errors.New("mpp: payment settlement pending")

type originKey struct{}

// WithOrigin returns ctx carrying origin, the lowercase scheme://host[:port]
// of the server that issued the Challenge being paid. An empty origin leaves
// ctx unchanged.
func WithOrigin(ctx context.Context, origin string) context.Context {
	if origin == "" {
		return ctx
	}
	return context.WithValue(ctx, originKey{}, origin)
}

// Origin returns the origin WithOrigin set on ctx, or "" when the issuing
// server is unknown, as over MCP or for a method called directly.
func Origin(ctx context.Context) string {
	origin, _ := ctx.Value(originKey{}).(string)
	return origin
}

// RetryPending calls create, and calls it again while it fails with
// ErrSettlementPending, up to attempts calls in all (at least one). It returns
// any other error at once. Once ctx is done it calls create no more, not even
// the first time, and returns the context's cause, so a call canceled while a
// payment awaited approval creates no Credential. When an earlier attempt
// reported a pending settlement, that error is joined to the cause, so callers
// can match either with errors.Is and keep the pending payment's details.
func RetryPending[T any](ctx context.Context, attempts int, create func() (T, error)) (T, error) {
	var pending error
	for attempt := 1; ; attempt++ {
		if ctx.Err() != nil {
			var zero T
			return zero, errors.Join(context.Cause(ctx), pending)
		}
		value, err := create()
		if err == nil || !errors.Is(err, ErrSettlementPending) || attempt >= attempts {
			return value, err
		}
		pending = err
	}
}
