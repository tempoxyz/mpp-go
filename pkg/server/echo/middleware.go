package echoadapter

import (
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

// ChargeMiddleware protects an Echo route with the server charge flow.
func ChargeMiddleware(m *server.Mpp, params server.ChargeParams) echofw.MiddlewareFunc {
	return func(next echofw.HandlerFunc) echofw.HandlerFunc {
		return func(c echofw.Context) error {
			chargeParams := params
			chargeParams.Authorization = c.Request().Header.Get("Authorization")

			result, err := m.Charge(c.Request().Context(), chargeParams)
			if err != nil {
				server.WritePaymentError(c.Response(), err)
				return nil
			}

			if result.Challenge != nil {
				server.WriteChallenge(c.Response(), result.Challenge, m.Realm())
				return nil
			}

			ctx := server.ContextWithPayment(c.Request().Context(), result.Credential, result.Receipt)
			c.SetRequest(c.Request().WithContext(ctx))
			c.Set(credentialKey, result.Credential)
			c.Set(receiptKey, result.Receipt)
			c.Response().Header().Set("Payment-Receipt", result.Receipt.ToPaymentReceipt())
			return next(c)
		}
	}
}
