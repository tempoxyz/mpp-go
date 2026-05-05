package fiberadapter

import (
	"encoding/json"

	fiberfw "github.com/gofiber/fiber/v2"
	"github.com/tempoxyz/mpp-go/pkg/mpp"
	"github.com/tempoxyz/mpp-go/pkg/server"
)

const (
	credentialKey = "mpp.credential"
	receiptKey    = "mpp.receipt"
)

// Credential returns the verified credential stored on the Fiber context.
func Credential(c *fiberfw.Ctx) *mpp.Credential {
	v := c.Locals(credentialKey)
	credential, _ := v.(*mpp.Credential)
	return credential
}

// Receipt returns the verified receipt stored on the Fiber context.
func Receipt(c *fiberfw.Ctx) *mpp.Receipt {
	v := c.Locals(receiptKey)
	receipt, _ := v.(*mpp.Receipt)
	return receipt
}

// ChargeMiddleware protects a Fiber route with the server charge flow.
func ChargeMiddleware(m *server.Mpp, params server.ChargeParams) fiberfw.Handler {
	return func(c *fiberfw.Ctx) error {
		chargeParams := params
		chargeParams.Authorization = c.Get("Authorization")

		result, err := m.Charge(c.UserContext(), chargeParams)
		if err != nil {
			WritePaymentError(c, err)
			return nil
		}

		if result.Challenge != nil {
			WriteChallenge(c, result.Challenge, m.Realm())
			return nil
		}

		ctx := server.ContextWithPayment(c.UserContext(), result.Credential, result.Receipt)
		c.SetUserContext(ctx)
		c.Locals(credentialKey, result.Credential)
		c.Locals(receiptKey, result.Receipt)
		c.Set("Payment-Receipt", result.Receipt.ToPaymentReceipt())
		return c.Next()
	}
}

// WriteChallenge serializes a 402 challenge response using RFC 9457 problem details.
//
// This is the Fiber equivalent of [server.WriteChallenge]. Fiber is built on
// fasthttp and cannot use http.ResponseWriter directly.
func WriteChallenge(c *fiberfw.Ctx, challenge *mpp.Challenge, realm string) {
	c.Set("WWW-Authenticate", challenge.ToAuthenticate(realm))
	c.Set("Content-Type", "application/problem+json")
	c.Set("Cache-Control", "no-store")

	problem := mpp.ErrPaymentRequired(realm, challenge.Description)
	body, _ := json.Marshal(problem.ProblemDetails(""))

	c.Status(fiberfw.StatusPaymentRequired).Send(body) //nolint:errcheck // matches server.WriteChallenge behavior
}

// WritePaymentError serializes MPP verification errors as problem details.
//
// This is the Fiber equivalent of [server.WritePaymentError]. Fiber is built on
// fasthttp and cannot use http.ResponseWriter directly.
func WritePaymentError(c *fiberfw.Ctx, err error) {
	c.Set("Content-Type", "application/problem+json")
	c.Set("Cache-Control", "no-store")

	if pe, ok := err.(*mpp.PaymentError); ok {
		body, _ := json.Marshal(pe.ProblemDetails(""))
		c.Status(pe.Status).Send(body) //nolint:errcheck // matches server.WritePaymentError behavior
		return
	}

	problem := mpp.ErrVerificationFailed(err.Error())
	body, _ := json.Marshal(problem.ProblemDetails(""))

	c.Status(fiberfw.StatusPaymentRequired).Send(body) //nolint:errcheck // matches server.WritePaymentError behavior
}
