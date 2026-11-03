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
		failing.Store(true) // the commit's record write fails after payment settled
		return &SubscriptionResult{Subscription: SubscriptionRecord{LookupKey: "user:x", SubscriptionID: "sub_1"}}, nil
	})
	require.ErrorContains(t, err, "store unavailable")
	_, ok, err := backing.Get(ctx, store.activationKey("user:x"))
	require.NoError(t, err)
	assert.False(t, ok, "a failed commit releases its committing marker")

	failing.Store(false)
	status, _, _, err := store.activate(ctx, "challenge-2", "user:x", nil, func() (*SubscriptionResult, error) {
		return &SubscriptionResult{Subscription: SubscriptionRecord{LookupKey: "user:x", SubscriptionID: "sub_2"}}, nil
	})
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

	status, _, _, err := store.activate(ctx, "challenge-1", "user:x", nil, create("sub_1"))
	require.NoError(t, err)
	assert.Equal(t, activationInFlight, status, "a live committing marker holds the lock")

	now = now.Add(time.Minute)
	status, _, _, err = store.activate(ctx, "challenge-2", "user:x", nil, create("sub_2"))
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
