package chargeserver

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	miniredis "github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	temporpc "github.com/tempoxyz/tempo-go/pkg/client"

	"github.com/tempoxyz/mpp-go/pkg/internal/mpptest"
	"github.com/tempoxyz/mpp-go/pkg/mpp"
	"github.com/tempoxyz/mpp-go/pkg/tempo"
)

// faultyStore fails or refuses compare-and-swaps on chosen keys. It
// overrides CompareAndSwap, as an AtomicStore wrapper must.
type faultyStore struct {
	*tempo.MemoryStore
	fail   func(key string) bool
	refuse bool
}

func (s *faultyStore) CompareAndSwap(ctx context.Context, key string, old *string, next string) (bool, error) {
	if s.fail != nil && s.fail(key) {
		return false, errors.New("store unavailable")
	}
	if s.refuse {
		return false, nil
	}
	return s.MemoryStore.CompareAndSwap(ctx, key, old, next)
}

func TestSubscriptionStoreClearsCommittingMarkerOnError(t *testing.T) {
	ctx := context.Background()
	var failing atomic.Bool
	backing := &faultyStore{MemoryStore: tempo.NewMemoryStore()}
	backing.fail = func(key string) bool {
		return failing.Load() && strings.HasPrefix(key, defaultSubscriptionRecordPrefix)
	}
	store, err := NewSubscriptionStore(backing, SubscriptionStoreOptions{})
	require.NoError(t, err)

	_, _, _, err = store.activate(ctx, "challenge-1", "user:x", nil, func() (*SubscriptionResult, error) {
		failing.Store(true)
		return &SubscriptionResult{Subscription: SubscriptionRecord{LookupKey: "user:x", SubscriptionID: "sub_1"}}, nil
	}, nil)

	require.ErrorContains(t, err, "store unavailable")
	_, ok, err := backing.Get(ctx, store.activationKey("user:x"))
	require.NoError(t, err)
	assert.False(t, ok, "a failed commit releases its committing marker")

	failing.Store(false)
	status, _, _, err := store.activate(ctx, "challenge-2", "user:x", nil, func() (*SubscriptionResult, error) {
		return &SubscriptionResult{Subscription: SubscriptionRecord{LookupKey: "user:x", SubscriptionID: "sub_2"}}, nil
	}, nil)

	require.NoError(t, err)
	assert.Equal(t, activationActivated, status)
}

func TestSubscriptionStoreRecoversAbandonedCommittingMarker(t *testing.T) {
	ctx := context.Background()
	backing := tempo.NewMemoryStore()
	store, err := NewSubscriptionStore(backing, SubscriptionStoreOptions{ActivationTimeout: time.Minute})
	require.NoError(t, err)
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }

	// A replica crashed while committing: its marker stays behind.
	marker, err := json.Marshal(activationMarker{ChallengeID: "crashed", CommittingAt: store.timestamp(), StartedAt: store.timestamp()})
	require.NoError(t, err)
	require.NoError(t, backing.Put(ctx, store.activationKey("user:x"), string(marker)))
	create := func(id string) func() (*SubscriptionResult, error) {
		return func() (*SubscriptionResult, error) {
			return &SubscriptionResult{Subscription: SubscriptionRecord{LookupKey: "user:x", SubscriptionID: id}}, nil
		}
	}

	status, _, _, err := store.activate(ctx, "challenge-1", "user:x", nil, create("sub_1"), nil)
	require.NoError(t, err)
	assert.Equal(t, activationInFlight, status, "a live committing marker holds the lock")

	now = now.Add(time.Minute)
	status, _, _, err = store.activate(ctx, "challenge-2", "user:x", nil, create("sub_2"), nil)
	require.NoError(t, err)
	assert.Equal(t, activationActivated, status, "a committing marker past the timeout is stale")
}

func TestSubscriptionStoreBoundsCompareAndSwapRetries(t *testing.T) {
	store, err := NewSubscriptionStore(&faultyStore{MemoryStore: tempo.NewMemoryStore(), refuse: true}, SubscriptionStoreOptions{})
	require.NoError(t, err)
	_, err = store.GetOrCreateAccessKey(context.Background(), "user:x")
	assert.ErrorIs(t, err, errSubscriptionStoreContention)
	err = store.Put(context.Background(), SubscriptionRecord{LookupKey: "user:x", SubscriptionID: "sub_x"})
	assert.ErrorIs(t, err, errSubscriptionStoreContention)
}

func TestSubscriptionStoreWritesPersistOnTTLStore(t *testing.T) {
	ctx := context.Background()
	server := miniredis.RunT(t)
	store, err := NewSubscriptionStore(tempo.NewRedisStore(redis.NewClient(&redis.Options{Addr: server.Addr()}), time.Minute), SubscriptionStoreOptions{})
	require.NoError(t, err)
	require.NoError(t, store.Put(ctx, SubscriptionRecord{LookupKey: "user:x", SubscriptionID: "sub_x"}))
	key, err := store.GetOrCreateAccessKey(ctx, "user:x")
	require.NoError(t, err)
	_, err = store.CreateChallengeAccessKey(ctx)
	require.NoError(t, err)
	for _, k := range []string{store.recordKey("sub_x"), store.lookupKey("user:x"), store.accessKeyKey("user:x"), store.accessKeyAddressKey(key.AccessKeyAddress), store.sealingKeyKey()} {
		assert.Zero(t, server.TTL(k), "%s must not expire", k)
	}
	server.FastForward(time.Hour)
	current, err := store.GetByKey(ctx, "user:x")
	require.NoError(t, err)
	require.NotNil(t, current)
	assert.Equal(t, "sub_x", current.SubscriptionID)
}

// challengeAccessKey returns the access key of a fresh Challenge for user.
func (h *subscriptionHarness) challengeAccessKey(user string, params SubscribeParams) string {
	h.t.Helper()
	result, err := h.sub.SubscribeRequest(h.request(user, ""), params)
	require.NoError(h.t, err)
	require.True(h.t, result.IsChallenge())
	request, err := tempo.ParseSubscriptionRequest(result.Challenge.Request)
	require.NoError(h.t, err)
	return request.AccessKey.AccessKeyAddress
}

func TestSubscriptionAutomaticModeRotatesAccessKey(t *testing.T) {
	ctx := context.Background()
	rpc, _ := subscriptionChain(t, nil)
	h := newSubscriptionHarness(t, func(config *SubscriptionConfig) { config.RPC = rpc })

	first := h.challengeAccessKey("lee", SubscribeParams{})
	assert.Equal(t, first, h.challengeAccessKey("lee", SubscribeParams{}), "an unused key is reused")
	_, authorization := h.pay("lee", subscriptionPayerKey)
	activated, err := h.subscribe("lee", authorization)
	require.NoError(t, err)
	id := activated.Receipt.SubscriptionID

	// New terms need a new subscription, so a key the payer has not
	// registered: the keychain refuses to authorize one twice.
	second := h.challengeAccessKey("lee", SubscribeParams{Amount: "2"})
	assert.NotEqual(t, first, second)
	assert.Equal(t, second, h.challengeAccessKey("lee", SubscribeParams{Amount: "2"}), "rotation happens once")

	// Renewing the old subscription keeps its key reachable and does not
	// undo the rotation.
	h.advance(25 * time.Hour)
	renewed, err := h.sub.Renew(ctx, id)
	require.NoError(t, err)
	require.NotNil(t, renewed)
	current, err := h.store.GetAccessKey(ctx, "user:lee")
	require.NoError(t, err)
	assert.Equal(t, second, current.AccessKeyAddress)

	// A canceled subscription's key is rotated too.
	record, err := h.store.Get(ctx, id)
	require.NoError(t, err)
	record.AccessKey = &tempo.SubscriptionAccessKey{AccessKeyAddress: second, KeyType: tempo.SubscriptionKeyTypeSecp256k1}
	record.CanceledAt = formatTimestamp(h.clock())
	require.NoError(t, h.store.Put(ctx, *record))
	third := h.challengeAccessKey("lee", SubscribeParams{})
	assert.NotEqual(t, second, third)
	assert.NotEqual(t, first, third)
}

func TestSubscriptionRequireCredentialKeepsActiveAccessKey(t *testing.T) {
	rpc, _ := subscriptionChain(t, nil)
	h := newSubscriptionHarness(t, func(config *SubscriptionConfig) {
		config.RPC = rpc
		config.RequireCredential = true
	})
	_, authorization := h.pay("mo", subscriptionPayerKey)
	activated, err := h.subscribe("mo", authorization)
	require.NoError(t, err)
	record, err := h.store.GetByKey(context.Background(), "user:mo")
	require.NoError(t, err)
	assert.Equal(t, record.AccessKey.AccessKeyAddress, h.challengeAccessKey("mo", SubscribeParams{}), "a reusable subscription keeps its key")
	_, authorization = h.pay("mo", subscriptionPayerKey)
	reused, err := h.subscribe("mo", authorization)
	require.NoError(t, err)
	assert.Equal(t, activated.Receipt.SubscriptionID, reused.Receipt.SubscriptionID)
}

func TestSubscriptionCredentialRenewalInFlightIsConflict(t *testing.T) {
	var renewals []SubscriptionRenewParams
	var h *subscriptionHarness
	h = newSubscriptionHarness(t, func(config *SubscriptionConfig) {
		manualHooks(&h, &renewals)(config)
		config.RequireCredential = true
	})
	_, authorization := h.pay("nia", subscriptionPayerKey)
	_, err := h.subscribe("nia", authorization)
	require.NoError(t, err)
	h.advance(25 * time.Hour)
	record, err := h.store.Get(context.Background(), "sub_nia")
	require.NoError(t, err)
	period := int64(1)
	record.InFlightPeriod, record.InFlightAttempt, record.InFlightStartedAt = &period, "other", formatTimestamp(h.clock())
	require.NoError(t, h.store.Put(context.Background(), *record))

	_, authorization = h.pay("nia", subscriptionPayerKey)
	_, err = h.subscribe("nia", authorization)
	assert.ErrorIs(t, err, ErrSubscriptionRenewalInFlight)

	_, authorization = h.pay("nia", subscriptionPayerKey)
	recorder := httptest.NewRecorder()
	h.sub.Middleware(SubscribeParams{})(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("handler served during an in-flight renewal")
	})).ServeHTTP(recorder, h.request("nia", authorization))
	assert.Equal(t, http.StatusConflict, recorder.Code)
	assert.Equal(t, "1", recorder.Header().Get("Retry-After"))
	assert.Empty(t, renewals)
}

func TestSubscriptionForeignCredentialGetsChallenge(t *testing.T) {
	rpc, _ := subscriptionChain(t, nil)
	h := newSubscriptionHarness(t, func(config *SubscriptionConfig) { config.RPC = rpc })
	foreign := &mpp.Credential{
		Challenge: mpptest.NewChallenge(subscriptionSecret, subscriptionRealm, "stripe", tempo.IntentCharge, map[string]any{"amount": "1"}).ToEcho(),
		Payload:   map[string]any{"spt": "spt_test"},
	}
	result, _ := h.subscribe("oli", mpptest.FormatCredential(foreign))
	require.NotNil(t, result)
	require.True(t, result.IsChallenge(), "a foreign Credential gets a fresh Challenge")
	request, err := tempo.ParseSubscriptionRequest(result.Challenge.Request)
	require.NoError(t, err)
	stored, err := h.store.GetAccessKey(context.Background(), "user:oli")
	require.NoError(t, err)
	assert.Equal(t, stored.AccessKeyAddress, request.AccessKey.AccessKeyAddress)
}

// ambiguousChain is subscriptionChain whose transactions land only once
// landed is set and whose receipt reads fail while down is set.
func ambiguousChain(t *testing.T) (*mockRPC, *atomic.Bool, *atomic.Bool) {
	rpc, _ := subscriptionChain(t, nil)
	var landed, down atomic.Bool
	credit := rpc.onSend
	rpc.onSend = func(raw string) (string, map[string]any, error) {
		hash, receipt, err := credit(raw)
		if !landed.Load() {
			receipt = nil
		}
		return hash, receipt, err
	}
	rpc.onGetReceipt = func(hash string) (*temporpc.JSONRPCResponse, error) {
		if down.Load() {
			return nil, errors.New("rpc unavailable")
		}
		return &temporpc.JSONRPCResponse{Result: rpc.receipts[hash]}, nil
	}
	return rpc, &landed, &down
}

func TestSubscriptionActivationRecoversAmbiguousPayment(t *testing.T) {
	ctx := context.Background()
	rpc, landed, down := ambiguousChain(t)
	h := newSubscriptionHarness(t, func(config *SubscriptionConfig) { config.RPC = rpc })

	// The node takes the transaction, then the receipt read fails.
	down.Store(true)
	_, authorization := h.pay("pia", subscriptionPayerKey)
	_, err := h.subscribe("pia", authorization)
	require.ErrorIs(t, err, mpp.ErrorTypePaymentOutcomeUnknown)
	require.Len(t, rpc.sentRawTxs, 1)
	record, err := h.store.GetByKey(ctx, "user:pia")
	require.NoError(t, err)
	assert.Nil(t, record)

	// A retry resolves the same transaction instead of signing a new one.
	down.Store(false)
	landed.Store(true)
	_, authorization = h.pay("pia", subscriptionPayerKey)
	result, err := h.subscribe("pia", authorization)
	require.NoError(t, err)
	require.Len(t, rpc.sentRawTxs, 2)
	assert.Equal(t, rpc.sentRawTxs[0], rpc.sentRawTxs[1], "the pending transaction is broadcast again")
	assert.Equal(t, strings.ToLower(mustTxHash(rpc.sentRawTxs[0])), result.Receipt.Reference)

	// Once settled, the subscription is current and nothing is pending.
	record, err = h.store.GetByKey(ctx, "user:pia")
	require.NoError(t, err)
	require.NotNil(t, record)
	assert.Equal(t, result.Receipt.SubscriptionID, record.SubscriptionID)
	served, err := h.subscribe("pia", "")
	require.NoError(t, err)
	assert.False(t, served.IsChallenge())
	assert.Equal(t, result.Receipt.SubscriptionID, served.Receipt.SubscriptionID)
	assert.Len(t, rpc.sentRawTxs, 2, "an active subscription is not billed again")
}

func TestSubscriptionRenewalRecoversAmbiguousPayment(t *testing.T) {
	ctx := context.Background()
	rpc, landed, down := ambiguousChain(t)
	landed.Store(true)
	h := newSubscriptionHarness(t, func(config *SubscriptionConfig) { config.RPC = rpc })
	_, authorization := h.pay("quinn", subscriptionPayerKey)
	activated, err := h.subscribe("quinn", authorization)
	require.NoError(t, err)
	id := activated.Receipt.SubscriptionID
	require.Len(t, rpc.sentRawTxs, 1)

	// The renewal lands, but its receipt cannot be read.
	h.advance(25 * time.Hour)
	down.Store(true)
	_, err = h.sub.Renew(ctx, id)
	require.ErrorIs(t, err, mpp.ErrorTypePaymentOutcomeUnknown)
	require.Len(t, rpc.sentRawTxs, 2)
	record, err := h.store.Get(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, int64(0), record.LastChargedPeriod)
	require.NotNil(t, record.InFlightPayment, "the period stays locked with its payment")
	assert.True(t, record.InFlightPayment.Unknown)

	// The next attempt finds the included transaction and charges nothing new.
	down.Store(false)
	renewed, err := h.sub.Renew(ctx, id)
	require.NoError(t, err)
	require.NotNil(t, renewed)
	assert.Len(t, rpc.sentRawTxs, 2)
	assert.Equal(t, strings.ToLower(mustTxHash(rpc.sentRawTxs[1])), renewed.Receipt.Reference)
	record, err = h.store.Get(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, int64(1), record.LastChargedPeriod)
	assert.Nil(t, record.InFlightPayment)
	again, err := h.sub.Renew(ctx, id)
	require.NoError(t, err)
	assert.Nil(t, again, "the period is paid")
	assert.Len(t, rpc.sentRawTxs, 2)
}

func TestSubscriptionRenewalRetriesRejectedPendingPayment(t *testing.T) {
	ctx := context.Background()
	rpc, landed, down := ambiguousChain(t)
	landed.Store(true)
	h := newSubscriptionHarness(t, func(config *SubscriptionConfig) { config.RPC = rpc })
	_, authorization := h.pay("rey", subscriptionPayerKey)
	activated, err := h.subscribe("rey", authorization)
	require.NoError(t, err)
	id := activated.Receipt.SubscriptionID

	h.advance(25 * time.Hour)
	landed.Store(false)
	down.Store(true)
	_, err = h.sub.Renew(ctx, id)
	require.ErrorIs(t, err, mpp.ErrorTypePaymentOutcomeUnknown)

	// The node refuses the pending transaction outright and it is not on
	// chain, so the period is billed afresh.
	down.Store(false)
	landed.Store(true)
	credit := rpc.onSend
	rejected := false
	rpc.onSend = func(raw string) (string, map[string]any, error) {
		if !rejected {
			rejected = true
			return "", nil, &temporpc.JSONRPCError{Code: -32000, Message: "nonce too low"}
		}
		return credit(raw)
	}
	renewed, err := h.sub.Renew(ctx, id)
	require.NoError(t, err)
	require.NotNil(t, renewed)
	require.Len(t, rpc.sentRawTxs, 4)
	assert.Equal(t, strings.ToLower(mustTxHash(rpc.sentRawTxs[3])), renewed.Receipt.Reference)
}

func TestSubscriptionChallengeMustExpireBeforeSubscription(t *testing.T) {
	h := newSubscriptionHarness(t, func(config *SubscriptionConfig) {
		config.RPC = &mockRPC{receipts: map[string]map[string]any{}}
		config.SubscriptionExpires = time.Now().Add(2 * time.Minute).Truncate(time.Second)
	})
	result, err := h.subscribe("sam", "")
	assert.Nil(t, result)
	require.ErrorContains(t, err, "subscriptionExpires must be strictly later than challenge expires")
	var paymentErr *mpp.PaymentError
	assert.False(t, errors.As(err, &paymentErr), "a misconfigured route is a server error, not a 402")

	// An explicit Challenge expiry past the subscription's is refused too.
	h = newSubscriptionHarness(t, func(config *SubscriptionConfig) { config.RPC = &mockRPC{receipts: map[string]map[string]any{}} })
	_, err = h.sub.SubscribeRequest(h.request("sam", ""), SubscribeParams{Expires: "2027-01-01T00:00:00Z"})
	require.ErrorContains(t, err, "subscriptionExpires must be strictly later than challenge expires")
	result, err = h.sub.SubscribeRequest(h.request("sam", ""), SubscribeParams{Expires: "2026-12-31T23:59:59Z"})
	require.NoError(t, err)
	assert.True(t, result.IsChallenge())
}
