package ginadapter

import (
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
		chargeParams.Authorization = c.GetHeader("Authorization")

		result, err := m.Charge(c.Request.Context(), chargeParams)
		if err != nil {
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
		c.Writer.Header().Set("Payment-Receipt", result.Receipt.ToPaymentReceipt())
		c.Next()
	}
}
