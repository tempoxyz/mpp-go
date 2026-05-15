package server

import (
	"bufio"
	"context"
	"encoding/json"
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
// It calls Mpp.Charge with the provided ChargeParams, injects the incoming
// Authorization header automatically, returns a 402 challenge when payment is
// required, and stores the verified Credential and Receipt in the request
// context on success.
func ChargeMiddleware(m *Mpp, params ChargeParams) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			chargeParams := params
			chargeParams.Authorization = r.Header.Get("Authorization")

			result, err := m.Charge(r.Context(), chargeParams)
			if err != nil {
				WritePaymentError(w, err)
				return
			}

			if result.Challenge != nil {
				WriteChallenge(w, result.Challenge, m.realm)
				return
			}

			serveVerified(next, w, r, result.Credential, result.Receipt)
		})
	}
}

func serveVerified(next http.Handler, w http.ResponseWriter, r *http.Request, credential *mpp.Credential, receipt *mpp.Receipt) {
	ctx := ContextWithPayment(r.Context(), credential, receipt)

	w.Header().Set("Payment-Receipt", receipt.ToPaymentReceipt())

	vw := newVaryResponseWriter(w, "Authorization")
	next.ServeHTTP(vw, r.WithContext(ctx))
	if varyWriter, ok := vw.(interface{ wrote() bool }); ok && !varyWriter.wrote() {
		appendVary(w.Header(), "Authorization")
	}
}

func newVaryResponseWriter(w http.ResponseWriter, vary string) http.ResponseWriter {
	vw := &varyResponseWriter{
		ResponseWriter: w,
		vary:           vary,
	}

	_, hasFlusher := w.(http.Flusher)
	_, hasHijacker := w.(http.Hijacker)
	switch {
	case hasFlusher && hasHijacker:
		return &varyFlusherHijacker{varyResponseWriter: vw}
	case hasFlusher:
		return &varyFlusher{varyResponseWriter: vw}
	case hasHijacker:
		return &varyHijacker{varyResponseWriter: vw}
	default:
		return vw
	}
}

type varyResponseWriter struct {
	http.ResponseWriter
	vary        string
	wroteHeader bool
}

func (w *varyResponseWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

func (w *varyResponseWriter) wrote() bool {
	return w.wroteHeader
}

func (w *varyResponseWriter) WriteHeader(statusCode int) {
	if w.wroteHeader {
		return
	}
	appendVary(w.Header(), w.vary)
	w.wroteHeader = true
	w.ResponseWriter.WriteHeader(statusCode)
}

func (w *varyResponseWriter) Write(body []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(body)
}

type varyFlusher struct {
	*varyResponseWriter
}

func (w *varyFlusher) Flush() {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	w.ResponseWriter.(http.Flusher).Flush()
}

type varyHijacker struct {
	*varyResponseWriter
}

func (w *varyHijacker) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return w.ResponseWriter.(http.Hijacker).Hijack()
}

type varyFlusherHijacker struct {
	*varyResponseWriter
}

func (w *varyFlusherHijacker) Flush() {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	w.ResponseWriter.(http.Flusher).Flush()
}

func (w *varyFlusherHijacker) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return w.ResponseWriter.(http.Hijacker).Hijack()
}

func appendVary(header http.Header, value string) {
	for _, existing := range header.Values("Vary") {
		for _, part := range strings.Split(existing, ",") {
			if strings.EqualFold(strings.TrimSpace(part), value) {
				return
			}
		}
	}
	header.Add("Vary", value)
}

// WriteChallenge serializes a 402 challenge response using RFC 9457 problem details.
func WriteChallenge(w http.ResponseWriter, challenge *mpp.Challenge, realm string) {
	header, err := challenge.ToAuthenticateStrict(realm)
	if err != nil {
		WritePaymentError(w, mpp.ErrInvalidChallenge(challenge.ID, err.Error()))
		return
	}

	w.Header().Set("WWW-Authenticate", header)
	w.Header().Set("Content-Type", "application/problem+json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusPaymentRequired)

	problem := mpp.ErrPaymentRequired(realm, challenge.Description)
	json.NewEncoder(w).Encode(problem.ProblemDetails(""))
}

// WritePaymentError serializes MPP verification errors as problem details.
func WritePaymentError(w http.ResponseWriter, err error) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.Header().Set("Cache-Control", "no-store")

	if pe, ok := err.(*mpp.PaymentError); ok {
		w.WriteHeader(pe.Status)
		json.NewEncoder(w).Encode(pe.ProblemDetails(""))
		return
	}

	w.WriteHeader(http.StatusPaymentRequired)
	problem := mpp.ErrVerificationFailed(err.Error())
	json.NewEncoder(w).Encode(problem.ProblemDetails(""))
}
