package fiberadapter

import (
	"bytes"
	"io"
	"net/http"
	"net/url"
	"slices"
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

// ChargeMiddleware protects a Fiber route with the server charge flow. It
// runs server.Mpp.ChargeRequest with the route's scope unless
// params.MppxScope is set, writes the payment response when payment is
// required or rejected, and on success stores the verified Credential and
// Receipt in the Fiber locals and the user context.
func ChargeMiddleware(m *server.Mpp, params server.ChargeParams) fiberfw.Handler {
	return func(c *fiberfw.Ctx) error {
		params := params
		if len(params.MppxScope) == 0 {
			params.MppxScope = fiberScope(c)
		}
		return handle(c, func(r *http.Request) (*server.ChargeResult, error) {
			return m.ChargeRequest(r, params)
		})
	}
}

// ComposeMiddleware protects a Fiber route with several payment offers, like
// server.ComposeMiddleware: an unpaid request gets every offer, and a
// Credential is verified against the offer it echoes. Offers without their
// own Params.MppxScope bind the route's scope. ComposeMiddleware panics when
// server.ComposeMiddleware would: offers is empty, an Mpp is nil, realms
// differ, or an offer cannot be built.
func ComposeMiddleware(offers ...server.Offer) fiberfw.Handler {
	_ = server.ComposeMiddleware(offers...) // validate offers up front
	offers = slices.Clone(offers)
	return func(c *fiberfw.Ctx) error {
		scoped := scopedOffers(offers, fiberScope(c))
		return handle(c, func(r *http.Request) (*server.ChargeResult, error) {
			return server.ComposeRequest(r, scoped...)
		})
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

// handle reads the request body, runs charge on an equivalent net/http
// request, and writes the payment response or, for a verified payment, runs
// the rest of the chain and adds the receipt to a 2xx response.
func handle(c *fiberfw.Ctx, charge func(*http.Request) (*server.ChargeResult, error)) error {
	if encoding := strings.TrimSpace(c.Get("Content-Encoding")); encoding != "" && !strings.EqualFold(encoding, "identity") {
		WritePaymentResponse(c, &mpp.PaymentError{
			Type:   "about:blank",
			Status: http.StatusUnsupportedMediaType,
			Title:  http.StatusText(http.StatusUnsupportedMediaType),
			Detail: "encoded request bodies are not supported by payment middleware",
		})
		return nil
	}
	body, err := readBody(c)
	if err != nil {
		c.Response().Header.SetConnectionClose()
		WritePaymentResponse(c, server.RequestBodyError(err))
		return nil
	}

	r := httpRequest(c, body)
	result, err := charge(r)
	if err != nil || result.IsChallenge() {
		result.ServeHTTP(newResponseWriter(c), r)
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

// readBody reads the request body, at most server.MaxRequestBodyBytes,
// keeping it available to the protected handler.
func readBody(c *fiberfw.Ctx) ([]byte, error) {
	var body []byte
	if c.Request().IsBodyStream() {
		var err error
		body, err = io.ReadAll(io.LimitReader(c.Request().BodyStream(), server.MaxRequestBodyBytes+1))
		_ = c.Request().CloseBodyStream()
		if err != nil {
			return nil, err
		}
		if len(body) <= server.MaxRequestBodyBytes {
			c.Request().SetBodyRaw(body)
		}
	} else {
		body = c.BodyRaw()
	}
	if len(body) > server.MaxRequestBodyBytes {
		return nil, &http.MaxBytesError{Limit: server.MaxRequestBodyBytes}
	}
	return append([]byte(nil), body...), nil
}

// httpRequest returns the net/http request the charge flow reads: the Fiber
// request's method, path, query, headers, body and user context.
func httpRequest(c *fiberfw.Ctx, body []byte) *http.Request {
	r := &http.Request{
		Method:        c.Method(),
		URL:           &url.URL{Path: c.Path(), RawQuery: string(c.Request().URI().QueryString())},
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		Header:        http.Header{},
		Body:          io.NopCloser(bytes.NewReader(body)),
		ContentLength: int64(len(body)),
		Host:          string(c.Request().Host()),
		RequestURI:    string(c.Request().RequestURI()),
	}
	c.Request().Header.VisitAll(func(key, value []byte) {
		r.Header.Add(string(key), string(value))
	})
	return r.WithContext(c.UserContext())
}

// responseWriter writes a net/http response, such as ChargeResult.ServeHTTP
// writes, to the Fiber response.
type responseWriter struct {
	c           *fiberfw.Ctx
	header      http.Header
	wroteHeader bool
}

func newResponseWriter(c *fiberfw.Ctx) *responseWriter {
	return &responseWriter{c: c, header: http.Header{}}
}

func (w *responseWriter) Header() http.Header { return w.header }

func (w *responseWriter) WriteHeader(status int) {
	if w.wroteHeader {
		return
	}
	w.wroteHeader = true
	w.c.Response().ResetBody()
	for key, values := range w.header {
		w.c.Response().Header.Del(key)
		for i, value := range values {
			if i == 0 {
				w.c.Response().Header.Set(key, value)
				continue
			}
			w.c.Response().Header.Add(key, value)
		}
	}
	w.c.Status(status)
}

func (w *responseWriter) Write(body []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	w.c.Response().AppendBody(body)
	return len(body), nil
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
// This is the Fiber equivalent of [server.WritePaymentResponse], except that
// an initial 402 carries a problem body. Fiber is built on fasthttp and cannot
// use http.ResponseWriter directly.
func WritePaymentResponse(c *fiberfw.Ctx, err error, challenges ...*mpp.Challenge) {
	challenges = slices.DeleteFunc(slices.Clone(challenges), func(ch *mpp.Challenge) bool { return ch == nil })
	if err == nil && len(challenges) > 0 {
		err = mpp.ErrPaymentRequired(challenges[0].Realm, challenges[0].Description)
	}
	server.WritePaymentResponse(newResponseWriter(c), err, challenges...)
}
