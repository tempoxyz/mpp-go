package mpp

import "time"

// Receipt represents a server-issued payment receipt sent via the
// Payment-Receipt header.
type Receipt struct {
	Status     string         `json:"status"`
	Timestamp  time.Time      `json:"timestamp"`
	Reference  string         `json:"reference"`
	Method     string         `json:"method,omitempty"`
	ExternalID string         `json:"externalId,omitempty"`
	Extra      map[string]any `json:"extra,omitempty"`
}

// ReceiptOption configures optional fields when creating a Receipt.
type ReceiptOption func(*Receipt)

// WithReceiptMethod sets the payment method on a Receipt.
func WithReceiptMethod(method string) ReceiptOption {
	return func(r *Receipt) { r.Method = method }
}

// WithExternalID sets the external transaction ID on a Receipt.
func WithExternalID(id string) ReceiptOption {
	return func(r *Receipt) { r.ExternalID = id }
}

// WithExtra sets extra metadata on a Receipt.
func WithExtra(extra map[string]any) ReceiptOption {
	return func(r *Receipt) { r.Extra = extra }
}

// Success creates a new successful Receipt with the given reference and options.
func Success(reference string, opts ...ReceiptOption) *Receipt {
	r := &Receipt{
		Status:    "success",
		Timestamp: time.Now().UTC(),
		Reference: reference,
	}
	for _, o := range opts {
		o(r)
	}
	return r
}

// ParseReceipt parses a receipt header value into a Receipt.
func ParseReceipt(header string) (*Receipt, error) {
	return ParsePaymentReceipt(header)
}

// FormatReceipt formats a Receipt as a receipt header value.
func FormatReceipt(r *Receipt) string {
	return FormatPaymentReceipt(r)
}

// FromPaymentReceipt parses a Payment-Receipt header value into a Receipt.
func FromPaymentReceipt(header string) (*Receipt, error) {
	return ParseReceipt(header)
}

// ToPaymentReceipt formats this Receipt as a Payment-Receipt header value.
func (r *Receipt) ToPaymentReceipt() string {
	return FormatReceipt(r)
}
