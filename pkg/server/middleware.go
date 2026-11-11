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

// ScopeFromHTTPRequest returns the route scope bound into charge Challenges,
// as mppx's framework adapters attach it: the request's upper-case method, a
// space, and route, such as "GET /items/:id". An empty route defaults to the
// path of r.Pattern (the http.ServeMux pattern that matched, without its
// method or host), then to the request's escaped path. The query is never part of the
// scope. It returns "" when there is no route or path.
func ScopeFromHTTPRequest(r *http.Request, route string) string {
	if r == nil {
		return ""
	}
	if route == "" {
		route = r.Pattern
		if _, path, ok := strings.Cut(route, " "); ok {
			route = strings.TrimLeft(path, " \t")
		}
		if i := strings.IndexByte(route, '/'); i > 0 {
			route = route[i:] // drop a host
		}
	}
	if route == "" && r.URL != nil {
		// The escaped path, as mppx binds url.pathname: the decoded form can
		// hold invalid UTF-8, which the JSON opaque cannot carry byte for byte.
		route = r.URL.EscapedPath()
	}
	if route == "" {
		return ""
	}
	method := r.Method
	if method == "" {
		method = http.MethodGet
	}
	return strings.ToUpper(method) + " " + route
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
	wrapped := &paymentReceiptWriter{ResponseWriter: w, receipt: header}
	next.ServeHTTP(wrapped, r.WithContext(ctx))
	if !wrapped.wroteHeader && !wrapped.hijacked {
		wrapped.WriteHeader(http.StatusOK)
	}
}

// DeferPaymentReceipt returns a writer that adds the receipt only when a
// 2xx response status is committed. Call complete after the handler
// returns to stage the receipt for an otherwise empty successful response. All
// later status writes must use the returned writer, including framework error
// handlers. A receipt mpp.FormatReceiptWithError rejects is left out.
func DeferPaymentReceipt(w http.ResponseWriter, receipt *mpp.Receipt) (http.ResponseWriter, func()) {
	header, _ := mpp.FormatReceiptWithError(receipt)
	return deferPaymentReceiptHeader(w, header)
}

func deferPaymentReceiptHeader(w http.ResponseWriter, receipt string) (http.ResponseWriter, func()) {
	wrapped := &paymentReceiptWriter{ResponseWriter: w, receipt: receipt}
	return wrapped, func() {
		if !wrapped.wroteHeader && !wrapped.hijacked {
			// Outer middleware may still have a buffered status to flush.
			// Stage headers for net/http's implicit 200 without committing it;
			// a later WriteHeader can still remove the receipt on failure.
			setPaymentReceiptForStatus(wrapped.Header(), http.StatusOK, wrapped.receipt)
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
	challenges = nonNilChallenges(challenges)
	if err == nil {
		if len(challenges) == 0 {
			writeProblem(w, errNoChallenges)
			return
		}
		headers, headerErr := authenticateHeaders(challenges)
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
		headers, headerErr := authenticateHeaders(challenges)
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

// IsPaymentOutcomeUnknown reports whether err is an
// mpp.ErrPaymentOutcomeUnknown problem. Writers send it without retry
// challenges, since the credential it answers may already have settled.
func IsPaymentOutcomeUnknown(err error) bool {
	return errors.Is(err, mpp.ErrorTypePaymentOutcomeUnknown)
}

// authenticateHeaders serializes every challenge before any header is written
// so a later failure cannot leave a partial set of offers. A failure is
// classified by ChallengeSerializationError.
func authenticateHeaders(challenges []*mpp.Challenge) ([]string, error) {
	headers := make([]string, 0, len(challenges))
	for _, challenge := range challenges {
		header, err := mpp.FormatChallenge(challenge)
		if err != nil {
			return nil, ChallengeSerializationError(challenge, challenge.Realm, err)
		}
		headers = append(headers, header)
	}
	return headers, nil
}

// formatChallengeWithRealm formats challenge with realm in place of its own
// Realm, for ChallengeSerializationError.
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
// it as a 500 internal payment error) unless the route scope bound into the
// challenge's opaque _mppx_scope made the header too large:
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
// would serialize without the route scope in its opaque _mppx_scope, which
// carries the request path when no route template is known.
func fitsWithoutRequestURI(challenge *mpp.Challenge, realm string) bool {
	if challenge.Opaque[mppxScopeKey] == "" {
		return false
	}
	opaque := make(map[string]string, len(challenge.Opaque))
	for k, v := range challenge.Opaque {
		if k != mppxScopeKey {
			opaque[k] = v
		}
	}
	probe := *challenge
	probe.Opaque = opaque
	_, err := formatChallengeWithRealm(&probe, realm)
	return err == nil
}

func setAuthenticateHeaders(header http.Header, values []string) {
	header.Del(mpp.HeaderWWWAuthenticate)
	for _, value := range values {
		header.Add(mpp.HeaderWWWAuthenticate, value)
	}
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
