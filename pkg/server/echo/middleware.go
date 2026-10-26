package echoadapter

import (
	"slices"

	echofw "github.com/labstack/echo/v4"

	"github.com/tempoxyz/mpp-go/pkg/mpp"
	"github.com/tempoxyz/mpp-go/pkg/server"
)

const (
	credentialKey = "mpp.credential"
	receiptKey    = "mpp.receipt"
)

// Credential returns the verified credential stored on the Echo context.
func Credential(c echofw.Context) *mpp.Credential {
	v := c.Get(credentialKey)
	credential, _ := v.(*mpp.Credential)
	return credential
}

// Receipt returns the verified receipt stored on the Echo context.
func Receipt(c echofw.Context) *mpp.Receipt {
	v := c.Get(receiptKey)
	receipt, _ := v.(*mpp.Receipt)
	return receipt
}

// ChargeMiddleware protects an Echo route with the server charge flow. It
// runs server.Mpp.ChargeRequest with the route's scope (its Path) unless
// params.MppxScope is set, writes the payment response when payment is
// required or rejected, and on success stores the verified Credential and
// Receipt on the Echo context and the request context.
func ChargeMiddleware(m *server.Mpp, params server.ChargeParams) echofw.MiddlewareFunc {
	return func(next echofw.HandlerFunc) echofw.HandlerFunc {
		return func(c echofw.Context) error {
			params := params
			if len(params.MppxScope) == 0 {
				params.MppxScope = server.ScopeFromHTTPRequest(c.Request(), c.Path())
			}
			result, err := m.ChargeRequest(c.Request(), params)
			return serve(c, next, result, err)
		}
	}
}

// ComposeMiddleware protects an Echo route with several payment offers, like
// server.ComposeMiddleware: an unpaid request gets every offer, and a
// Credential is verified against the offer it echoes. Offers without their
// own Params.MppxScope bind the route's scope. ComposeMiddleware panics when
// server.ComposeMiddleware would: offers is empty, an Mpp is nil, realms
// differ, or an offer cannot be built.
func ComposeMiddleware(offers ...server.Offer) echofw.MiddlewareFunc {
	_ = server.ComposeMiddleware(offers...) // validate offers up front
	offers = slices.Clone(offers)
	return func(next echofw.HandlerFunc) echofw.HandlerFunc {
		return func(c echofw.Context) error {
			scope := server.ScopeFromHTTPRequest(c.Request(), c.Path())
			result, err := server.ComposeRequest(c.Request(), scopedOffers(offers, scope)...)
			return serve(c, next, result, err)
		}
	}
}

// scopedOffers returns offers with scope bound into every offer that sets
// none of its own.
func scopedOffers(offers []server.Offer, scope map[string]string) []server.Offer {
	if len(scope) == 0 {
		return offers
	}
	scoped := slices.Clone(offers)
	for i := range scoped {
		if len(scoped[i].Params.MppxScope) == 0 {
			scoped[i].Params.MppxScope = scope
		}
	}
	return scoped
}

// serve writes the payment response for an unpaid or rejected request, or
// calls next for a verified payment, adding its receipt to a 2xx response.
func serve(c echofw.Context, next echofw.HandlerFunc, result *server.ChargeResult, err error) error {
	if err != nil || result.IsChallenge() {
		result.ServeHTTP(c.Response(), c.Request())
		return nil
	}

	// The payment has settled: DeferPaymentReceipt leaves out a receipt that
	// cannot be formatted rather than failing the request.
	ctx := server.ContextWithPayment(c.Request().Context(), result.Credential, result.Receipt)
	c.SetRequest(c.Request().WithContext(ctx))
	c.Set(credentialKey, result.Credential)
	c.Set(receiptKey, result.Receipt)
	writer, complete := server.DeferPaymentReceipt(c.Response().Writer, result.Receipt)
	c.Response().Writer = writer
	if err := next(c); err != nil {
		return err
	}
	complete()
	return nil
}
