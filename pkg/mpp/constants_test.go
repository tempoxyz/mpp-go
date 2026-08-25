package mpp

import "testing"

func TestProtocolConstants(t *testing.T) {
	tests := []struct {
		name string
		got  string
		want string
	}{
		{name: "accept payment", got: HeaderAcceptPayment, want: "Accept-Payment"},
		{name: "authorization", got: HeaderAuthorization, want: "Authorization"},
		{name: "payment authorization", got: HeaderPaymentAuthorization, want: "Payment-Authorization"},
		{name: "payment receipt", got: HeaderPaymentReceipt, want: "Payment-Receipt"},
		{name: "payment session", got: HeaderPaymentSession, want: "Payment-Session"},
		{name: "payment session snapshot", got: HeaderPaymentSessionSnapshot, want: "Payment-Session-Snapshot"},
		{name: "www authenticate", got: HeaderWWWAuthenticate, want: "WWW-Authenticate"},
		{name: "payment scheme", got: SchemePayment, want: "Payment"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.got != tt.want {
				t.Fatalf("got %q, want %q", tt.got, tt.want)
			}
		})
	}
}
