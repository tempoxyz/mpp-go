package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"

	"github.com/tempoxyz/mpp-go/pkg/mpp"
)

type contextKey int

const (
	credentialKey contextKey = iota
	receiptKey
)

// ScopeFromHTTPRequest returns the framework scope bound into charge requests.
func ScopeFromHTTPRequest(r *http.Request, route string) map[string]string {
	scope := map[string]string{}
	if route == "" {
		route = r.Pattern
	}
	if method, path, ok := strings.Cut(route, " "); ok && method != "" && path != "" {
		route = path
	}
	if route != "" {
		scope["route"] = route
	}
	if r.URL != nil {
		if r.URL.Path != "" {
			scope["resource"] = r.URL.Path
		}
		if r.URL.RawQuery != "" {
			scope["query"] = r.URL.RawQuery
		}
	}
	if len(scope) == 0 {
		return nil
	}
	return scope
}

// ContextWithPayment stores the verified payment objects on a request context.
func ContextWithPayment(ctx context.Context, credential *mpp.Credential, receipt *mpp.Receipt) context.Context {
	ctx = context.WithValue(ctx, credentialKey, credential)
	ctx = context.WithValue(ctx, receiptKey, receipt)
	return ctx
}

// CredentialFromContext extracts the Credential from the request context.
func CredentialFromContext(ctx context.Context) *mpp.Credential {
	v, _ := ctx.Value(credentialKey).(*mpp.Credential)
	return v
}

// ReceiptFromContext extracts the Receipt from the request context.
func ReceiptFromContext(ctx context.Context) *mpp.Receipt {
	v, _ := ctx.Value(receiptKey).(*mpp.Receipt)
	return v
}

// ChargeMiddleware creates an http.Handler middleware for the charge intent.
//
// It runs Mpp.ChargeRequest for each request with the provided ChargeParams,
// which reads the incoming Authorization or Payment-Authorization header, body
// and route scope, writes the 402 challenge or problem response when payment
// is required or rejected, and stores the verified Credential and Receipt in
// the request context on success. It behaves as ComposeMiddleware with a
// single offer.
func ChargeMiddleware(m *Mpp, params ChargeParams) func(http.Handler) http.Handler {
	return m.flow(params).middleware()
}

// errNoChallenges reports a server configuration that issued no offers. It is
// not a PaymentError, so WritePaymentResponse reports it as an internal error.
var errNoChallenges = errors.New("server: no challenges could be generated")

// MaxRequestBodyBytes bounds the body buffered for payment digest verification.
// Configure the HTTP server's read timeout separately to bound slow uploads.
const MaxRequestBodyBytes = 4 << 20

// RequestBodyError maps oversized bodies to HTTP 413 and other read failures to 400.
func RequestBodyError(err error) *mpp.PaymentError {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		return &mpp.PaymentError{
			Type:   "about:blank",
			Status: http.StatusRequestEntityTooLarge,
			Title:  http.StatusText(http.StatusRequestEntityTooLarge),
			Detail: "request body exceeds payment middleware limit",
		}
	}
	return mpp.ErrBadRequest("failed to read request body")
}

// ReadRequestBody reads and restores r.Body so middleware can verify body digests
// without consuming the body before the protected handler runs.
func ReadRequestBody(r *http.Request) ([]byte, error) {
	if r.Body == nil {
		return nil, nil
	}
	original := r.Body
	defer original.Close()
	if r.ContentLength > MaxRequestBodyBytes {
		return nil, &http.MaxBytesError{Limit: MaxRequestBodyBytes}
	}
	body, err := io.ReadAll(io.LimitReader(original, MaxRequestBodyBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > MaxRequestBodyBytes {
		return nil, &http.MaxBytesError{Limit: MaxRequestBodyBytes}
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	return body, nil
}

func serveVerified(next http.Handler, w http.ResponseWriter, r *http.Request, credential *mpp.Credential, receipt *mpp.Receipt) {
	// The payment has settled, so the payer gets the resource even when the
	// receipt cannot be formatted: the Payment-Receipt header is left out
	// rather than failing the request.
	header, _ := mpp.FormatReceiptWithError(receipt)
	ctx := ContextWithPayment(r.Context(), credential, receipt)
	wrapped, complete := deferPaymentReceiptHeader(w, header)
	next.ServeHTTP(wrapped, r.WithContext(ctx))
	complete()
}

// DeferPaymentReceipt returns a writer that adds the receipt only when a
// 2xx response status is committed. Call complete after the handler
// returns to commit an otherwise empty successful response. A receipt
// mpp.FormatReceiptWithError rejects is left out.
func DeferPaymentReceipt(w http.ResponseWriter, receipt *mpp.Receipt) (http.ResponseWriter, func()) {
	header, _ := mpp.FormatReceiptWithError(receipt)
	return deferPaymentReceiptHeader(w, header)
}

func deferPaymentReceiptHeader(w http.ResponseWriter, receipt string) (http.ResponseWriter, func()) {
	wrapped := &paymentReceiptWriter{ResponseWriter: w, receipt: receipt}
	return wrapped, func() {
		if !wrapped.wroteHeader && !wrapped.hijacked {
			wrapped.WriteHeader(http.StatusOK)
		}
	}
}

type paymentReceiptWriter struct {
	http.ResponseWriter
	receipt     string
	wroteHeader bool
	// hijacked is set once the handler takes over the connection, after
	// which nothing may be written through the ResponseWriter.
	hijacked bool
}

func (w *paymentReceiptWriter) WriteHeader(status int) {
	if w.wroteHeader {
		return
	}
	if status >= 100 && status < 200 && status != http.StatusSwitchingProtocols {
		w.Header().Del(mpp.HeaderPaymentReceipt)
		w.ResponseWriter.WriteHeader(status)
		return
	}
	w.wroteHeader = true
	setPaymentReceiptForStatus(w.Header(), status, w.receipt)
	w.ResponseWriter.WriteHeader(status)
}

func (w *paymentReceiptWriter) Write(body []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(body)
}

func (w *paymentReceiptWriter) Flush() {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	_ = http.NewResponseController(w.ResponseWriter).Flush()
}

func (w *paymentReceiptWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hijacker, ok := w.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, errors.New("http.ResponseWriter does not support hijacking")
	}
	conn, rw, err := hijacker.Hijack()
	if err == nil {
		w.hijacked = true
	}
	return conn, rw, err
}

func (w *paymentReceiptWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func setPaymentReceiptForStatus(header http.Header, status int, receipt string) {
	if status < http.StatusOK || status >= http.StatusMultipleChoices {
		header.Del(mpp.HeaderPaymentReceipt)
		return
	}
	header.Set("Cache-Control", "private")
	if receipt == "" {
		header.Del(mpp.HeaderPaymentReceipt)
		return
	}
	header.Set(mpp.HeaderPaymentReceipt, receipt)
}

// WritePaymentResponse writes the payment response for err and challenges,
// each challenge as one WWW-Authenticate field value, in order, with its own
// Realm (the realm its ID binds):
//
//   - err nil: the initial 402 Payment Required offering challenges, with
//     Cache-Control: no-store. Without challenges it is a 500 internal
//     payment error, since the server issued no offers.
//   - err non-nil: err as RFC 9457 application/problem+json. A PaymentError
//     is written with its own status; any other error is a 500 internal
//     payment error whose text never reaches the client. Challenges are
//     retry offers, omitted for ErrPaymentOutcomeUnknown, since the
//     credential it answers may already have settled.
//
// Every challenge is serialized before any header is written, so a failure
// cannot leave a partial set of offers; it is classified by
// ChallengeSerializationError (414 or 500). Nil challenges are skipped.
func WritePaymentResponse(w http.ResponseWriter, err error, challenges ...*mpp.Challenge) {
	writePaymentResponse(w, err, challenges, challengeRealm)
}

func challengeRealm(c *mpp.Challenge) string { return c.Realm }

func fixedRealm(realm string) func(*mpp.Challenge) string {
	return func(*mpp.Challenge) string { return realm }
}

func writePaymentResponse(w http.ResponseWriter, err error, challenges []*mpp.Challenge, realmOf func(*mpp.Challenge) string) {
	challenges = nonNilChallenges(challenges)
	if err == nil {
		if len(challenges) == 0 {
			writeProblem(w, errNoChallenges)
			return
		}
		headers, headerErr := authenticateHeaders(challenges, realmOf)
		if headerErr != nil {
			writeProblem(w, headerErr)
			return
		}
		setAuthenticateHeaders(w.Header(), headers)
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusPaymentRequired)
		return
	}
	if len(challenges) > 0 && !IsPaymentOutcomeUnknown(err) {
		headers, headerErr := authenticateHeaders(challenges, realmOf)
		if headerErr != nil {
			writeProblem(w, headerErr)
			return
		}
		setAuthenticateHeaders(w.Header(), headers)
	}
	writeProblem(w, err)
}

func nonNilChallenges(challenges []*mpp.Challenge) []*mpp.Challenge {
	for _, challenge := range challenges {
		if challenge == nil {
			kept := make([]*mpp.Challenge, 0, len(challenges))
			for _, c := range challenges {
				if c != nil {
					kept = append(kept, c)
				}
			}
			return kept
		}
	}
	return challenges
}

// WritePaymentErrorWithChallenge serializes an MPP error with a fresh retry
// challenge, written with realm.
//
// Deprecated: Use WritePaymentResponse(w, err, challenge) instead, which
// writes the challenge's own Realm.
func WritePaymentErrorWithChallenge(w http.ResponseWriter, err error, challenge *mpp.Challenge, realm string) {
	writePaymentResponse(w, err, []*mpp.Challenge{challenge}, fixedRealm(realm))
}

// WritePaymentErrorWithChallenges serializes an MPP error with fresh retry
// challenges, one WWW-Authenticate field value per challenge in order, written
// with realm.
//
// Deprecated: Use WritePaymentResponse(w, err, challenges...) instead, which
// writes each challenge's own Realm.
func WritePaymentErrorWithChallenges(w http.ResponseWriter, err error, challenges []*mpp.Challenge, realm string) {
	writePaymentResponse(w, err, challenges, fixedRealm(realm))
}

// IsPaymentOutcomeUnknown reports whether err is an
// mpp.ErrPaymentOutcomeUnknown problem. Writers send it without retry
// challenges, since the credential it answers may already have settled.
func IsPaymentOutcomeUnknown(err error) bool {
	return errors.Is(err, mpp.ErrorTypePaymentOutcomeUnknown)
}

// WriteChallenge serializes an initial 402 challenge response, written with
// realm.
//
// Deprecated: Use WritePaymentResponse(w, nil, challenge) instead, which
// writes the challenge's own Realm.
func WriteChallenge(w http.ResponseWriter, challenge *mpp.Challenge, realm string) {
	writePaymentResponse(w, nil, []*mpp.Challenge{challenge}, fixedRealm(realm))
}

// WriteChallenges serializes an initial 402 response offering every challenge,
// one WWW-Authenticate field value per challenge in presentation order,
// written with realm.
//
// Deprecated: Use WritePaymentResponse(w, nil, challenges...) instead, which
// writes each challenge's own Realm.
func WriteChallenges(w http.ResponseWriter, challenges []*mpp.Challenge, realm string) {
	writePaymentResponse(w, nil, challenges, fixedRealm(realm))
}

// authenticateHeaders serializes every challenge before any header is written
// so a later failure cannot leave a partial set of offers. A failure is
// classified by ChallengeSerializationError.
func authenticateHeaders(challenges []*mpp.Challenge, realmOf func(*mpp.Challenge) string) ([]string, error) {
	headers := make([]string, 0, len(challenges))
	for _, challenge := range challenges {
		realm := realmOf(challenge)
		header, err := formatChallengeWithRealm(challenge, realm)
		if err != nil {
			return nil, ChallengeSerializationError(challenge, realm, err)
		}
		headers = append(headers, header)
	}
	return headers, nil
}

// FormatChallengeWithRealm formats challenge with realm in place of its own
// Realm, for writers that predate mpp.FormatChallenge.
func formatChallengeWithRealm(challenge *mpp.Challenge, realm string) (string, error) {
	if challenge == nil || challenge.Realm == realm {
		return mpp.FormatChallenge(challenge)
	}
	c := *challenge
	c.Realm = realm
	return mpp.FormatChallenge(&c)
}

// ChallengeSerializationError classifies err, a failure to serialize
// challenge as a WWW-Authenticate value for realm. Challenges come from server
// configuration, so the failure is a server error (WritePaymentResponse reports
// it as a 500 internal payment error) unless the request-derived resource or
// query bound into the challenge's _mppx_scope made the header too large:
// then the request URI is at fault and the result is a 414 URI Too Long
// PaymentError.
func ChallengeSerializationError(challenge *mpp.Challenge, realm string, err error) error {
	if challenge != nil && fitsWithoutRequestURI(challenge, realm) {
		return &mpp.PaymentError{
			Type:   "about:blank",
			Status: http.StatusRequestURITooLong,
			Title:  http.StatusText(http.StatusRequestURITooLong),
			Detail: "request URI is too long to bind into a payment challenge",
		}
	}
	return fmt.Errorf("server: serializing challenge: %w", err)
}

// fitsWithoutRequestURI reports whether challenge, which failed to serialize,
// would serialize without the resource and query of its _mppx_scope.
func fitsWithoutRequestURI(challenge *mpp.Challenge, realm string) bool {
	var trimmed map[string]any
	switch scope := challenge.Request["_mppx_scope"].(type) {
	case map[string]string:
		if scope["resource"] == "" && scope["query"] == "" {
			return false
		}
		trimmed = make(map[string]any, len(scope))
		for k, v := range scope {
			trimmed[k] = v
		}
	case map[string]any:
		if scope["resource"] == nil && scope["query"] == nil {
			return false
		}
		trimmed = make(map[string]any, len(scope))
		for k, v := range scope {
			trimmed[k] = v
		}
	default:
		return false
	}
	delete(trimmed, "resource")
	delete(trimmed, "query")

	request := make(map[string]any, len(challenge.Request))
	for k, v := range challenge.Request {
		request[k] = v
	}
	request["_mppx_scope"] = trimmed
	probe := *challenge
	probe.Request = request
	probe.RequestB64 = ""
	_, err := formatChallengeWithRealm(&probe, realm)
	return err == nil
}

func setAuthenticateHeaders(header http.Header, values []string) {
	header.Del(mpp.HeaderWWWAuthenticate)
	for _, value := range values {
		header.Add(mpp.HeaderWWWAuthenticate, value)
	}
}

// WritePaymentError serializes MPP verification errors as problem details.
//
// Deprecated: Use WritePaymentResponse instead, which also writes initial and
// retry challenges.
func WritePaymentError(w http.ResponseWriter, err error) {
	writeProblem(w, err)
}

// writeProblem serializes err as RFC 9457 problem details.
func writeProblem(w http.ResponseWriter, err error) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.Header().Set("Cache-Control", "no-store")

	var pe *mpp.PaymentError
	if errors.As(err, &pe) {
		w.WriteHeader(pe.Status)
		json.NewEncoder(w).Encode(pe.ProblemDetails(""))
		return
	}

	// Anything that is not a PaymentError is an unexpected server-side
	// failure: report a fixed internal-payment-error so its text (RPC URLs,
	// infrastructure details) never reaches the client.
	problem := mpp.ErrInternalPayment().WithCause(err)
	w.WriteHeader(problem.Status)
	json.NewEncoder(w).Encode(problem.ProblemDetails(""))
}
