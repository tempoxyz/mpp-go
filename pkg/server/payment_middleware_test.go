package server

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/tempoxyz/mpp-go/pkg/mpp"
)

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
