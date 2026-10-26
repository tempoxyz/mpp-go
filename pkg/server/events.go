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
// its outcome: a hook that panics is recovered and the panic is ignored, so
// a request whose payment settled is still served.
type Events struct {
	// ChallengeCreated is called for every Challenge returned to the client,
	// whether as the initial offer or as a retry offer after a failure. It
	// is called only once the whole set of Challenges for the response is
	// issued and can be serialized, never for an offer that is not returned.
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
// Payment credential the call was given, empty when there was none. With
// challenges false it leaves ChallengeCreated to the caller, a composed flow
// that reports the full set of Challenges it returns.
func (m *Mpp) emitCharge(ctx context.Context, authorization string, result *ChargeResult, err error, challenges bool) {
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
	if challenges && challengesDelivered(err, result.offers()) {
		m.challengesCreated(ctx, result.offers())
	}
	if err == nil && result.Challenge == nil && result.Credential != nil {
		if hook := m.events.PaymentSucceeded; hook != nil {
			safeCall(func() { hook(ctx, result.Credential, result.Receipt) })
		}
	}
}

// challengesCreated reports challenges to the ChallengeCreated hook.
func (m *Mpp) challengesCreated(ctx context.Context, challenges []*mpp.Challenge) {
	if m == nil || m.events == nil || m.events.ChallengeCreated == nil {
		return
	}
	hook := m.events.ChallengeCreated
	for _, challenge := range challenges {
		if challenge != nil {
			safeCall(func() { hook(ctx, challenge) })
		}
	}
}

// challengesDelivered reports whether a payment response for err returns
// challenges to the client, as WritePaymentResponse writes it: some are
// offered, the outcome is known and every one serializes.
func challengesDelivered(err error, challenges []*mpp.Challenge) bool {
	challenges = nonNilChallenges(challenges)
	if len(challenges) == 0 || IsPaymentOutcomeUnknown(err) {
		return false
	}
	_, headerErr := authenticateHeaders(challenges)
	return headerErr == nil
}

func (m *Mpp) paymentFailed(ctx context.Context, credential *mpp.Credential, err error) {
	if m.events == nil {
		return
	}
	if hook := m.events.PaymentFailed; hook != nil {
		safeCall(func() { hook(ctx, credential, err) })
	}
}

// safeCall runs a user hook, recovering and ignoring a panic so that a hook
// cannot abort a request, such as one whose payment already settled.
func safeCall(hook func()) {
	defer func() { _ = recover() }()
	hook()
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
