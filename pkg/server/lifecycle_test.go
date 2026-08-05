package server

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tempoxyz/mpp-go/pkg/mpp"
)

type splitTestIntent struct {
	validateCalls  int
	broadcastCalls int
	validateErr    error
	broadcastErr   error
}

func (i *splitTestIntent) Name() string { return "charge" }

func (i *splitTestIntent) Validate(
	_ context.Context,
	credential *mpp.Credential,
	request map[string]any,
) (*Validation, error) {
	i.validateCalls++
	if i.validateErr != nil {
		return nil, i.validateErr
	}
	return &Validation{
		Challenge:  credential.Challenge,
		Credential: credential,
		Details:    map[string]any{},
		Intent:     i.Name(),
		Method:     "tempo",
		Request:    request,
		Source:     credential.Source,
	}, nil
}

func (i *splitTestIntent) Broadcast(
	_ context.Context,
	_ *mpp.Credential,
	_ map[string]any,
) (*mpp.Receipt, error) {
	i.broadcastCalls++
	if i.broadcastErr != nil {
		return nil, i.broadcastErr
	}
	return mpp.Success("0xsplit", mpp.WithReceiptMethod("tempo")), nil
}

func newSplitTestServerIntent(t *testing.T, hooks *splitTestIntent) Intent {
	t.Helper()
	intent, err := NewIntent(hooks.Name(), IntentHooks{
		Validate:  hooks.Validate,
		Broadcast: hooks.Broadcast,
	})
	require.NoError(t, err)
	return intent
}

func splitCredential(secretKey string, request map[string]any) *mpp.Credential {
	challenge := mpp.NewChallenge(
		secretKey,
		"api.example.com",
		"tempo",
		"charge",
		request,
		mpp.WithExpires(mpp.Expires.Minutes(5)),
	)
	return challenge.NewCredential(map[string]any{"type": "transaction", "signature": "0x1234"})
}

func TestVerifyOrChallengeUsesSplitLifecycle(t *testing.T) {
	t.Parallel()
	intent := &splitTestIntent{}
	request := map[string]any{"amount": "1", "currency": "0x123"}
	credential := splitCredential("secret-key", request)

	result, err := VerifyOrChallenge(context.Background(), VerifyParams{
		Authorization: credential.ToAuthorization(),
		Intent:        newSplitTestServerIntent(t, intent),
		Request:       request,
		Realm:         "api.example.com",
		SecretKey:     "secret-key",
		Method:        "tempo",
		Expires:       credential.Challenge.Expires,
	})
	require.NoError(t, err)
	assert.Equal(t, "0xsplit", result.Receipt.Reference)
	assert.Equal(t, 1, intent.validateCalls)
	assert.Equal(t, 1, intent.broadcastCalls)
}

func TestMppCredentialLifecycle(t *testing.T) {
	t.Parallel()
	intent := &splitTestIntent{}
	method := chargeTestMethod{intents: map[string]Intent{"charge": newSplitTestServerIntent(t, intent)}}
	payment := New(method, "api.example.com", "secret-key")
	request := map[string]any{"amount": "1", "currency": "0x123"}
	credential := splitCredential("secret-key", request)

	validation, err := payment.ValidateCredential(context.Background(), credential)
	require.NoError(t, err)
	assert.Equal(t, credential, validation.Credential)
	assert.Equal(t, request, validation.Request)
	assert.Equal(t, 1, intent.validateCalls)
	assert.Zero(t, intent.broadcastCalls)

	receipt, err := payment.BroadcastCredential(context.Background(), credential)
	require.NoError(t, err)
	assert.Equal(t, "0xsplit", receipt.Reference)
	assert.Equal(t, 2, intent.validateCalls)
	assert.Equal(t, 1, intent.broadcastCalls)

	receipt, err = payment.VerifyCredential(context.Background(), credential)
	require.NoError(t, err)
	assert.Equal(t, "0xsplit", receipt.Reference)
	assert.Equal(t, 3, intent.validateCalls)
	assert.Equal(t, 2, intent.broadcastCalls)
}

func TestNewIntentSynthesizesVerify(t *testing.T) {
	t.Parallel()
	intent := &splitTestIntent{}
	credential := splitCredential("secret-key", map[string]any{"amount": "1"})
	configured := newSplitTestServerIntent(t, intent)

	receipt, err := configured.Verify(
		context.Background(),
		credential,
		map[string]any{"amount": "1"},
	)
	require.NoError(t, err)
	assert.Equal(t, "0xsplit", receipt.Reference)
	assert.Equal(t, 1, intent.validateCalls)
	assert.Equal(t, 1, intent.broadcastCalls)

	intent.validateErr = errors.New("invalid")
	_, err = configured.Verify(
		context.Background(),
		credential,
		map[string]any{"amount": "1"},
	)
	require.Error(t, err)
	assert.Equal(t, 2, intent.validateCalls)
	assert.Equal(t, 1, intent.broadcastCalls)
}

func TestNewIntentValidatesHookShape(t *testing.T) {
	t.Parallel()
	verify := func(context.Context, *mpp.Credential, map[string]any) (*mpp.Receipt, error) {
		return mpp.Success("0xlegacy"), nil
	}
	validate := func(context.Context, *mpp.Credential, map[string]any) (*Validation, error) {
		return &Validation{}, nil
	}
	broadcast := func(context.Context, *mpp.Credential, map[string]any) (*mpp.Receipt, error) {
		return mpp.Success("0xsplit"), nil
	}
	tests := []struct {
		name      string
		intent    string
		hooks     IntentHooks
		wantError bool
	}{
		{name: "verify", intent: "charge", hooks: IntentHooks{Verify: verify}},
		{name: "split", intent: "charge", hooks: IntentHooks{Validate: validate, Broadcast: broadcast}},
		{name: "missing name", hooks: IntentHooks{Verify: verify}, wantError: true},
		{name: "missing hooks", intent: "charge", wantError: true},
		{name: "validate only", intent: "charge", hooks: IntentHooks{Validate: validate}, wantError: true},
		{name: "broadcast only", intent: "charge", hooks: IntentHooks{Broadcast: broadcast}, wantError: true},
		{name: "mixed", intent: "charge", hooks: IntentHooks{Verify: verify, Validate: validate, Broadcast: broadcast}, wantError: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			intent, err := NewIntent(tt.intent, tt.hooks)
			if tt.wantError {
				require.Error(t, err)
				assert.Nil(t, intent)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.intent, intent.Name())
		})
	}
}

func TestMppValidateCredentialRequiresSplitHooks(t *testing.T) {
	t.Parallel()
	payment := New(
		chargeTestMethod{intents: map[string]Intent{"charge": verifyTestIntent{}}},
		"api.example.com",
		"secret-key",
	)
	credential := splitCredential("secret-key", map[string]any{"amount": "1"})

	_, err := payment.ValidateCredential(context.Background(), credential)
	var paymentErr *mpp.PaymentError
	require.ErrorAs(t, err, &paymentErr)
	assert.Equal(t, mpp.ErrorTypeVerificationFailed, paymentErr.Type)
	assert.Equal(t, map[string]any{"intent": "charge", "method": "tempo"}, paymentErr.Details)
}

func TestMppBroadcastCredentialSupportsLegacyIntent(t *testing.T) {
	t.Parallel()
	payment := New(
		chargeTestMethod{intents: map[string]Intent{"charge": verifyTestIntent{}}},
		"api.example.com",
		"secret-key",
	)
	credential := splitCredential("secret-key", map[string]any{"amount": "1"})

	receipt, err := payment.BroadcastCredential(context.Background(), credential)
	require.NoError(t, err)
	assert.Equal(t, "0xreceipt", receipt.Reference)
}

func TestMppBroadcastCredentialStopsAfterValidationFailure(t *testing.T) {
	t.Parallel()
	intent := &splitTestIntent{validateErr: errors.New("invalid")}
	payment := New(
		chargeTestMethod{intents: map[string]Intent{"charge": newSplitTestServerIntent(t, intent)}},
		"api.example.com",
		"secret-key",
	)
	credential := splitCredential("secret-key", map[string]any{"amount": "1"})

	_, err := payment.BroadcastCredential(context.Background(), credential)
	require.Error(t, err)
	assert.Equal(t, 1, intent.validateCalls)
	assert.Zero(t, intent.broadcastCalls)
}

func TestMppCredentialLifecycleRejectsForeignChallenge(t *testing.T) {
	t.Parallel()
	intent := &splitTestIntent{}
	payment := New(
		chargeTestMethod{intents: map[string]Intent{"charge": newSplitTestServerIntent(t, intent)}},
		"api.example.com",
		"secret-key",
	)
	credential := splitCredential("foreign-secret", map[string]any{"amount": "1"})

	_, err := payment.ValidateCredential(context.Background(), credential)
	var paymentErr *mpp.PaymentError
	require.ErrorAs(t, err, &paymentErr)
	assert.Equal(t, mpp.ErrorTypeInvalidChallenge, paymentErr.Type)
	assert.Zero(t, intent.validateCalls)
}
