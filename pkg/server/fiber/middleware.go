package fiberadapter

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

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
		if len(chargeParams.MppxScope) == 0 {
			chargeParams.MppxScope = fiberScope(c)
		}
		if encoding := strings.TrimSpace(c.Get("Content-Encoding")); encoding != "" && !strings.EqualFold(encoding, "identity") {
			WritePaymentResponse(c, &mpp.PaymentError{
				Type:   "about:blank",
				Status: http.StatusUnsupportedMediaType,
				Title:  http.StatusText(http.StatusUnsupportedMediaType),
				Detail: "encoded request bodies are not supported by payment middleware",
			})
			return nil
		}
		var body []byte
		if c.Request().IsBodyStream() {
			var err error
			body, err = io.ReadAll(io.LimitReader(c.Request().BodyStream(), server.MaxRequestBodyBytes+1))
			_ = c.Request().CloseBodyStream()
			if err != nil {
				c.Response().Header.SetConnectionClose()
				WritePaymentResponse(c, server.RequestBodyError(err))
				return nil
			}
			if len(body) <= server.MaxRequestBodyBytes {
				c.Request().SetBodyRaw(body)
			}
		} else {
			body = c.BodyRaw()
		}
		if len(body) > server.MaxRequestBodyBytes {
			c.Response().Header.SetConnectionClose()
			WritePaymentResponse(c, server.RequestBodyError(&http.MaxBytesError{Limit: server.MaxRequestBodyBytes}))
			return nil
		}
		if len(body) > 0 {
			chargeParams.Body = append([]byte(nil), body...)
		}

		result, err := m.Charge(c.UserContext(), chargeParams)
		if err != nil {
			if result != nil && result.Challenge != nil {
				WritePaymentResponse(c, err, result.Challenges...)
				return nil
			}
			WritePaymentResponse(c, err)
			return nil
		}

		if result.Challenge != nil {
			WritePaymentResponse(c, nil, result.Challenges...)
			return nil
		}

		// The payment has settled: a receipt that cannot be formatted is left
		// out rather than failing the request.
		receipt, _ := mpp.FormatReceiptWithError(result.Receipt)
		ctx := server.ContextWithPayment(c.UserContext(), result.Credential, result.Receipt)
		c.SetUserContext(ctx)
		c.Locals(credentialKey, result.Credential)
		c.Locals(receiptKey, result.Receipt)
		err = c.Next()
		if err != nil || c.Response().StatusCode() < fiberfw.StatusOK || c.Response().StatusCode() >= fiberfw.StatusMultipleChoices {
			c.Response().Header.Del(mpp.HeaderPaymentReceipt)
			return err
		}
		c.Set("Cache-Control", "private")
		if receipt == "" {
			c.Response().Header.Del(mpp.HeaderPaymentReceipt)
			return nil
		}
		c.Set(mpp.HeaderPaymentReceipt, receipt)
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

// WritePaymentResponse writes the payment response for err and challenges,
// each challenge as one WWW-Authenticate field value, in order, with its own
// Realm:
//
//   - err nil: the initial 402 Payment Required offering challenges, with a
//     payment-required problem body. Without challenges it is a 500 internal
//     payment error.
//   - err non-nil: err as RFC 9457 application/problem+json, a non-PaymentError
//     as a 500 internal payment error. Challenges are retry offers, omitted
//     for mpp.ErrPaymentOutcomeUnknown.
//
// This is the Fiber equivalent of [server.WritePaymentResponse]. Fiber is
// built on fasthttp and cannot use http.ResponseWriter directly.
func WritePaymentResponse(c *fiberfw.Ctx, err error, challenges ...*mpp.Challenge) {
	writePaymentResponse(c, err, challenges, func(ch *mpp.Challenge) string { return ch.Realm })
}

func fixedRealm(realm string) func(*mpp.Challenge) string {
	return func(*mpp.Challenge) string { return realm }
}

func writePaymentResponse(c *fiberfw.Ctx, err error, challenges []*mpp.Challenge, realmOf func(*mpp.Challenge) string) {
	kept := make([]*mpp.Challenge, 0, len(challenges))
	for _, challenge := range challenges {
		if challenge != nil {
			kept = append(kept, challenge)
		}
	}
	challenges = kept
	if err == nil {
		if len(challenges) == 0 {
			writeProblem(c, errors.New("fiberadapter: no challenges could be generated"))
			return
		}
		headers, headerErr := authenticateHeaders(challenges, realmOf)
		if headerErr != nil {
			writeProblem(c, headerErr)
			return
		}
		setAuthenticateHeaders(c, headers)
		c.Set("Content-Type", "application/problem+json")
		c.Set("Cache-Control", "no-store")

		problem := mpp.ErrPaymentRequired(realmOf(challenges[0]), challenges[0].Description)
		body, _ := json.Marshal(problem.ProblemDetails(""))
		_ = c.Status(fiberfw.StatusPaymentRequired).Send(body)
		return
	}
	if len(challenges) > 0 && !server.IsPaymentOutcomeUnknown(err) {
		headers, headerErr := authenticateHeaders(challenges, realmOf)
		if headerErr != nil {
			writeProblem(c, headerErr)
			return
		}
		setAuthenticateHeaders(c, headers)
	}
	writeProblem(c, err)
}

// WritePaymentErrorWithChallenge serializes an MPP error with a fresh retry
// challenge, written with realm.
//
// Deprecated: Use WritePaymentResponse(c, err, challenge) instead, which
// writes the challenge's own Realm.
func WritePaymentErrorWithChallenge(c *fiberfw.Ctx, err error, challenge *mpp.Challenge, realm string) {
	writePaymentResponse(c, err, []*mpp.Challenge{challenge}, fixedRealm(realm))
}

// WritePaymentErrorWithChallenges serializes an MPP error with fresh retry
// challenges, one WWW-Authenticate field value per challenge in order, written
// with realm.
//
// Deprecated: Use WritePaymentResponse(c, err, challenges...) instead, which
// writes each challenge's own Realm.
func WritePaymentErrorWithChallenges(c *fiberfw.Ctx, err error, challenges []*mpp.Challenge, realm string) {
	writePaymentResponse(c, err, challenges, fixedRealm(realm))
}

// WriteChallenge serializes a 402 challenge response using RFC 9457 problem
// details, written with realm.
//
// Deprecated: Use WritePaymentResponse(c, nil, challenge) instead, which
// writes the challenge's own Realm.
func WriteChallenge(c *fiberfw.Ctx, challenge *mpp.Challenge, realm string) {
	writePaymentResponse(c, nil, []*mpp.Challenge{challenge}, fixedRealm(realm))
}

// WriteChallenges serializes a 402 response offering every challenge, one
// WWW-Authenticate field value per challenge in presentation order, written
// with realm.
//
// Deprecated: Use WritePaymentResponse(c, nil, challenges...) instead, which
// writes each challenge's own Realm.
func WriteChallenges(c *fiberfw.Ctx, challenges []*mpp.Challenge, realm string) {
	writePaymentResponse(c, nil, challenges, fixedRealm(realm))
}

// authenticateHeaders serializes every challenge before any header is written
// so a later failure cannot leave a partial set of offers. A failure is
// classified by server.ChallengeSerializationError.
func authenticateHeaders(challenges []*mpp.Challenge, realmOf func(*mpp.Challenge) string) ([]string, error) {
	headers := make([]string, 0, len(challenges))
	for _, challenge := range challenges {
		realm := realmOf(challenge)
		formatted := challenge
		if challenge.Realm != realm {
			copied := *challenge
			copied.Realm = realm
			formatted = &copied
		}
		header, err := mpp.FormatChallenge(formatted)
		if err != nil {
			return nil, server.ChallengeSerializationError(challenge, realm, err)
		}
		headers = append(headers, header)
	}
	return headers, nil
}

func setAuthenticateHeaders(c *fiberfw.Ctx, values []string) {
	c.Response().Header.Del(mpp.HeaderWWWAuthenticate)
	for _, value := range values {
		c.Response().Header.Add(mpp.HeaderWWWAuthenticate, value)
	}
}

// WritePaymentError serializes MPP verification errors as problem details.
//
// Deprecated: Use WritePaymentResponse instead, which also writes initial and
// retry challenges.
func WritePaymentError(c *fiberfw.Ctx, err error) {
	writeProblem(c, err)
}

func writeProblem(c *fiberfw.Ctx, err error) {
	c.Set("Content-Type", "application/problem+json")
	c.Set("Cache-Control", "no-store")

	var pe *mpp.PaymentError
	if errors.As(err, &pe) {
		body, _ := json.Marshal(pe.ProblemDetails(""))
		_ = c.Status(pe.Status).Send(body)
		return
	}

	// See server.WritePaymentResponse: never echo an unexpected error's text.
	problem := mpp.ErrInternalPayment().WithCause(err)
	body, _ := json.Marshal(problem.ProblemDetails(""))

	_ = c.Status(problem.Status).Send(body)
}
