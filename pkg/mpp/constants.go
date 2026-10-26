package mpp

// MPP HTTP header names.
const (
	HeaderAcceptPayment          = "Accept-Payment"
	HeaderAuthorization          = "Authorization"
	HeaderPaymentAuthorization   = "Payment-Authorization"
	HeaderPaymentReceipt         = "Payment-Receipt"
	HeaderPaymentSession         = "Payment-Session"
	HeaderPaymentSessionSnapshot = "Payment-Session-Snapshot"
	HeaderWWWAuthenticate        = "WWW-Authenticate"
)

// MPP payment intent names.
const (
	IntentCharge  = "charge"
	IntentSession = "session"
)

// MPP HTTP authentication schemes.
const (
	SchemePayment = "Payment"
)
