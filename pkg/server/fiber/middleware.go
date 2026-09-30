package fiberadapter

import (
	"encoding/json"
	"errors"

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
		chargeParams.Authorization = c.Get(mpp.HeaderAuthorization)
		chargeParams.PaymentAuthorization = c.Get(mpp.HeaderPaymentAuthorization)
		chargeParams.MppxScope = fiberScope(c)
		if body := c.Body(); len(body) > 0 {
			chargeParams.Body = append([]byte(nil), body...)
		}

		result, err := m.Charge(c.UserContext(), chargeParams)
		if err != nil {
			if result != nil && result.Challenge != nil {
				WritePaymentErrorWithChallenges(c, err, result.Challenges, m.Realm())
				return nil
			}
			WritePaymentError(c, err)
			return nil
		}

		if result.Challenge != nil {
			WriteChallenges(c, result.Challenges, m.Realm())
			return nil
		}

		ctx := server.ContextWithPayment(c.UserContext(), result.Credential, result.Receipt)
		c.SetUserContext(ctx)
		c.Locals(credentialKey, result.Credential)
		c.Locals(receiptKey, result.Receipt)
		err = c.Next()
		if err != nil || c.Response().StatusCode() >= fiberfw.StatusBadRequest {
			c.Response().Header.Del(mpp.HeaderPaymentReceipt)
			return err
		}
		c.Set("Cache-Control", "private")
		c.Set(mpp.HeaderPaymentReceipt, result.Receipt.ToPaymentReceipt())
		return nil
	}
}

func fiberScope(c *fiberfw.Ctx) map[string]string {
	scope := map[string]string{}
	if route := c.Route(); route != nil && route.Path != "" {
		scope["route"] = route.Path
	}
	if path := c.Path(); path != "" {
		scope["resource"] = path
	}
	if query := string(c.Request().URI().QueryString()); query != "" {
		scope["query"] = query
	}
	if len(scope) == 0 {
		return nil
	}
	return scope
}

// WritePaymentErrorWithChallenge serializes an MPP error with a fresh retry challenge.
func WritePaymentErrorWithChallenge(c *fiberfw.Ctx, err error, challenge *mpp.Challenge, realm string) {
	if challenge == nil {
		WritePaymentError(c, err)
		return
	}
	WritePaymentErrorWithChallenges(c, err, []*mpp.Challenge{challenge}, realm)
}

// WritePaymentErrorWithChallenges serializes an MPP error with fresh retry
// challenges, one WWW-Authenticate field value per challenge in order.
func WritePaymentErrorWithChallenges(c *fiberfw.Ctx, err error, challenges []*mpp.Challenge, realm string) {
	if len(challenges) == 0 {
		WritePaymentError(c, err)
		return
	}
	headers, challengeID, headerErr := authenticateHeaders(challenges, realm)
	if headerErr != nil {
		WritePaymentError(c, mpp.ErrInvalidChallenge(challengeID, headerErr.Error()))
		return
	}

	setAuthenticateHeaders(c, headers)
	WritePaymentError(c, err)
}

// WriteChallenge serializes a 402 challenge response using RFC 9457 problem details.
//
// This is the Fiber equivalent of [server.WriteChallenge]. Fiber is built on
// fasthttp and cannot use http.ResponseWriter directly.
func WriteChallenge(c *fiberfw.Ctx, challenge *mpp.Challenge, realm string) {
	WriteChallenges(c, []*mpp.Challenge{challenge}, realm)
}

// WriteChallenges serializes a 402 response offering every challenge, one
// WWW-Authenticate field value per challenge in presentation order.
//
// This is the Fiber equivalent of [server.WriteChallenges].
func WriteChallenges(c *fiberfw.Ctx, challenges []*mpp.Challenge, realm string) {
	if len(challenges) == 0 {
		WritePaymentError(c, mpp.ErrBadRequest("no challenges could be generated"))
		return
	}
	headers, _, err := authenticateHeaders(challenges, realm)
	if err != nil {
		WritePaymentError(c, mpp.ErrBadRequest(err.Error()))
		return
	}

	setAuthenticateHeaders(c, headers)
	c.Set("Content-Type", "application/problem+json")
	c.Set("Cache-Control", "no-store")

	problem := mpp.ErrPaymentRequired(realm, challenges[0].Description)
	body, _ := json.Marshal(problem.ProblemDetails(""))

	c.Status(fiberfw.StatusPaymentRequired).Send(body) //nolint:errcheck // matches server.WriteChallenge behavior
}

// authenticateHeaders serializes every challenge before any header is written
// so a later failure cannot leave a partial set of offers. On failure it also
// returns the ID of the challenge that could not be serialized.
func authenticateHeaders(challenges []*mpp.Challenge, realm string) ([]string, string, error) {
	headers := make([]string, 0, len(challenges))
	for _, challenge := range challenges {
		header, err := challenge.ToAuthenticateStrict(realm)
		if err != nil {
			return nil, challenge.ID, err
		}
		headers = append(headers, header)
	}
	return headers, "", nil
}

func setAuthenticateHeaders(c *fiberfw.Ctx, values []string) {
	c.Response().Header.Del(mpp.HeaderWWWAuthenticate)
	for _, value := range values {
		c.Response().Header.Add(mpp.HeaderWWWAuthenticate, value)
	}
}

// WritePaymentError serializes MPP verification errors as problem details.
//
// This is the Fiber equivalent of [server.WritePaymentError]. Fiber is built on
// fasthttp and cannot use http.ResponseWriter directly.
func WritePaymentError(c *fiberfw.Ctx, err error) {
	c.Set("Content-Type", "application/problem+json")
	c.Set("Cache-Control", "no-store")

	var pe *mpp.PaymentError
	if errors.As(err, &pe) {
		body, _ := json.Marshal(pe.ProblemDetails(""))
		c.Status(pe.Status).Send(body) //nolint:errcheck // matches server.WritePaymentError behavior
		return
	}

	problem := mpp.ErrVerificationFailed(err.Error())
	body, _ := json.Marshal(problem.ProblemDetails(""))

	c.Status(fiberfw.StatusPaymentRequired).Send(body) //nolint:errcheck // matches server.WritePaymentError behavior
}
