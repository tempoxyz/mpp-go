package chargeserver

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tempoxyz/mpp-go/pkg/mpp"
	"github.com/tempoxyz/mpp-go/pkg/tempo"
)

// failingExpiringStore is a store whose PutIfAbsentUntil fails, as Redis
// before 6.2 does for SET NX EXAT.
type failingExpiringStore struct{ *tempo.MemoryStore }

func (failingExpiringStore) PutIfAbsentUntil(context.Context, string, string, time.Time) (bool, error) {
	return false, errors.New("ERR syntax error")
}

func settledActivation(id, lookupKey, anchor, reference string) func() (*SubscriptionResult, error) {
	return func() (*SubscriptionResult, error) {
		record := SubscriptionRecord{
			BillingAnchor: anchor, LookupKey: lookupKey, Reference: reference,
			SubscriptionExpires: "2027-01-01T00:00:00.000Z", SubscriptionID: id, Timestamp: anchor,
		}
		return &SubscriptionResult{Receipt: subscriptionReceipt(record), Subscription: record}, nil
	}
}

func TestSubscriptionStoreClaimsPersistByDefault(t *testing.T) {
	ctx := context.Background()
	claimUntil := &activationHooks{claimUntil: time.Now().Add(10 * time.Minute)}

	store, err := NewSubscriptionStore(failingExpiringStore{tempo.NewMemoryStore()}, SubscriptionStoreOptions{})
	require.NoError(t, err)
	status, _, _, err := store.activate(ctx, "challenge-1", "user:x", nil, settledActivation("sub_1", "user:x", "2026-11-03T12:00:00.000Z", testTxHash(1)), claimUntil)
	require.NoError(t, err, "a store without SET NX EXAT still activates")
	assert.Equal(t, activationActivated, status)

	// Expiring claims are opt-in, as the charge intent's ExpireReplayKeys.
	store, err = NewSubscriptionStore(failingExpiringStore{tempo.NewMemoryStore()}, SubscriptionStoreOptions{ExpireCredentialClaims: true})
	require.NoError(t, err)
	_, _, _, err = store.activate(ctx, "challenge-1", "user:x", nil, settledActivation("sub_1", "user:x", "2026-11-03T12:00:00.000Z", testTxHash(1)), claimUntil)
	require.ErrorContains(t, err, "syntax error")

	_, err = NewSubscriptionStore(atomicOnlyStore{tempo.NewMemoryStore()}, SubscriptionStoreOptions{ExpireCredentialClaims: true})
	require.ErrorContains(t, err, "ExpireCredentialClaims requires a store that implements tempo.ExpiringStore")
}

// atomicOnlyStore hides MemoryStore's PutIfAbsentUntil.
type atomicOnlyStore struct{ inner *tempo.MemoryStore }

func (s atomicOnlyStore) Get(ctx context.Context, key string) (string, bool, error) {
	return s.inner.Get(ctx, key)
}
func (s atomicOnlyStore) Put(ctx context.Context, key, value string) error {
	return s.inner.Put(ctx, key, value)
}
func (s atomicOnlyStore) PutIfAbsent(ctx context.Context, key, value string) (bool, error) {
	return s.inner.PutIfAbsent(ctx, key, value)
}
func (s atomicOnlyStore) Delete(ctx context.Context, key string) error {
	return s.inner.Delete(ctx, key)
}
func (s atomicOnlyStore) CompareAndSwap(ctx context.Context, key string, old *string, value string) (bool, error) {
	return s.inner.CompareAndSwap(ctx, key, old, value)
}
func (s atomicOnlyStore) CompareAndDelete(ctx context.Context, key, old string) (bool, error) {
	return s.inner.CompareAndDelete(ctx, key, old)
}

func TestSubscriptionStoreReactivationReplacesEndedRecord(t *testing.T) {
	ctx := context.Background()
	store, err := NewSubscriptionStore(tempo.NewMemoryStore(), SubscriptionStoreOptions{})
	require.NoError(t, err)
	_, _, _, err = store.activate(ctx, "challenge-1", "user:x", nil, settledActivation("sub_x", "user:x", "2026-11-03T12:00:00.000Z", testTxHash(1)), nil)
	require.NoError(t, err)
	require.NoError(t, store.cancelRecord(ctx, "sub_x"))

	// A custom Activate reusing the deterministic ID settles a new payment.
	status, result, _, err := store.activate(ctx, "challenge-2", "user:x", nil, settledActivation("sub_x", "user:x", "2026-11-04T12:00:00.000Z", testTxHash(2)), nil)
	require.NoError(t, err)
	assert.Equal(t, activationActivated, status)
	require.NotNil(t, result)
	record, err := store.GetByKey(ctx, "user:x")
	require.NoError(t, err)
	require.NotNil(t, record)
	assert.Equal(t, testTxHash(2), record.Reference, "the new activation's record is written")
	assert.Empty(t, record.CanceledAt)
}

func TestSubscriptionStoreReactivationKeepsLiveRecordOfAnotherKey(t *testing.T) {
	ctx := context.Background()
	store, err := NewSubscriptionStore(tempo.NewMemoryStore(), SubscriptionStoreOptions{})
	require.NoError(t, err)
	_, _, _, err = store.activate(ctx, "challenge-1", "user:x", nil, settledActivation("sub_shared", "user:x", "2026-11-03T12:00:00.000Z", testTxHash(1)), nil)
	require.NoError(t, err)

	_, _, _, err = store.activate(ctx, "challenge-2", "user:y", nil, settledActivation("sub_shared", "user:y", "2026-11-04T12:00:00.000Z", testTxHash(2)), nil)
	require.ErrorContains(t, err, "already holds another activation")
	record, err := store.Get(ctx, "sub_shared")
	require.NoError(t, err)
	assert.Equal(t, "user:x", record.LookupKey, "a live record is never overwritten")
	marker, _, err := getJSON[activationMarker](ctx, store.store, store.activationKey("user:y"))
	require.NoError(t, err)
	require.NotNil(t, marker)
	require.NotNil(t, marker.Settled, "the settled activation is kept for recovery")
	assert.Equal(t, testTxHash(2), marker.Settled.Reference)
	assert.True(t, marker.Retry)
}

func TestSubscriptionStoreRecoveredCommitKeepsRenewedRecord(t *testing.T) {
	ctx := context.Background()
	store, err := NewSubscriptionStore(tempo.NewMemoryStore(), SubscriptionStoreOptions{})
	require.NoError(t, err)
	renewed := SubscriptionRecord{
		BillingAnchor: "2026-11-03T12:00:00.000Z", LastChargedPeriod: 1, LookupKey: "user:x", Reference: testTxHash(9),
		SubscriptionExpires: "2027-01-01T00:00:00.000Z", SubscriptionID: "sub_x", Timestamp: "2026-11-04T12:00:00.000Z",
	}
	require.NoError(t, store.Put(ctx, renewed))
	status, _, _, err := store.activate(ctx, "challenge-1", "user:x", nil, settledActivation("sub_x", "user:x", "2026-11-03T12:00:00.000Z", testTxHash(1)), nil)
	require.NoError(t, err)
	assert.Equal(t, activationActivated, status)
	record, err := store.Get(ctx, "sub_x")
	require.NoError(t, err)
	assert.Equal(t, int64(1), record.LastChargedPeriod, "renewals since the first commit are kept")
	assert.Equal(t, testTxHash(9), record.Reference)
}

func TestSubscriptionDeterministicIDReactivates(t *testing.T) {
	var renewals []SubscriptionRenewParams
	var h *subscriptionHarness
	h = newSubscriptionHarness(t, func(config *SubscriptionConfig) { manualHooks(&h, &renewals)(config) })
	ctx := context.Background()
	_, authorization := h.pay("alice", subscriptionPayerKey)
	_, err := h.subscribe("alice", authorization)
	require.NoError(t, err)
	require.NoError(t, h.store.cancelRecord(ctx, "sub_alice"))

	h.advance(time.Hour)
	_, authorization = h.pay("alice", subscriptionPayerKey)
	result, err := h.subscribe("alice", authorization)
	require.NoError(t, err)
	assert.Equal(t, "sub_alice", result.Receipt.SubscriptionID)
	record, err := h.store.GetByKey(ctx, "user:alice")
	require.NoError(t, err)
	require.NotNil(t, record)
	assert.Empty(t, record.CanceledAt, "the re-activation's record replaces the canceled one")
	assert.Equal(t, formatTimestamp(h.clock()), record.BillingAnchor)

	// It is reused, so the payer is not asked to pay again.
	reused, err := h.subscribe("alice", "")
	require.NoError(t, err)
	assert.False(t, reused.IsChallenge())
}

func TestSubscriptionStoreCommitsHeldPendingPayments(t *testing.T) {
	ctx := context.Background()
	store, err := NewSubscriptionStore(tempo.NewMemoryStore(), SubscriptionStoreOptions{})
	require.NoError(t, err)
	heldErr := func(hash string) error {
		return &subscriptionHeldError{hash: hash, err: mpp.ErrVerificationFailed("held").WithCause(errSubscriptionPaymentHeld)}
	}
	pending := SubscriptionRecord{
		BillingAnchor: "2026-11-03T12:00:00.000Z", LookupKey: "user:x", SubscriptionExpires: "2027-01-01T00:00:00.000Z",
		SubscriptionID: "sub_x", Timestamp: "2026-11-03T12:00:00.000Z",
	}
	_, err = store.startActivationForTest(ctx, "user:x", "challenge-1", pending)
	require.NoError(t, err)

	var recovered []SubscriptionResult
	status, _, existing, err := store.activate(ctx, "challenge-2", "user:x", func(SubscriptionRecord) bool { return true }, func() (*SubscriptionResult, error) {
		t.Fatal("a held payment is never paid again")
		return nil, nil
	}, &activationHooks{
		resolve: func(SubscriptionPendingPayment, SubscriptionRecord) (string, error) {
			return "", heldErr(testTxHash(5))
		},
		recovered: func(result SubscriptionResult) { recovered = append(recovered, result) },
	})
	require.NoError(t, err)
	assert.Equal(t, activationExisting, status)
	require.Len(t, recovered, 1)
	assert.True(t, recovered[0].Subscription.Held)
	assert.Equal(t, testTxHash(5), existing.Reference)
	assert.True(t, existing.Held)

	// A renewal whose pending payment was held commits its period.
	period := int64(1)
	locked := *existing
	locked.InFlightPeriod, locked.InFlightAttempt, locked.InFlightReference = &period, "old", "renewal:sub_x:1"
	locked.InFlightPayment = &SubscriptionPendingPayment{Hash: testTxHash(6), Unknown: true}
	locked.InFlightStartedAt = store.timestamp()
	require.NoError(t, store.Put(ctx, locked))
	renewStatus, result, _, err := store.renew(ctx, "sub_x", 1, "renewal:sub_x:1", func(SubscriptionRecord) (*SubscriptionResult, error) {
		t.Fatal("a held period is never billed again")
		return nil, nil
	}, &renewalHooks{resolve: func(SubscriptionPendingPayment, SubscriptionRecord) (string, error) {
		return "", heldErr(testTxHash(6))
	}})
	require.NoError(t, err)
	assert.Equal(t, renewalRenewed, renewStatus)
	assert.True(t, result.Subscription.Held)
	assert.Equal(t, testTxHash(6), result.Subscription.Reference)
	assert.Equal(t, int64(1), result.Subscription.LastChargedPeriod)
	assert.Nil(t, result.Subscription.InFlightPayment)
}

// startActivationForTest leaves challengeID's activation lock with a pending
// payment whose outcome is unknown, as an attempt that crashed would.
func (s *SubscriptionStore) startActivationForTest(ctx context.Context, lookupKey, challengeID string, record SubscriptionRecord) (bool, error) {
	if _, _, err := s.startActivation(ctx, lookupKey, challengeID); err != nil {
		return false, err
	}
	if err := s.recordActivationPayment(ctx, lookupKey, challengeID, SubscriptionPendingPayment{Hash: testTxHash(5)}, record); err != nil {
		return false, err
	}
	return true, s.markActivationUnknown(ctx, lookupKey, challengeID)
}
