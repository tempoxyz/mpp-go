package ginadapter

import (
	"net/http"
	"slices"

	ginfw "github.com/gin-gonic/gin"

	"github.com/tempoxyz/mpp-go/pkg/mpp"
	"github.com/tempoxyz/mpp-go/pkg/server"
)

const (
	credentialKey = "mpp.credential"
	receiptKey    = "mpp.receipt"
)

// Credential returns the verified credential stored on the Gin context.
func Credential(c *ginfw.Context) *mpp.Credential {
	v, _ := c.Get(credentialKey)
	credential, _ := v.(*mpp.Credential)
	return credential
}

// Receipt returns the verified receipt stored on the Gin context.
func Receipt(c *ginfw.Context) *mpp.Receipt {
	v, _ := c.Get(receiptKey)
	receipt, _ := v.(*mpp.Receipt)
	return receipt
}

// ChargeMiddleware protects a Gin route with the server charge flow. It runs
// server.Mpp.ChargeRequest with the route's scope (its FullPath) unless
// params.MppxScope is set, writes the payment response when payment is
// required or rejected, and on success stores the verified Credential and
// Receipt on the Gin context and the request context.
func ChargeMiddleware(m *server.Mpp, params server.ChargeParams) ginfw.HandlerFunc {
	return func(c *ginfw.Context) {
		params := params
		if len(params.MppxScope) == 0 {
			params.MppxScope = server.ScopeFromHTTPRequest(c.Request, c.FullPath())
		}
		result, err := m.ChargeRequest(c.Request, params)
		serve(c, result, err)
	}
}

// ComposeMiddleware protects a Gin route with several payment offers, like
// server.ComposeMiddleware: an unpaid request gets every offer, and a
// Credential is verified against the offer it echoes. Offers without their
// own Params.MppxScope bind the route's scope. ComposeMiddleware panics when
// server.ComposeMiddleware would: offers is empty, an Mpp is nil, realms
// differ, or an offer cannot be built.
func ComposeMiddleware(offers ...server.Offer) ginfw.HandlerFunc {
	_ = server.ComposeMiddleware(offers...) // validate offers up front
	offers = slices.Clone(offers)
	return func(c *ginfw.Context) {
		scope := server.ScopeFromHTTPRequest(c.Request, c.FullPath())
		result, err := server.ComposeRequest(c.Request, scopedOffers(offers, scope)...)
		serve(c, result, err)
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
// runs the rest of the chain for a verified payment, adding its receipt to a
// 2xx response.
func serve(c *ginfw.Context, result *server.ChargeResult, err error) {
	if err != nil || result.IsChallenge() {
		result.ServeHTTP(c.Writer, c.Request)
		c.Abort()
		return
	}

	// The payment has settled: a receipt that cannot be formatted is left
	// out rather than failing the request.
	receipt, _ := mpp.FormatReceiptWithError(result.Receipt)
	ctx := server.ContextWithPayment(c.Request.Context(), result.Credential, result.Receipt)
	c.Request = c.Request.WithContext(ctx)
	c.Set(credentialKey, result.Credential)
	c.Set(receiptKey, result.Receipt)
	writer := &paymentReceiptWriter{
		ResponseWriter: c.Writer,
		receipt:        receipt,
	}
	c.Writer = writer
	c.Next()
	if !writer.Written() {
		writer.WriteHeaderNow()
	}
}

type paymentReceiptWriter struct {
	ginfw.ResponseWriter
	receipt string
}

func (w *paymentReceiptWriter) WriteHeaderNow() {
	if w.Written() {
		return
	}
	w.prepare(w.Status())
	w.ResponseWriter.WriteHeaderNow()
}

func (w *paymentReceiptWriter) Write(body []byte) (int, error) {
	w.WriteHeaderNow()
	return w.ResponseWriter.Write(body)
}

func (w *paymentReceiptWriter) WriteString(body string) (int, error) {
	w.WriteHeaderNow()
	return w.ResponseWriter.WriteString(body)
}

func (w *paymentReceiptWriter) Flush() {
	w.WriteHeaderNow()
	w.ResponseWriter.Flush()
}

func (w *paymentReceiptWriter) prepare(status int) {
	if status < http.StatusOK || status >= http.StatusMultipleChoices {
		w.Header().Del(mpp.HeaderPaymentReceipt)
		return
	}
	w.Header().Set("Cache-Control", "private")
	if w.receipt == "" {
		w.Header().Del(mpp.HeaderPaymentReceipt)
		return
	}
	w.Header().Set(mpp.HeaderPaymentReceipt, w.receipt)
}
