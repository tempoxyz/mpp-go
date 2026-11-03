package chargeserver

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	miniredis "github.com/alicebob/miniredis/v2"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	temporpc "github.com/tempoxyz/tempo-go/pkg/client"
	tempotx "github.com/tempoxyz/tempo-go/pkg/transaction"

	"github.com/tempoxyz/mpp-go/pkg/internal/mpptest"
	"github.com/tempoxyz/mpp-go/pkg/mpp"
	"github.com/tempoxyz/mpp-go/pkg/tempo"
)

// hookedStore runs before ahead of every compare-and-swap, to interleave a
// concurrent write with a commit.
type hookedStore struct {
	*tempo.MemoryStore
	before func(key string)
}

func (s *hookedStore) CompareAndSwap(ctx context.Context, key string, old *string, next string) (bool, error) {
	if s.before != nil {
		s.before(key)
	}
	return s.MemoryStore.CompareAndSwap(ctx, key, old, next)
}

func mustNotCreate(t *testing.T) func() (*SubscriptionResult, error) {
	return func() (*SubscriptionResult, error) {
		t.Fatal("a paid activation must not be paid again")
		return nil, nil
	}
}

func TestSubscriptionStoreStaleActivationKeepsRecoveredRecord(t *testing.T) {
	ctx := context.Background()
	store, err := NewSubscriptionStore(tempo.NewMemoryStore(), SubscriptionStoreOptions{ActivationTimeout: time.Minute})
	require.NoError(t, err)
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	record := SubscriptionRecord{LookupKey: "user:x", SubscriptionID: "sub_1", BillingAnchor: store.timestamp(), Reference: testTxHash(1)}
	var recovered []SubscriptionResult
	hooks := &activationHooks{
		resolve:   func(SubscriptionPendingPayment, SubscriptionRecord) (string, error) { return testTxHash(1), nil },
		recovered: func(result SubscriptionResult) { recovered = append(recovered, result) },
	}
	reusable := func(r SubscriptionRecord) bool { return r.SubscriptionID == "sub_1" }

	status, _, _, err := store.activate(ctx, "challenge-1", "user:x", nil, func() (*SubscriptionResult, error) {
		// The payment is recorded, then this replica stalls past the lock
		// timeout while another attempt finds it landed and commits it.
		require.NoError(t, store.recordActivationPayment(ctx, "user:x", "challenge-1", SubscriptionPendingPayment{Hash: testTxHash(1)}, record))
		now = now.Add(time.Minute)
		status, _, _, err := store.activate(ctx, "challenge-2", "user:x", reusable, mustNotCreate(t), hooks)
		require.NoError(t, err)
		require.Equal(t, activationExisting, status)
		// The subscription renews before the stalled replica resumes.
		renewed, err := store.Get(ctx, "sub_1")
		require.NoError(t, err)
		renewed.LastChargedPeriod = 2
		require.NoError(t, store.putJSON(ctx, store.recordKey("sub_1"), renewed))
		return &SubscriptionResult{Receipt: subscriptionReceipt(record), Subscription: record}, nil
	}, nil)
	require.NoError(t, err)
	assert.Equal(t, activationClaimMismatch, status)

	current, err := store.GetByKey(ctx, "user:x")
	require.NoError(t, err)
	require.NotNil(t, current)
	assert.Equal(t, "sub_1", current.SubscriptionID)
	assert.Empty(t, current.CanceledAt, "the stale replica must not cancel the recovered subscription")
	assert.Equal(t, int64(2), current.LastChargedPeriod, "nor undo its renewal")
	require.Len(t, recovered, 1, "the recovered activation is reported once")
	assert.Equal(t, "sub_1", recovered[0].Subscription.SubscriptionID)
}

func TestSubscriptionStoreReportsRecoveredRenewal(t *testing.T) {
	ctx := context.Background()
	store, err := NewSubscriptionStore(tempo.NewMemoryStore(), SubscriptionStoreOptions{})
	require.NoError(t, err)
	period := int64(1)
	require.NoError(t, store.Put(ctx, SubscriptionRecord{
		LookupKey: "user:x", SubscriptionID: "sub_1", InFlightPeriod: &period, InFlightAttempt: "crashed",
		InFlightStartedAt: store.timestamp(), InFlightPayment: &SubscriptionPendingPayment{Hash: testTxHash(1), Unknown: true},
	}))
	var recovered []int64
	status, result, _, err := store.renew(ctx, "sub_1", 2, "renewal:sub_1:2", func(started SubscriptionRecord) (*SubscriptionResult, error) {
		assert.Equal(t, int64(1), started.LastChargedPeriod, "the recovered period is committed first")
		paid := started
		paid.Reference = testTxHash(2)
		return &SubscriptionResult{Receipt: subscriptionReceipt(paid), Subscription: paid}, nil
	}, &renewalHooks{
		resolve: func(SubscriptionPendingPayment, SubscriptionRecord) (string, error) { return testTxHash(1), nil },
		recovered: func(period int64, result SubscriptionResult) {
			recovered = append(recovered, period)
			assert.Equal(t, testTxHash(1), result.Subscription.Reference)
		},
	})
	require.NoError(t, err)
	assert.Equal(t, renewalRenewed, status)
	assert.Equal(t, int64(2), result.Subscription.LastChargedPeriod)
	assert.Equal(t, []int64{1}, recovered, "the recovered period is reported once")
}

func TestSubscriptionHeldActivationIsCommittedAsPaid(t *testing.T) {
	held := true
	rpc, sent := subscriptionChain(t, &held)
	var reported []int64
	h := newSubscriptionHarness(t, func(config *SubscriptionConfig) {
		config.RPC = rpc
		config.OnPaymentHeld = func(_ context.Context, period int64, result SubscriptionResult) {
			reported = append(reported, period)
			assert.True(t, result.Subscription.Held)
		}
	})
	_, authorization := h.pay("kai", subscriptionPayerKey)
	result, err := h.subscribe("kai", authorization)
	require.NoError(t, err, "a held activation is paid: the payer's funds left their account")
	require.Len(t, *sent, 1)
	assert.Equal(t, strings.ToLower(mustTxHash(rpc.sentRawTxs[0])), result.Receipt.Reference)
	assert.Equal(t, []int64{0}, reported, "the held activation is reported once")

	record, err := h.store.GetByKey(context.Background(), "user:kai")
	require.NoError(t, err)
	require.NotNil(t, record)
	assert.True(t, record.Held)
	assert.Equal(t, result.Receipt.Reference, record.Reference)
	marker, _, err := getJSON[activationMarker](context.Background(), h.store.store, h.store.activationKey("user:kai"))
	require.NoError(t, err)
	assert.Nil(t, marker, "the activation lock is released")

	// Later requests reuse it without paying again.
	held = false
	reused, err := h.subscribe("kai", "")
	require.NoError(t, err)
	assert.False(t, reused.IsChallenge())
	assert.Len(t, *sent, 1, "a held activation is not paid again")
}

func TestSubscriptionHeldRenewalIsCommittedAsPaid(t *testing.T) {
	ctx := context.Background()
	held := false
	rpc, sent := subscriptionChain(t, &held)
	var reported []int64
	h := newSubscriptionHarness(t, func(config *SubscriptionConfig) {
		config.RPC = rpc
		config.OnPaymentHeld = func(_ context.Context, period int64, _ SubscriptionResult) { reported = append(reported, period) }
	})
	_, authorization := h.pay("lex", subscriptionPayerKey)
	activated, err := h.subscribe("lex", authorization)
	require.NoError(t, err)
	id := activated.Receipt.SubscriptionID

	h.advance(25 * time.Hour)
	held = true
	renewed, err := h.sub.Renew(ctx, id)
	require.NoError(t, err)
	require.NotNil(t, renewed)
	assert.True(t, renewed.Subscription.Held)
	assert.Equal(t, strings.ToLower(mustTxHash(rpc.sentRawTxs[1])), renewed.Receipt.Reference)
	again, err := h.sub.Renew(ctx, id)
	require.NoError(t, err)
	assert.Nil(t, again, "the held period counts as paid")
	assert.Len(t, *sent, 2, "a held renewal is not billed again")
	assert.Equal(t, []int64{1}, reported)
	record, err := h.store.Get(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, int64(1), record.LastChargedPeriod)
	assert.Nil(t, record.InFlightPayment)
	assert.True(t, record.Held)

	// The next period that credits the recipient clears Held.
	h.advance(24 * time.Hour)
	held = false
	renewed, err = h.sub.Renew(ctx, id)
	require.NoError(t, err)
	require.NotNil(t, renewed)
	assert.False(t, renewed.Subscription.Held)
	assert.Equal(t, []int64{1}, reported)
}

func TestSubscriptionExpiryProofReadsNonceAtObservedBlock(t *testing.T) {
	ctx := context.Background()
	h, rpc, id, _, down := renewalLeftUnknown(t, "lia", true)
	down.Store(false)
	// A lagging node has no receipt for the landed renewal and refuses it
	// again.
	landedHash := mustTxHash(rpc.sentRawTxs[1])
	rpc.onGetReceipt = func(hash string) (*temporpc.JSONRPCResponse, error) {
		if hash == landedHash {
			return &temporpc.JSONRPCResponse{Result: nil}, nil
		}
		return &temporpc.JSONRPCResponse{Result: rpc.receipts[hash]}, nil
	}
	rpc.onSend = func(string) (string, map[string]any, error) {
		return "", nil, &temporpc.JSONRPCError{Code: -32000, Message: "nonce too low"}
	}
	rpc.blockNumber = 42
	h.advance(subscriptionValidBeforeWindow + time.Second)

	for _, answer := range []struct {
		name  string
		nonce string
		err   error
	}{
		{name: "nonce used at every block back to genesis", nonce: "0x8"},
		{name: "block not reached by the node", err: errors.New("header not found")},
		{name: "nonce below the payment's", nonce: "0x6"},
	} {
		var blocks []string
		rpc.nonceAt = func(_, block string) (*temporpc.JSONRPCResponse, error) {
			blocks = append(blocks, block)
			if answer.err != nil {
				return nil, answer.err
			}
			return &temporpc.JSONRPCResponse{Result: answer.nonce}, nil
		}
		_, err := h.sub.Renew(ctx, id)
		require.ErrorIs(t, err, mpp.ErrorTypePaymentOutcomeUnknown, answer.name)
		requireRenewalLocked(t, h, rpc, id)
		require.NotEmpty(t, blocks)
		assert.Equal(t, "0x2a", blocks[0], "%s: the nonce is read at the observed block", answer.name)
		if answer.nonce == "0x8" {
			// The search for the block that used it reads only numbered
			// blocks below it, and finds none.
			for _, block := range blocks[1:] {
				assert.Regexp(t, "^0x[0-9a-f]+$", block)
			}
			assert.Equal(t, "0x0", blocks[len(blocks)-1])
		} else {
			assert.Len(t, blocks, 1, answer.name)
		}
	}
}

func TestSubscriptionStoreNeverPaysSettledActivationAgain(t *testing.T) {
	ctx := context.Background()
	backing := tempo.NewMemoryStore()
	store, err := NewSubscriptionStore(backing, SubscriptionStoreOptions{ActivationTimeout: time.Minute})
	require.NoError(t, err)
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	settled := SubscriptionRecord{LookupKey: "user:x", SubscriptionID: "sub_1", BillingAnchor: store.timestamp()}
	marker, err := json.Marshal(activationMarker{
		ChallengeID: "committing", CommittingAt: store.timestamp(), StartedAt: store.timestamp(), Settled: &settled,
		Payment: &SubscriptionPendingPayment{Hash: testTxHash(1), Unknown: true}, Subscription: &settled,
	})
	require.NoError(t, err)
	require.NoError(t, backing.Put(ctx, store.activationKey("user:x"), string(marker)))
	hooks := &activationHooks{resolve: func(SubscriptionPendingPayment, SubscriptionRecord) (string, error) {
		t.Fatal("a settled activation's payment is not resolved again")
		return "", nil
	}}

	status, _, _, err := store.activate(ctx, "challenge-1", "user:x", nil, mustNotCreate(t), hooks)
	require.NoError(t, err)
	assert.Equal(t, activationInFlight, status, "a live commit holds the lock")

	now = now.Add(time.Minute)
	status, _, existing, err := store.activate(ctx, "challenge-2", "user:x", func(SubscriptionRecord) bool { return true }, mustNotCreate(t), hooks)
	require.NoError(t, err)
	assert.Equal(t, activationExisting, status)
	assert.Equal(t, "sub_1", existing.SubscriptionID, "the takeover commits the settled record")
}

func TestSubscriptionStoreKeepsLockWhenActivationCannotBeSaved(t *testing.T) {
	ctx := context.Background()
	var failNext atomic.Bool
	backing := &faultyStore{MemoryStore: tempo.NewMemoryStore()}
	backing.fail = func(key string) bool {
		return strings.HasPrefix(key, defaultSubscriptionActivationPrefix) && failNext.CompareAndSwap(true, false)
	}
	store, err := NewSubscriptionStore(backing, SubscriptionStoreOptions{})
	require.NoError(t, err)

	// A custom Activate settled, then saving its result failed once.
	_, _, _, err = store.activate(ctx, "challenge-1", "user:x", nil, func() (*SubscriptionResult, error) {
		failNext.Store(true)
		return &SubscriptionResult{Subscription: SubscriptionRecord{LookupKey: "user:x", SubscriptionID: "sub_1"}}, nil
	}, nil)
	require.ErrorContains(t, err, "store unavailable")
	marker, _, err := getJSON[activationMarker](ctx, backing, store.activationKey("user:x"))
	require.NoError(t, err)
	require.NotNil(t, marker, "the lock is kept")
	assert.Equal(t, "challenge-1", marker.ChallengeID)

	status, _, _, err := store.activate(ctx, "challenge-2", "user:x", nil, mustNotCreate(t), nil)
	require.NoError(t, err)
	assert.Equal(t, activationInFlight, status)
}

func TestSubscriptionSupersededRenewalRecordsPaidPeriod(t *testing.T) {
	ctx := context.Background()
	var renewals []SubscriptionRenewParams
	var renewed []int64
	var h *subscriptionHarness
	h = newSubscriptionHarness(t, func(config *SubscriptionConfig) {
		manualHooks(&h, &renewals)(config)
		config.OnRenewed = func(_ context.Context, period int64, _ SubscriptionResult) { renewed = append(renewed, period) }
		renew := config.Renew
		config.Renew = func(ctx context.Context, params SubscriptionRenewParams) (*SubscriptionResult, error) {
			// A concurrent activation replaces the subscription mid-charge.
			require.NoError(t, h.store.Put(ctx, SubscriptionRecord{LookupKey: params.Subscription.LookupKey, SubscriptionID: "sub_new"}))
			return renew(ctx, params)
		}
	})
	_, authorization := h.pay("vic", subscriptionPayerKey)
	_, err := h.subscribe("vic", authorization)
	require.NoError(t, err)

	h.advance(25 * time.Hour)
	result, err := h.sub.Renew(ctx, "sub_vic")
	require.NoError(t, err)
	assert.Nil(t, result, "a superseded subscription is not renewed for the caller")
	assert.Equal(t, []int64{1}, renewed, "the paid period is reported")
	record, err := h.store.Get(ctx, "sub_vic")
	require.NoError(t, err)
	assert.Equal(t, int64(1), record.LastChargedPeriod, "the paid period is recorded")
	assert.Equal(t, testTxHash(101), record.Reference)
	assert.Nil(t, record.InFlightPeriod)
}

func TestSubscriptionStoreCancelKeepsConcurrentRenewal(t *testing.T) {
	ctx := context.Background()
	backing := &hookedStore{MemoryStore: tempo.NewMemoryStore()}
	store, err := NewSubscriptionStore(backing, SubscriptionStoreOptions{})
	require.NoError(t, err)
	require.NoError(t, store.Put(ctx, SubscriptionRecord{LookupKey: "user:x", SubscriptionID: "sub_1"}))
	renewedOnce := false
	backing.before = func(key string) {
		if key != store.recordKey("sub_1") || renewedOnce {
			return
		}
		renewedOnce = true
		// A renewal of sub_1 commits just before the activation cancels it.
		current, raw, err := getJSON[SubscriptionRecord](ctx, backing.MemoryStore, key)
		require.NoError(t, err)
		next := *current
		next.LastChargedPeriod = 5
		encoded, err := json.Marshal(next)
		require.NoError(t, err)
		swapped, err := backing.MemoryStore.CompareAndSwap(ctx, key, &raw, string(encoded))
		require.NoError(t, err)
		require.True(t, swapped)
	}
	status, _, _, err := store.activate(ctx, "challenge-1", "user:x", nil, func() (*SubscriptionResult, error) {
		return &SubscriptionResult{Subscription: SubscriptionRecord{LookupKey: "user:x", SubscriptionID: "sub_2"}}, nil
	}, nil)
	require.NoError(t, err)
	assert.Equal(t, activationActivated, status)
	previous, err := store.Get(ctx, "sub_1")
	require.NoError(t, err)
	assert.NotEmpty(t, previous.CanceledAt)
	assert.Equal(t, int64(5), previous.LastChargedPeriod, "canceling keeps the concurrent renewal")
}

func TestSubscriptionStoreReleasesUnpaidClaims(t *testing.T) {
	ctx := context.Background()
	backing := tempo.NewMemoryStore()
	store, err := NewSubscriptionStore(backing, SubscriptionStoreOptions{})
	require.NoError(t, err)
	_, _, _, err = store.activate(ctx, "challenge-1", "user:x", nil, func() (*SubscriptionResult, error) {
		return nil, paymentNotSent(errors.New("card declined"))
	}, nil)
	require.Error(t, err)
	_, ok, err := backing.Get(ctx, store.credentialKey("challenge-1"))
	require.NoError(t, err)
	assert.False(t, ok, "a claim whose attempt paid nothing is released")

	status, _, _, err := store.activate(ctx, "challenge-1", "user:x", nil, func() (*SubscriptionResult, error) {
		return &SubscriptionResult{Subscription: SubscriptionRecord{LookupKey: "user:x", SubscriptionID: "sub_1"}}, nil
	}, nil)
	require.NoError(t, err)
	assert.Equal(t, activationActivated, status)
	_, ok, err = backing.Get(ctx, store.credentialKey("challenge-1"))
	require.NoError(t, err)
	assert.True(t, ok, "a paid claim is kept")
}

func TestSubscriptionStoreClaimsExpireWithChallenge(t *testing.T) {
	ctx := context.Background()
	server := miniredis.RunT(t)
	store, err := NewSubscriptionStore(tempo.NewRedisStore(redis.NewClient(&redis.Options{Addr: server.Addr()}), 0), SubscriptionStoreOptions{ExpireCredentialClaims: true})
	require.NoError(t, err)
	status, _, _, err := store.activate(ctx, "challenge-1", "user:x", nil, func() (*SubscriptionResult, error) {
		return &SubscriptionResult{Subscription: SubscriptionRecord{LookupKey: "user:x", SubscriptionID: "sub_1"}}, nil
	}, &activationHooks{claimUntil: time.Now().Add(10 * time.Minute)})
	require.NoError(t, err)
	assert.Equal(t, activationActivated, status)
	assert.Positive(t, server.TTL(store.credentialKey("challenge-1")), "the claim expires with its Challenge")
	assert.Zero(t, server.TTL(store.recordKey("sub_1")), "the record persists")
}

func TestSubscriptionForgedCredentialRetryUsesServerKey(t *testing.T) {
	rpc, sent := subscriptionChain(t, nil)
	h := newSubscriptionHarness(t, func(config *SubscriptionConfig) { config.RPC = rpc })
	credential, _ := h.pay("ann", subscriptionPayerKey)
	echoed, err := mpp.B64Decode(credential.Challenge.Request)
	require.NoError(t, err)
	attacker := "0x" + strings.Repeat("66", 20)
	echoed["methodDetails"].(map[string]any)["accessKey"].(map[string]any)["accessKeyAddress"] = attacker
	raw, err := json.Marshal(echoed)
	require.NoError(t, err)
	credential.Challenge.Request = base64.RawURLEncoding.EncodeToString(raw)

	result, err := h.subscribe("ann", mpptest.FormatCredential(credential))
	require.Error(t, err)
	require.NotNil(t, result)
	require.NotNil(t, result.Challenge)
	request, err := tempo.ParseSubscriptionRequest(result.Challenge.Request)
	require.NoError(t, err)
	stored, err := h.store.GetAccessKey(context.Background(), "user:ann")
	require.NoError(t, err)
	assert.Equal(t, stored.AccessKeyAddress, request.AccessKey.AccessKeyAddress, "the retry Challenge names the server's key")
	assert.Empty(t, *sent)
}

func TestSubscriptionRejectsAnotherLookupsAccessKey(t *testing.T) {
	rpc, sent := subscriptionChain(t, nil)
	h := newSubscriptionHarness(t, func(config *SubscriptionConfig) { config.RPC = rpc })
	_, authorization := h.pay("bob", subscriptionPayerKey)
	_, err := h.subscribe("ann", authorization)
	require.ErrorContains(t, err, "accessKey is not this subscription's")
	assert.Empty(t, *sent)
	current, err := h.store.GetByKey(context.Background(), "user:ann")
	require.NoError(t, err)
	assert.Nil(t, current)
}

func TestSubscriptionCustomActivationIsNotRenewedAutomatically(t *testing.T) {
	ctx := context.Background()
	var renewals []SubscriptionRenewParams
	var h *subscriptionHarness
	h = newSubscriptionHarness(t, func(config *SubscriptionConfig) {
		manualHooks(&h, &renewals)(config)
		config.Renew = nil
		config.RPC = &mockRPC{receipts: map[string]map[string]any{}}
	})
	_, authorization := h.pay("wes", subscriptionPayerKey)
	_, err := h.subscribe("wes", authorization)
	require.NoError(t, err)

	h.advance(25 * time.Hour)
	result, err := h.sub.Renew(ctx, "sub_wes")
	require.NoError(t, err, "no automatic billing with a key the store never held")
	assert.Nil(t, result)
	served, err := h.subscribe("wes", "")
	require.NoError(t, err)
	assert.True(t, served.IsChallenge())
}

// nonceUsedBlock is the block nonceSpentRenewal's chain used the pending
// renewal's nonce in, below its latest block, nonceSpentLatest.
const (
	nonceSpentLatest = 1000
	nonceUsedBlock   = 977
)

// nonceSpentChain is the chain nonceSpentRenewal leaves: hidden hides the
// pending payment's receipt, searchDown fails the nonce and block reads
// below the latest block, and blockReads counts reads of numbered blocks.
type nonceSpentChain struct {
	hidden, searchDown atomic.Bool
	blockReads         atomic.Int64
}

// nonceSpentRenewal leaves the first renewal of a new subscription pending
// past its valid_before, its nonce used in block nonceUsedBlock. That block
// lists the pending payment when landed (with its receipt on the node while
// chain.hidden is unset), and otherwise only another transaction. credit
// restores the node's broadcast, which accepts and credits a transaction
// once landedFlag is set.
func nonceSpentRenewal(t *testing.T, user string, landed bool) (h *subscriptionHarness, rpc *mockRPC, id string, chain *nonceSpentChain, landedFlag *atomic.Bool, credit func(string) (string, map[string]any, error)) {
	t.Helper()
	h, rpc, id, landedFlag, down := renewalLeftUnknown(t, user, landed)
	down.Store(false)
	credit = rpc.onSend
	pendingHash := mustTxHash(rpc.sentRawTxs[1])
	tx, err := tempotx.Deserialize(rpc.sentRawTxs[1])
	require.NoError(t, err)
	chain = &nonceSpentChain{}
	chain.hidden.Store(true)
	rpc.onGetReceipt = func(hash string) (*temporpc.JSONRPCResponse, error) {
		if chain.hidden.Load() && hash == pendingHash {
			return &temporpc.JSONRPCResponse{Result: nil}, nil
		}
		return &temporpc.JSONRPCResponse{Result: rpc.receipts[hash]}, nil
	}
	rpc.onSend = func(string) (string, map[string]any, error) {
		return "", nil, &temporpc.JSONRPCError{Code: -32000, Message: "nonce too low"}
	}
	rpc.blockNumber = nonceSpentLatest
	rpc.nonceAt = func(_, block string) (*temporpc.JSONRPCResponse, error) {
		number, err := hexutil.DecodeUint64(block)
		if err != nil {
			// "pending", for a new payment's nonce.
			return &temporpc.JSONRPCResponse{Result: hexutil.EncodeUint64(tx.Nonce + 1)}, nil
		}
		if number < nonceSpentLatest && chain.searchDown.Load() {
			return nil, errors.New("missing trie node")
		}
		nonce := tx.Nonce
		if number >= nonceUsedBlock {
			nonce++
		}
		return &temporpc.JSONRPCResponse{Result: hexutil.EncodeUint64(nonce)}, nil
	}
	rpc.blockAt = func(number uint64) (*temporpc.JSONRPCResponse, error) {
		chain.blockReads.Add(1)
		if chain.searchDown.Load() {
			return nil, errors.New("block unavailable")
		}
		transactions := []any{testTxHash(9)}
		if number == nonceUsedBlock && landed {
			transactions = append(transactions, pendingHash)
		}
		return &temporpc.JSONRPCResponse{Result: map[string]any{"number": hexutil.EncodeUint64(number), "transactions": transactions}}, nil
	}
	h.advance(subscriptionValidBeforeWindow + time.Second)
	return h, rpc, id, chain, landedFlag, credit
}

func TestSubscriptionRenewalFailsNonceTakenByAnotherTransaction(t *testing.T) {
	ctx := context.Background()
	h, rpc, id, chain, landed, credit := nonceSpentRenewal(t, "noa", false)

	// The block that used the payment's nonce does not list it: another
	// transaction took the nonce, so the payment failed and the period is
	// billed afresh in the same attempt.
	landed.Store(true)
	rpc.onSend = credit
	sentBefore := len(rpc.sentRawTxs)
	renewed, err := h.sub.Renew(ctx, id)
	require.NoError(t, err)
	require.NotNil(t, renewed)
	assert.Equal(t, int64(1), chain.blockReads.Load(), "only the block that used the nonce is read")
	require.Len(t, rpc.sentRawTxs, sentBefore+1)
	rebilled := rpc.sentRawTxs[len(rpc.sentRawTxs)-1]
	assert.NotEqual(t, rpc.sentRawTxs[1], rebilled)
	assert.Equal(t, strings.ToLower(mustTxHash(rebilled)), renewed.Receipt.Reference)
	record, err := h.store.Get(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, int64(1), record.LastChargedPeriod)
	assert.Nil(t, record.InFlightPayment)
}

func TestSubscriptionRenewalPrunedReceiptOfIncludedPaymentKeepsLock(t *testing.T) {
	ctx := context.Background()
	h, rpc, id, chain, _, _ := nonceSpentRenewal(t, "ora", true)
	landedHash := mustTxHash(rpc.sentRawTxs[1])

	// The block that used the nonce lists the payment but its receipt is
	// gone (pruned, or a lagging replica): it landed, so it never fails and
	// the period is never billed again, however long that lasts.
	for range 2 {
		_, err := h.sub.Renew(ctx, id)
		require.ErrorIs(t, err, mpp.ErrorTypePaymentOutcomeUnknown)
		require.NotErrorIs(t, err, errSubscriptionPaymentFailed)
		requireRenewalLocked(t, h, rpc, id)
		h.advance(time.Hour)
	}

	// Once its receipt reads again, the landed payment settles the period.
	chain.hidden.Store(false)
	renewed, err := h.sub.Renew(ctx, id)
	require.NoError(t, err)
	require.NotNil(t, renewed)
	assert.Equal(t, strings.ToLower(landedHash), renewed.Receipt.Reference)
	for _, raw := range rpc.sentRawTxs[2:] {
		assert.Equal(t, rpc.sentRawTxs[1], raw, "the period is not billed again")
	}
}

func TestSubscriptionRenewalNonceSearchErrorIsUnknown(t *testing.T) {
	ctx := context.Background()
	h, rpc, id, chain, landed, credit := nonceSpentRenewal(t, "pax", false)

	// The nonce is past the payment's at the latest block, but the blocks
	// below it cannot be read: that proves nothing, so the period stays
	// locked.
	chain.searchDown.Store(true)
	_, err := h.sub.Renew(ctx, id)
	require.ErrorIs(t, err, mpp.ErrorTypePaymentOutcomeUnknown)
	require.NotErrorIs(t, err, errSubscriptionPaymentFailed)
	requireRenewalLocked(t, h, rpc, id)

	// Once the search reads, the payment is proven failed and re-billed.
	chain.searchDown.Store(false)
	landed.Store(true)
	rpc.onSend = credit
	renewed, err := h.sub.Renew(ctx, id)
	require.NoError(t, err)
	require.NotNil(t, renewed)
	assert.NotEqual(t, strings.ToLower(mustTxHash(rpc.sentRawTxs[1])), renewed.Receipt.Reference)
}

func TestNonceUsedAtSearchIsBounded(t *testing.T) {
	ctx := context.Background()
	const account = "0x00000000000000000000000000000000000000aa"
	chain := func(used uint64) (*mockRPC, *int) {
		reads := 0
		return &mockRPC{nonceAt: func(_, block string) (*temporpc.JSONRPCResponse, error) {
			reads++
			number, err := hexutil.DecodeUint64(block)
			require.NoError(t, err, "every read names its block")
			nonce := uint64(7)
			if number >= used {
				nonce = 9
			}
			return &temporpc.JSONRPCResponse{Result: hexutil.EncodeUint64(nonce)}, nil
		}}, &reads
	}

	for _, used := range []uint64{1, 5, 999_999, 1_000_000, 123_456} {
		rpc, reads := chain(used)
		got, err := nonceUsedAt(ctx, rpc, account, 7, 1_000_000)
		require.NoError(t, err)
		assert.Equal(t, used, got)
		assert.LessOrEqual(t, *reads, subscriptionNonceSearchReads)
	}

	// A nonce already past the payment's at genesis is no answer.
	rpc, _ := chain(0)
	_, err := nonceUsedAt(ctx, rpc, account, 7, 1_000_000)
	require.ErrorIs(t, err, errSubscriptionNonceSearch)

	// A search past its budget gives up rather than guess.
	rpc, reads := chain(1 << 60)
	_, err = nonceUsedAt(ctx, rpc, account, 7, 1<<62)
	require.ErrorIs(t, err, errSubscriptionNonceSearch)
	assert.Equal(t, subscriptionNonceSearchReads, *reads)
}

func TestSubscriptionStoreLookupKeysNeverReachAddressIndex(t *testing.T) {
	ctx := context.Background()
	store, err := NewSubscriptionStore(tempo.NewMemoryStore(), SubscriptionStoreOptions{})
	require.NoError(t, err)
	victim, err := store.GetOrCreateAccessKey(ctx, "user:victim")
	require.NoError(t, err)

	// A lookup key spelled like the victim key's address index entry gets
	// its own key, and leaves the victim's reachable by address.
	colliding := "address:" + strings.ToLower(victim.AccessKeyAddress)
	attacker, err := store.GetOrCreateAccessKey(ctx, colliding)
	require.NoError(t, err)
	assert.NotEqual(t, victim.AccessKeyAddress, attacker.AccessKeyAddress)
	other, err := store.CreateChallengeAccessKey(ctx)
	require.NoError(t, err)
	require.NoError(t, store.RetainAccessKey(ctx, colliding, *other))
	byAddress, err := store.GetAccessKeyByAddress(ctx, victim.AccessKeyAddress, "")
	require.NoError(t, err)
	require.NotNil(t, byAddress)
	assert.Equal(t, victim.PrivateKey, byAddress.PrivateKey)
	got, err := store.GetAccessKey(ctx, colliding)
	require.NoError(t, err)
	assert.Equal(t, other.AccessKeyAddress, got.AccessKeyAddress)

	// Escaped lookup keys stay distinct from each other and from the index;
	// any other lookup key keeps mppx's layout.
	keys := map[string]bool{}
	for _, key := range []string{"user:x", colliding, "lookup:" + colliding, "lookup:x", "x"} {
		keys[store.accessKeyKey(key)] = true
	}
	keys[store.accessKeyAddressKey(victim.AccessKeyAddress)] = true
	assert.Len(t, keys, 6)
	assert.Equal(t, "tempo:subscription:access-key:user:x", store.accessKeyKey("user:x"))
}
