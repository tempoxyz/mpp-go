package server

import (
	"context"

	"github.com/tempoxyz/mpp-go/internal/serverevents"
	"github.com/tempoxyz/mpp-go/pkg/mpp"
)

func init() {
	serverevents.PaymentFailed = func(ctx context.Context, payments []any, credential *mpp.Credential, err error) {
		handlers := make([]*Mpp, 0, len(payments))
		for _, payment := range payments {
			if m, ok := payment.(*Mpp); ok {
				handlers = append(handlers, m)
			}
		}
		paymentFailedOnce(ctx, handlers, credential, err)
	}
}

// Events are hooks a handler calls as payments progress, like mppx's
// onChallengeCreated, onPaymentSuccess and onPaymentFailed. Every hook is
// optional and runs synchronously on the request's goroutine, so keep it
// fast and do not block in it. Hooks observe the flow; they cannot change
// its outcome.
type Events struct {
	// ChallengeCreated is called for every Challenge returned to the client,
	// whether as the initial offer or as a retry offer after a failure.
	ChallengeCreated func(ctx context.Context, challenge *mpp.Challenge)
	// PaymentSucceeded is called after a Credential is verified and settled.
	PaymentSucceeded func(ctx context.Context, credential *mpp.Credential, receipt *mpp.Receipt)
	// PaymentFailed is called when a submitted Credential is rejected or
	// cannot be settled. credential is nil when it could not be parsed.
	PaymentFailed func(ctx context.Context, credential *mpp.Credential, err error)
}

// WithEvents sets the hooks the handler calls as payments progress.
func WithEvents(events Events) Option {
	return func(m *Mpp) {
		m.events = &events
	}
}

// emitCharge reports the outcome of one Charge call. authorization is the
// Payment credential the call was given, empty when there was none.
func (m *Mpp) emitCharge(ctx context.Context, authorization string, result *ChargeResult, err error) {
	if m.events == nil {
		return
	}
	if err != nil {
		if credential, submitted := submittedCredential(authorization); submitted {
			m.paymentFailed(ctx, credential, err)
		}
	}
	if result == nil {
		return
	}
	if hook := m.events.ChallengeCreated; hook != nil {
		for _, challenge := range result.offers() {
			hook(ctx, challenge)
		}
	}
	if err == nil && result.Challenge == nil && result.Credential != nil {
		if hook := m.events.PaymentSucceeded; hook != nil {
			hook(ctx, result.Credential, result.Receipt)
		}
	}
}

func (m *Mpp) paymentFailed(ctx context.Context, credential *mpp.Credential, err error) {
	if m.events == nil {
		return
	}
	if hook := m.events.PaymentFailed; hook != nil {
		hook(ctx, credential, err)
	}
}

// submittedCredential reports whether authorization carries a Payment
// credential, and returns it parsed, or nil when it cannot be parsed.
func submittedCredential(authorization string) (*mpp.Credential, bool) {
	header, err := mpp.FindPaymentAuthorizationStrict(authorization)
	if err != nil {
		return nil, true
	}
	if header == "" {
		return nil, false
	}
	credential, err := mpp.ParseCredential(header)
	if err != nil {
		return nil, true
	}
	return credential, true
}
