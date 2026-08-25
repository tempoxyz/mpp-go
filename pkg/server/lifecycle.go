package server

import (
	"context"
	"fmt"
	"time"

	"github.com/tempoxyz/mpp-go/pkg/mpp"
)

// validateCredential dispatches the advisory validation phase and rejects
// legacy intents rather than invoking their potentially mutating Verify hook.
func validateCredential(
	ctx context.Context,
	intent Intent,
	credential *mpp.Credential,
	request map[string]any,
	method string,
) (*Validation, error) {
	validating, ok := intent.(ValidatingIntent)
	if !ok {
		err := mpp.ErrVerificationFailed(fmt.Sprintf(
			"%s/%s does not support non-mutating credential validation",
			method,
			intent.Name(),
		))
		err.Details = map[string]any{"intent": intent.Name(), "method": method}
		return nil, err
	}
	return validating.Validate(ctx, credential, request)
}

// broadcastCredential dispatches the split lifecycle when available and keeps
// legacy intents on their combined Verify path.
func broadcastCredential(
	ctx context.Context,
	intent Intent,
	credential *mpp.Credential,
	request map[string]any,
) (*mpp.Receipt, error) {
	if broadcasting, ok := intent.(BroadcastingIntent); ok {
		return broadcasting.Broadcast(ctx, credential, request)
	}
	return intent.Verify(ctx, credential, request)
}

// prepareCredential authenticates the echoed stateless challenge, enforces its
// server bindings and expiry, and resolves the intent request for dispatch.
func (m *Mpp) prepareCredential(credential *mpp.Credential) (Intent, map[string]any, error) {
	if credential == nil {
		return nil, nil, mpp.ErrMalformedCredential("credential is required")
	}
	echoedRequest, err := echoedRequestMap(credential)
	if err != nil {
		return nil, nil, mpp.ErrMalformedCredential(fmt.Sprintf("invalid echoed request: %v", err))
	}
	echoed := &credential.Challenge
	echoedChallenge := mpp.NewChallenge(
		m.secretKey,
		echoed.Realm,
		echoed.Method,
		echoed.Intent,
		echoedRequest,
		echoedChallengeOpts(credential)...,
	)
	if !mpp.ConstantTimeEqual(echoed.ID, echoedChallenge.ID) {
		return nil, nil, mpp.ErrInvalidChallenge(echoed.ID, "challenge was not issued by this server")
	}
	if echoed.Realm != m.realm {
		return nil, nil, mpp.ErrInvalidChallenge(echoed.ID, "realm mismatch")
	}
	if echoed.Method != m.method.Name() {
		return nil, nil, mpp.ErrInvalidChallenge(echoed.ID, "method mismatch")
	}
	intent, ok := m.method.Intents()[echoed.Intent]
	if !ok || intent.Name() != echoed.Intent {
		return nil, nil, mpp.ErrInvalidChallenge(echoed.ID, "intent mismatch")
	}
	if echoed.Expires == "" {
		return nil, nil, mpp.ErrInvalidChallenge(echoed.ID, "missing required expires")
	}
	expires, err := parseChallengeExpiry(echoed.Expires)
	if err != nil {
		return nil, nil, mpp.ErrInvalidChallenge(echoed.ID, "invalid expires format")
	}
	if time.Now().UTC().After(expires) {
		return nil, nil, mpp.ErrPaymentExpired(echoed.Expires)
	}
	return intent, echoedRequest, nil
}

// parseChallengeExpiry accepts the protocol's RFC 3339 timestamp format,
// including fractional seconds supported by time.Parse.
func parseChallengeExpiry(value string) (time.Time, error) {
	return time.Parse(time.RFC3339, value)
}
