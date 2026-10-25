package mpp

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestPaymentActionRequiredAndOutcomeUnknownErrors(t *testing.T) {
	action := ErrPaymentActionRequired("requires_action")
	assert.Equal(t, ErrorType("https://paymentauth.org/problems/payment-action-required"), action.Type)
	assert.Equal(t, "Payment Action Required", action.Title)
	assert.Equal(t, http.StatusPaymentRequired, action.Status)
	assert.Equal(t, "Payment requires action: requires_action.", action.Detail)
	assert.Equal(t, "Payment requires action.", ErrPaymentActionRequired("").Detail)

	unknown := ErrPaymentOutcomeUnknown("ch_1")
	assert.Equal(t, ErrorType("https://paymentauth.org/problems/payment-outcome-unknown"), unknown.Type)
	assert.Equal(t, "Payment Outcome Unknown", unknown.Title)
	assert.Equal(t, http.StatusPaymentRequired, unknown.Status)
	assert.Equal(t, "Payment outcome is unknown after sending a credential for challenge \"ch_1\". Do not blindly retry.", unknown.Detail)
}
