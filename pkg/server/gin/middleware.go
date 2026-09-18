package ginadapter

import (
	"net/http"

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

// ChargeMiddleware protects a Gin route with the server charge flow.
func ChargeMiddleware(m *server.Mpp, params server.ChargeParams) ginfw.HandlerFunc {
	return func(c *ginfw.Context) {
		chargeParams := params
		chargeParams.Authorization = c.GetHeader(mpp.HeaderAuthorization)
		chargeParams.PaymentAuthorization = c.GetHeader(mpp.HeaderPaymentAuthorization)
		chargeParams.MppxScope = server.ScopeFromHTTPRequest(c.Request, c.FullPath())
		body, err := server.ReadRequestBody(c.Request)
		if err != nil {
			server.WritePaymentError(c.Writer, mpp.ErrBadRequest("failed to read request body"))
			c.Abort()
			return
		}
		if len(body) > 0 {
			chargeParams.Body = body
		}

		result, err := m.Charge(c.Request.Context(), chargeParams)
		if err != nil {
			if result != nil && result.Challenge != nil {
				server.WritePaymentErrorWithChallenge(c.Writer, err, result.Challenge, m.Realm())
				c.Abort()
				return
			}
			server.WritePaymentError(c.Writer, err)
			c.Abort()
			return
		}

		if result.Challenge != nil {
			server.WriteChallenge(c.Writer, result.Challenge, m.Realm())
			c.Abort()
			return
		}

		ctx := server.ContextWithPayment(c.Request.Context(), result.Credential, result.Receipt)
		c.Request = c.Request.WithContext(ctx)
		c.Set(credentialKey, result.Credential)
		c.Set(receiptKey, result.Receipt)
		writer := &paymentReceiptWriter{
			ResponseWriter: c.Writer,
			receipt:        result.Receipt.ToPaymentReceipt(),
		}
		c.Writer = writer
		c.Next()
		if !writer.Written() {
			writer.WriteHeaderNow()
		}
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

func (w *paymentReceiptWriter) prepare(status int) {
	if status < http.StatusOK || status >= http.StatusMultipleChoices {
		w.Header().Del(mpp.HeaderPaymentReceipt)
		return
	}
	w.Header().Set("Cache-Control", "private")
	w.Header().Set(mpp.HeaderPaymentReceipt, w.receipt)
}
