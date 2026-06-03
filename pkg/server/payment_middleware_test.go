package server

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/tempoxyz/mpp-go/pkg/mpp"
)

func TestWritePaymentError_FormatsProblemResponses(t *testing.T) {
	t.Parallel()

	paymentErrorResponse := httptest.NewRecorder()
	WritePaymentError(paymentErrorResponse, mpp.ErrBadRequest("bad request"))
	if !assert.Equalf(t, http.StatusBadRequest, paymentErrorResponse.Code,
		"paymentErrorResponse.Code = %d, want %d", paymentErrorResponse.Code, http.StatusBadRequest) {
		return
	}

	if got := paymentErrorResponse.Header().Get("Content-Type"); got != "application/problem+json" {
		assert.Failf(t, "", "Content-Type = %q, want %q", got, "application/problem+json")
		return
	}

	genericErrorResponse := httptest.NewRecorder()
	WritePaymentError(genericErrorResponse, errors.New("verification failed"))
	if !assert.Equalf(t, http.StatusPaymentRequired, genericErrorResponse.Code,
		"genericErrorResponse.Code = %d, want %d", genericErrorResponse.Code, http.StatusPaymentRequired) {
		return
	}

}
