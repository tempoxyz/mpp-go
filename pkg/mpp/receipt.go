package mpp

import "time"

// Receipt represents a server-issued payment receipt sent via the
// Payment-Receipt header.
type Receipt struct {
	Status         string    `json:"status"`
	Timestamp      time.Time `json:"timestamp"`
	Reference      string    `json:"reference"`
	Method         string    `json:"method,omitempty"`
	ExternalID     string    `json:"externalId,omitempty"`
	SubscriptionID string    `json:"subscriptionId,omitempty"`
	// FundingCurrency is the currency actually debited to fund the payment,
	// when the payment method knows it.
	FundingCurrency string `json:"fundingCurrency,omitempty"`
	// Extra is a nested "extra" object, which no other MPP SDK emits.
	//
	// Deprecated: Use Extensions instead, which holds method-defined fields
	// at the top level of the receipt as mppx and mpp-rs do.
	Extra map[string]any `json:"extra,omitempty"`

	// Extensions holds method-defined top-level receipt fields beyond the
	// base set. draft-ietf-httpauth-payment §5.3 states "Payment method
	// specifications MAY define additional fields for receipts", and the
	// canonical mppx schema preserves such unknown fields across
	// parse/serialize round trips rather than discarding them. These are
	// flattened to the top level of the Payment-Receipt JSON (not nested
	// under "extra"). The tag is "-" because serialization is handled
	// explicitly by FormatReceiptWithError, not via struct marshaling. Set
	// it with WithExtensions.
	Extensions map[string]any `json:"-"`
}

// ReceiptOption configures optional fields when creating a Receipt.
type ReceiptOption func(*Receipt)

// WithExternalID sets the external transaction ID on a Receipt.
func WithExternalID(id string) ReceiptOption {
	return func(r *Receipt) { r.ExternalID = id }
}

// WithSubscriptionID sets the server-issued subscription ID on a Receipt.
func WithSubscriptionID(id string) ReceiptOption {
	return func(r *Receipt) { r.SubscriptionID = id }
}

// WithFundingCurrency sets the currency actually debited to fund the payment.
func WithFundingCurrency(currency string) ReceiptOption {
	return func(r *Receipt) { r.FundingCurrency = currency }
}

// WithExtensions sets method-defined top-level fields on a Receipt. Keys of
// base receipt fields (status, timestamp, reference, method, externalId,
// subscriptionId) are ignored when formatting.
func WithExtensions(extensions map[string]any) ReceiptOption {
	return func(r *Receipt) { r.Extensions = extensions }
}

// Success creates a new successful Receipt with the given method, reference,
// and options.
func Success(method, reference string, opts ...ReceiptOption) *Receipt {
	r := &Receipt{
		Status:    "success",
		Timestamp: time.Now().UTC(),
		Reference: reference,
		Method:    method,
	}
	for _, o := range opts {
		o(r)
	}
	return r
}
