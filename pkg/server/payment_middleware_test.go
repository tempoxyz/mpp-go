package server

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/tempoxyz/mpp-go/pkg/mpp"
)

func TestPaymentMiddleware_ChargeIntentIssuesChallenge(t *testing.T) {
	payment := New(middlewareTestMethod{}, "api.example.com", "secret-key")
	handler := PaymentMiddleware(payment, "0.50")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/paid", nil)
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusPaymentRequired {
		t.Fatalf("response.Code = %d, want %d", response.Code, http.StatusPaymentRequired)
	}
	if _, err := mpp.ParseChallenge(response.Header().Get("WWW-Authenticate")); err != nil {
		t.Fatalf("ParseChallenge() error = %v", err)
	}
}

func TestPaymentMiddleware_UnsupportedIntentReturnsInternalServerError(t *testing.T) {
	payment := New(middlewareTestMethod{}, "api.example.com", "secret-key")
	handler := PaymentMiddleware(payment, "0.50", WithIntent("session"))(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/paid", nil)
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusInternalServerError {
		t.Fatalf("response.Code = %d, want %d", response.Code, http.StatusInternalServerError)
	}
}

func TestWritePaymentError_FormatsProblemResponses(t *testing.T) {
	t.Parallel()

	paymentErrorResponse := httptest.NewRecorder()
	writePaymentError(paymentErrorResponse, mpp.ErrBadRequest("bad request"))
	if paymentErrorResponse.Code != http.StatusBadRequest {
		t.Fatalf("paymentErrorResponse.Code = %d, want %d", paymentErrorResponse.Code, http.StatusBadRequest)
	}
	if got := paymentErrorResponse.Header().Get("Content-Type"); got != "application/problem+json" {
		t.Fatalf("Content-Type = %q, want %q", got, "application/problem+json")
	}

	genericErrorResponse := httptest.NewRecorder()
	writePaymentError(genericErrorResponse, errors.New("verification failed"))
	if genericErrorResponse.Code != http.StatusPaymentRequired {
		t.Fatalf("genericErrorResponse.Code = %d, want %d", genericErrorResponse.Code, http.StatusPaymentRequired)
	}
}
