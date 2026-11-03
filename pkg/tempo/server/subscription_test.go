package chargeserver

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tempoxyz/tempo-go/pkg/keychain"
	temposigner "github.com/tempoxyz/tempo-go/pkg/signer"
	tempotx "github.com/tempoxyz/tempo-go/pkg/transaction"

	"github.com/tempoxyz/mpp-go/pkg/internal/mpptest"
	"github.com/tempoxyz/mpp-go/pkg/mpp"
	mppserver "github.com/tempoxyz/mpp-go/pkg/server"
	"github.com/tempoxyz/mpp-go/pkg/tempo"
	chargeclient "github.com/tempoxyz/mpp-go/pkg/tempo/client"
)

const (
	subscriptionSecret    = "subscription-test-secret-key-0123456789"
	subscriptionRealm     = "api.example.com"
	subscriptionPayerKey  = "0x59c6995e998f97a5a0044966f0945389dc9e86dae88c7a8412f4603b6b78690d"
	subscriptionOtherKey  = "0x5de4111afa1a4b94908f83103eb1f1706367c2e68ca870fc3fb9a804cdab365a"
	subscriptionRecipient = "0x70997970c51812dc3a010c7d01b50e0d17dc79c8"
)

// subscriptionHarness is a subscription server with a controllable clock.
type subscriptionHarness struct {
	t     *testing.T
	sub   *Subscription
	store *SubscriptionStore
	mu    sync.Mutex
	now   time.Time
}

func (h *subscriptionHarness) clock() time.Time {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.now
}

func (h *subscriptionHarness) advance(d time.Duration) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.now = h.now.Add(d)
}

func newSubscriptionHarness(t *testing.T, configure func(*SubscriptionConfig)) *subscriptionHarness {
	t.Helper()
	store, err := NewSubscriptionStore(tempo.NewMemoryStore(), SubscriptionStoreOptions{})
	require.NoError(t, err)
	h := &subscriptionHarness{t: t, store: store, now: time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)}
	store.now = h.clock
	config := SubscriptionConfig{
		Realm:               subscriptionRealm,
		SecretKey:           subscriptionSecret,
		Recipient:           subscriptionRecipient,
		Currency:            tempo.PathUSDAddress,
		Amount:              "1",
		PeriodCount:         "1",
		PeriodUnit:          tempo.SubscriptionPeriodDay,
		SubscriptionExpires: time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC),
		Store:               store,
		Resolve: func(_ context.Context, params SubscriptionResolveParams) (*SubscriptionLookup, error) {
			if params.Request == nil || params.Request.Header.Get("X-User") == "" {
				return nil, nil
			}
			return &SubscriptionLookup{Key: "user:" + params.Request.Header.Get("X-User")}, nil
		},
	}
	if configure != nil {
		configure(&config)
	}
	sub, err := NewSubscription(config)
	require.NoError(t, err)
	sub.now = h.clock
	h.sub = sub
	return h
}

func (h *subscriptionHarness) request(user, authorization string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "https://api.example.com/feed", nil)
	if user != "" {
		r.Header.Set("X-User", user)
	}
	if authorization != "" {
		r.Header.Set("Authorization", authorization)
	}
	return r
}

func (h *subscriptionHarness) subscribe(user, authorization string) (*mppserver.ChargeResult, error) {
	return h.sub.SubscribeRequest(h.request(user, authorization), SubscribeParams{})
}

// pay answers a fresh Challenge for user with a credential signed by key.
func (h *subscriptionHarness) pay(user, key string) (*mpp.Credential, string) {
	h.t.Helper()
	result, err := h.subscribe(user, "")
	require.NoError(h.t, err)
	require.True(h.t, result.IsChallenge(), "want a Challenge")
	client, err := chargeclient.NewSubscription(chargeclient.SubscriptionConfig{PrivateKey: key})
	require.NoError(h.t, err)
	credential, err := client.CreateCredential(context.Background(), result.Challenge)
	require.NoError(h.t, err)
	return credential, mpptest.FormatCredential(credential)
}

func testTxHash(n int) string { return fmt.Sprintf("0x%064x", n) }

// manualHooks returns Activate and Renew hooks settling without a chain.
func manualHooks(h **subscriptionHarness, renewals *[]SubscriptionRenewParams) func(*SubscriptionConfig) {
	return func(config *SubscriptionConfig) {
		config.AccessKey = func(context.Context, SubscriptionAccessKeyParams) (*tempo.SubscriptionAccessKey, error) {
			return &tempo.SubscriptionAccessKey{AccessKeyAddress: "0x" + strings.Repeat("ab", 20), KeyType: tempo.SubscriptionKeyTypeSecp256k1}, nil
		}
		config.Activate = func(_ context.Context, params SubscriptionActivateParams) (*SubscriptionResult, error) {
			timestamp := formatTimestamp((*h).clock())
			source := params.Source
			record := SubscriptionRecord{
				Amount: params.Subscription.Amount, BillingAnchor: timestamp, ChainID: params.Subscription.ChainID,
				Currency: params.Subscription.Currency, ExternalID: params.Subscription.ExternalID,
				LookupKey: params.Resolved.Key, Payer: &source, PeriodCount: params.Subscription.PeriodCount,
				PeriodUnit: params.Subscription.PeriodUnit, Recipient: params.Subscription.Recipient,
				Reference: testTxHash(1), SubscriptionExpires: params.Subscription.SubscriptionExpires,
				SubscriptionID: "sub_" + strings.TrimPrefix(params.Resolved.Key, "user:"), Timestamp: timestamp,
			}
			return &SubscriptionResult{Receipt: subscriptionReceipt(record), Subscription: record}, nil
		}
		config.Renew = func(_ context.Context, params SubscriptionRenewParams) (*SubscriptionResult, error) {
			*renewals = append(*renewals, params)
			record := params.Subscription
			record.LastChargedPeriod = params.PeriodIndex
			record.Reference = testTxHash(100 + int(params.PeriodIndex))
			record.Timestamp = formatTimestamp((*h).clock())
			return &SubscriptionResult{Receipt: subscriptionReceipt(record), Subscription: record}, nil
		}
	}
}

func TestSubscriptionActivatesReusesAndRenews(t *testing.T) {
	var renewals []SubscriptionRenewParams
	var activated, renewed []SubscriptionResult
	var h *subscriptionHarness
	h = newSubscriptionHarness(t, func(config *SubscriptionConfig) {
		manualHooks(&h, &renewals)(config)
		config.OnActivated = func(_ context.Context, result SubscriptionResult) { activated = append(activated, result) }
		config.OnRenewed = func(_ context.Context, _ int64, result SubscriptionResult) { renewed = append(renewed, result) }
	})

	challenge, err := h.subscribe("alice", "")
	require.NoError(t, err)
	require.True(t, challenge.IsChallenge())
	assert.Equal(t, tempo.IntentSubscription, challenge.Challenge.Intent)
	request, err := tempo.ParseSubscriptionRequest(challenge.Challenge.Request)
	require.NoError(t, err)
	assert.Equal(t, "1000000", request.Amount)
	assert.Equal(t, tempo.ChainIDModerato, *request.ChainID, "omitted chainId defaults to Tempo testnet")
	require.NotNil(t, request.AccessKey)

	credential, authorization := h.pay("alice", subscriptionPayerKey)
	result, err := h.subscribe("alice", authorization)
	require.NoError(t, err)
	require.False(t, result.IsChallenge())
	assert.Equal(t, "sub_alice", result.Receipt.SubscriptionID)
	assert.Equal(t, testTxHash(1), result.Receipt.Reference)
	require.Len(t, activated, 1)
	payer, err := temposigner.NewSigner(subscriptionPayerKey)
	require.NoError(t, err)
	assert.Equal(t, strings.ToLower(payer.Address().Hex()), activated[0].Subscription.Payer.Address)
	assert.True(t, strings.HasSuffix(credential.Source, strings.ToLower(payer.Address().Hex())))

	// A later request reuses the active subscription without a Credential.
	reused, err := h.subscribe("alice", "")
	require.NoError(t, err)
	require.False(t, reused.IsChallenge())
	assert.Equal(t, "sub_alice", reused.Receipt.SubscriptionID)
	assert.Empty(t, renewals)

	// The same Credential cannot activate twice.
	_, err = h.subscribe("alice", authorization)
	assert.ErrorContains(t, err, "already been used")

	// Once a period passes the request path renews it, keeping the ID.
	h.advance(25 * time.Hour)
	renewedResult, err := h.subscribe("alice", "")
	require.NoError(t, err)
	assert.Equal(t, "sub_alice", renewedResult.Receipt.SubscriptionID)
	assert.Equal(t, testTxHash(101), renewedResult.Receipt.Reference)
	require.Len(t, renewals, 1)
	assert.Equal(t, int64(1), renewals[0].PeriodIndex)
	assert.Equal(t, "renewal:sub_alice:1", renewals[0].InFlightReference)
	require.Len(t, renewed, 1)
	record, err := h.store.Get(context.Background(), "sub_alice")
	require.NoError(t, err)
	assert.Equal(t, int64(1), record.LastChargedPeriod)
	assert.Nil(t, record.InFlightPeriod)

	// Paid for this period: no second renewal.
	again, err := h.subscribe("alice", "")
	require.NoError(t, err)
	assert.Equal(t, testTxHash(101), again.Receipt.Reference)
	assert.Len(t, renewals, 1)

	// Background renewal bills the next period outside the request path.
	result2, err := h.sub.Renew(context.Background(), "sub_alice")
	require.NoError(t, err)
	assert.Nil(t, result2, "nothing is due yet")
	h.advance(24 * time.Hour)
	result2, err = h.sub.Renew(context.Background(), "sub_alice")
	require.NoError(t, err)
	require.NotNil(t, result2)
	assert.Equal(t, "sub_alice", result2.Receipt.SubscriptionID)
	assert.Equal(t, int64(2), result2.Subscription.LastChargedPeriod)

	// A different binding is not authorized by this subscription.
	other, err := h.sub.SubscribeRequest(h.request("alice", ""), SubscribeParams{Amount: "2"})
	require.NoError(t, err)
	assert.True(t, other.IsChallenge())
}

func TestSubscriptionRejectsInvalidCredentials(t *testing.T) {
	var renewals []SubscriptionRenewParams
	var h *subscriptionHarness
	h = newSubscriptionHarness(t, func(config *SubscriptionConfig) { manualHooks(&h, &renewals)(config) })

	t.Run("declared source differs from the signer", func(t *testing.T) {
		credential, _ := h.pay("bob", subscriptionPayerKey)
		other, err := temposigner.NewSigner(subscriptionOtherKey)
		require.NoError(t, err)
		credential.Source = tempo.ProofSource(tempo.ChainIDModerato, other.Address())
		_, err = h.subscribe("bob", mpptest.FormatCredential(credential))
		assert.ErrorContains(t, err, "credential source does not match signature")
	})

	t.Run("key authorization for another access key", func(t *testing.T) {
		result, err := h.subscribe("carol", "")
		require.NoError(t, err)
		client, err := chargeclient.NewSubscription(chargeclient.SubscriptionConfig{
			PrivateKey: subscriptionPayerKey,
			AccessKey:  &tempo.SubscriptionAccessKey{AccessKeyAddress: "0x" + strings.Repeat("cd", 20), KeyType: tempo.SubscriptionKeyTypeSecp256k1},
		})
		require.NoError(t, err)
		credential, err := client.CreateCredential(context.Background(), result.Challenge)
		require.NoError(t, err)
		_, err = h.subscribe("carol", mpptest.FormatCredential(credential))
		assert.ErrorContains(t, err, "access key mismatch")
	})

	t.Run("credential for another route binding", func(t *testing.T) {
		_, authorization := h.pay("dave", subscriptionPayerKey)
		_, err := h.sub.SubscribeRequest(h.request("dave", authorization), SubscribeParams{ExternalID: "plan-pro"})
		assert.Error(t, err)
	})
}

func TestSubscriptionRejectsInvalidSettlements(t *testing.T) {
	cases := map[string]struct {
		activate func(SubscriptionRecord) SubscriptionRecord
		want     string
	}{
		"amount": {func(r SubscriptionRecord) SubscriptionRecord { r.Amount = "5"; return r }, "record does not match request"},
		"chainId": {func(r SubscriptionRecord) SubscriptionRecord {
			chainID := tempo.ChainIDMainnet
			r.ChainID = &chainID
			return r
		}, "record does not match request"},
		"externalId":    {func(r SubscriptionRecord) SubscriptionRecord { r.ExternalID = "other"; return r }, "record does not match request"},
		"lookupKey":     {func(r SubscriptionRecord) SubscriptionRecord { r.LookupKey = "user:mallory"; return r }, "lookupKey does not match"},
		"reference":     {func(r SubscriptionRecord) SubscriptionRecord { r.Reference = "pending"; return r }, "transaction hash"},
		"id":            {func(r SubscriptionRecord) SubscriptionRecord { r.SubscriptionID = "not base64url!"; return r }, "base64url"},
		"period":        {func(r SubscriptionRecord) SubscriptionRecord { r.LastChargedPeriod = 1; return r }, "lastChargedPeriod"},
		"anchor":        {func(r SubscriptionRecord) SubscriptionRecord { r.BillingAnchor = r.SubscriptionExpires; return r }, "billingAnchor must be before"},
		"receipt match": {nil, "receipt reference mismatch"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			var renewals []SubscriptionRenewParams
			var h *subscriptionHarness
			h = newSubscriptionHarness(t, func(config *SubscriptionConfig) {
				manualHooks(&h, &renewals)(config)
				activate := config.Activate
				config.Activate = func(ctx context.Context, params SubscriptionActivateParams) (*SubscriptionResult, error) {
					result, err := activate(ctx, params)
					if err != nil {
						return nil, err
					}
					if tc.activate == nil {
						result.Receipt.Reference = testTxHash(9)
						return result, nil
					}
					record := tc.activate(result.Subscription)
					return &SubscriptionResult{Receipt: subscriptionReceipt(record), Subscription: record}, nil
				}
			})
			_, authorization := h.pay("erin", subscriptionPayerKey)
			_, err := h.subscribe("erin", authorization)
			assert.ErrorContains(t, err, tc.want)
			record, err := h.store.GetByKey(context.Background(), "user:erin")
			require.NoError(t, err)
			assert.Nil(t, record, "a rejected settlement is not stored")
		})
	}
}

func TestSubscriptionRejectsRenewalsChangingTheSubscription(t *testing.T) {
	var renewals []SubscriptionRenewParams
	var h *subscriptionHarness
	h = newSubscriptionHarness(t, func(config *SubscriptionConfig) {
		manualHooks(&h, &renewals)(config)
		renew := config.Renew
		config.Renew = func(ctx context.Context, params SubscriptionRenewParams) (*SubscriptionResult, error) {
			result, err := renew(ctx, params)
			if err != nil {
				return nil, err
			}
			record := result.Subscription
			record.SubscriptionID = "sub_new"
			return &SubscriptionResult{Receipt: subscriptionReceipt(record), Subscription: record}, nil
		}
	})
	_, authorization := h.pay("frank", subscriptionPayerKey)
	_, err := h.subscribe("frank", authorization)
	require.NoError(t, err)
	h.advance(25 * time.Hour)
	_, err = h.subscribe("frank", "")
	assert.ErrorContains(t, err, "subscriptionId does not match the active subscription")
	_, err = h.sub.Renew(context.Background(), "sub_frank")
	assert.ErrorContains(t, err, "subscriptionId does not match the active subscription")
	record, err := h.store.Get(context.Background(), "sub_frank")
	require.NoError(t, err)
	assert.Equal(t, int64(0), record.LastChargedPeriod)
	assert.Nil(t, record.InFlightPeriod, "a failed renewal releases its lock")
}

func TestSubscriptionRenewalInFlight(t *testing.T) {
	var renewals []SubscriptionRenewParams
	var h *subscriptionHarness
	h = newSubscriptionHarness(t, func(config *SubscriptionConfig) { manualHooks(&h, &renewals)(config) })
	_, authorization := h.pay("gina", subscriptionPayerKey)
	_, err := h.subscribe("gina", authorization)
	require.NoError(t, err)
	h.advance(25 * time.Hour)

	record, err := h.store.Get(context.Background(), "sub_gina")
	require.NoError(t, err)
	period := int64(1)
	record.InFlightPeriod, record.InFlightAttempt, record.InFlightStartedAt = &period, "other", formatTimestamp((*h).clock())
	require.NoError(t, h.store.Put(context.Background(), *record))

	result, err := h.subscribe("gina", "")
	assert.ErrorIs(t, err, ErrSubscriptionRenewalInFlight)
	assert.Equal(t, "sub_gina", result.Receipt.SubscriptionID)

	recorder := httptest.NewRecorder()
	h.sub.Middleware(SubscribeParams{})(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("handler served during an in-flight renewal")
	})).ServeHTTP(recorder, h.request("gina", ""))
	assert.Equal(t, http.StatusConflict, recorder.Code)
	assert.Equal(t, "1", recorder.Header().Get("Retry-After"))
	assert.Empty(t, renewals)
}

func TestSubscriptionRequireCredential(t *testing.T) {
	var renewals []SubscriptionRenewParams
	var h *subscriptionHarness
	h = newSubscriptionHarness(t, func(config *SubscriptionConfig) {
		manualHooks(&h, &renewals)(config)
		config.RequireCredential = true
	})
	_, authorization := h.pay("hana", subscriptionPayerKey)
	first, err := h.subscribe("hana", authorization)
	require.NoError(t, err)

	result, err := h.subscribe("hana", "")
	require.NoError(t, err)
	assert.True(t, result.IsChallenge(), "an active subscription still needs a fresh Credential")

	_, authorization = h.pay("hana", subscriptionPayerKey)
	reused, err := h.subscribe("hana", authorization)
	require.NoError(t, err)
	assert.Equal(t, first.Receipt.SubscriptionID, reused.Receipt.SubscriptionID)

	_, authorization = h.pay("hana", subscriptionOtherKey)
	_, err = h.subscribe("hana", authorization)
	assert.ErrorContains(t, err, "subscription payer mismatch")
}

func TestSubscriptionRequiresAccessKey(t *testing.T) {
	h := newSubscriptionHarness(t, nil)
	_, err := h.subscribe("", "")
	assert.ErrorContains(t, err, "subscription accessKey is missing")
}

func TestSubscriptionMiddlewareServesWithReceipt(t *testing.T) {
	var renewals []SubscriptionRenewParams
	var h *subscriptionHarness
	h = newSubscriptionHarness(t, func(config *SubscriptionConfig) { manualHooks(&h, &renewals)(config) })
	handler := h.sub.Middleware(SubscribeParams{})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "sub_ivy", mppserver.ReceiptFromContext(r.Context()).SubscriptionID)
		w.WriteHeader(http.StatusOK)
	}))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, h.request("ivy", ""))
	assert.Equal(t, http.StatusPaymentRequired, recorder.Code)
	assert.Contains(t, recorder.Header().Get("WWW-Authenticate"), "subscription")

	_, authorization := h.pay("ivy", subscriptionPayerKey)
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, h.request("ivy", authorization))
	assert.Equal(t, http.StatusOK, recorder.Code)
	receipt, err := mpp.ParseReceipt(recorder.Header().Get("Payment-Receipt"))
	require.NoError(t, err)
	assert.Equal(t, "sub_ivy", receipt.SubscriptionID)
}

// subscriptionChain answers the automatic billing RPC calls, crediting each
// transfer unless held is set.
func subscriptionChain(t *testing.T, held *bool) (*mockRPC, *[]*tempotx.Tx) {
	var sent []*tempotx.Tx
	rpc := &mockRPC{chainID: uint64(tempo.ChainIDModerato), nonce: 7, gasPrice: "0x1", estimateGas: "0x30d40", receipts: map[string]map[string]any{}}
	rpc.onSend = func(raw string) (string, map[string]any, error) {
		tx, err := tempotx.Deserialize(raw)
		require.NoError(t, err)
		sent = append(sent, tx)
		hash := mustTxHash(raw)
		data := tx.Calls[0].Data
		to := common.BytesToAddress(data[4:36])
		if held != nil && *held {
			to = common.HexToAddress("0x" + strings.Repeat("99", 20))
		}
		log := map[string]any{
			"address": tx.Calls[0].To.Hex(),
			"topics": []any{
				tempo.TransferWithMemoTopic.Hex(),
				common.BytesToHash(tx.From.Bytes()).Hex(),
				common.BytesToHash(to.Bytes()).Hex(),
				common.BytesToHash(data[68:100]).Hex(),
			},
			"data": common.BytesToHash(data[36:68]).Hex(),
		}
		return hash, map[string]any{"status": "0x1", "transactionHash": hash, "logs": []any{log}}, nil
	}
	return rpc, &sent
}

func TestSubscriptionAutomaticActivationAndRenewal(t *testing.T) {
	rpc, sent := subscriptionChain(t, nil)
	h := newSubscriptionHarness(t, func(config *SubscriptionConfig) { config.RPC = rpc })

	challenge, err := h.subscribe("jo", "")
	require.NoError(t, err)
	request, err := tempo.ParseSubscriptionRequest(challenge.Challenge.Request)
	require.NoError(t, err)
	stored, err := h.store.GetAccessKey(context.Background(), "user:jo")
	require.NoError(t, err)
	require.NotNil(t, stored, "issuing the Challenge creates the server-owned access key")
	assert.Equal(t, stored.AccessKeyAddress, request.AccessKey.AccessKeyAddress)

	_, authorization := h.pay("jo", subscriptionPayerKey)
	result, err := h.subscribe("jo", authorization)
	require.NoError(t, err)
	require.Len(t, *sent, 1)
	payer, err := temposigner.NewSigner(subscriptionPayerKey)
	require.NoError(t, err)
	activation := (*sent)[0]
	assert.NotNil(t, activation.KeyAuthorization, "the activation installs the access key")
	accessKey, rootAccount, err := keychain.VerifyAccessKeySignature(activation)
	require.NoError(t, err)
	assert.Equal(t, payer.Address(), rootAccount)
	assert.True(t, strings.EqualFold(stored.AccessKeyAddress, accessKey.Hex()))
	assert.Equal(t, strings.ToLower(mustTxHash(rpc.sentRawTxs[0])), result.Receipt.Reference)
	id := result.Receipt.SubscriptionID
	assert.Regexp(t, "^[A-Za-z0-9_-]{24}$", id)

	h.advance(25 * time.Hour)
	renewed, err := h.sub.Renew(context.Background(), id)
	require.NoError(t, err)
	require.NotNil(t, renewed)
	assert.Equal(t, id, renewed.Receipt.SubscriptionID)
	require.Len(t, *sent, 2)
	assert.Nil(t, (*sent)[1].KeyAuthorization, "renewals reuse the installed key")
	assert.NotEqual(t, result.Receipt.Reference, renewed.Receipt.Reference)
}

func TestSubscriptionAutomaticActivationRejectsHeldTransfer(t *testing.T) {
	held := true
	rpc, _ := subscriptionChain(t, &held)
	h := newSubscriptionHarness(t, func(config *SubscriptionConfig) { config.RPC = rpc })
	_, authorization := h.pay("kim", subscriptionPayerKey)
	_, err := h.subscribe("kim", authorization)
	assert.ErrorContains(t, err, "was not credited to the recipient")
	record, err := h.store.GetByKey(context.Background(), "user:kim")
	require.NoError(t, err)
	assert.Nil(t, record)
}

func TestSubscriptionAnonymousChallengeUsesSealedKey(t *testing.T) {
	rpc, sent := subscriptionChain(t, nil)
	h := newSubscriptionHarness(t, func(config *SubscriptionConfig) {
		config.RPC = rpc
		config.RequireCredential = true
		config.Resolve = func(_ context.Context, params SubscriptionResolveParams) (*SubscriptionLookup, error) {
			if params.Source == nil {
				return nil, nil
			}
			return &SubscriptionLookup{Key: "wallet:" + params.Source.Address}, nil
		}
	})
	challenge, err := h.subscribe("", "")
	require.NoError(t, err)
	request, err := tempo.ParseSubscriptionRequest(challenge.Challenge.Request)
	require.NoError(t, err)
	require.NotEmpty(t, request.AccessKey.KeyToken)
	client, err := chargeclient.NewSubscription(chargeclient.SubscriptionConfig{PrivateKey: subscriptionPayerKey})
	require.NoError(t, err)
	credential, err := client.CreateCredential(context.Background(), challenge.Challenge)
	require.NoError(t, err)
	result, err := h.subscribe("", mpptest.FormatCredential(credential))
	require.NoError(t, err)
	assert.NotEmpty(t, result.Receipt.SubscriptionID)
	require.Len(t, *sent, 1)
	payer, _ := temposigner.NewSigner(subscriptionPayerKey)
	retained, err := h.store.GetAccessKey(context.Background(), "wallet:"+strings.ToLower(payer.Address().Hex()))
	require.NoError(t, err)
	require.NotNil(t, retained)
	assert.Empty(t, retained.KeyToken)
}

func TestSubscriptionStoreSerializesActivations(t *testing.T) {
	store, err := NewSubscriptionStore(tempo.NewMemoryStore(), SubscriptionStoreOptions{})
	require.NoError(t, err)
	ctx := context.Background()
	release := make(chan struct{})
	started := make(chan struct{})
	record := SubscriptionRecord{LookupKey: "user:x", SubscriptionID: "sub_x", Reference: testTxHash(1)}
	done := make(chan subscriptionActivationStatus)
	go func() {
		status, _, _, _ := store.activate(ctx, "challenge-1", "user:x", nil, func() (*SubscriptionResult, error) {
			close(started)
			<-release
			return &SubscriptionResult{Subscription: record}, nil
		})
		done <- status
	}()
	<-started
	status, _, _, err := store.activate(ctx, "challenge-2", "user:x", nil, func() (*SubscriptionResult, error) {
		return nil, errors.New("must not run")
	})
	require.NoError(t, err)
	assert.Equal(t, activationInFlight, status)
	close(release)
	assert.Equal(t, activationActivated, <-done)
	current, err := store.GetByKey(ctx, "user:x")
	require.NoError(t, err)
	assert.Equal(t, "sub_x", current.SubscriptionID)

	// A failed activation releases the lock for a retry.
	_, _, _, err = store.activate(ctx, "challenge-3", "user:y", nil, func() (*SubscriptionResult, error) {
		return nil, errors.New("settlement failed")
	})
	assert.Error(t, err)
	status, _, _, err = store.activate(ctx, "challenge-4", "user:y", nil, func() (*SubscriptionResult, error) {
		return &SubscriptionResult{Subscription: SubscriptionRecord{LookupKey: "user:y", SubscriptionID: "sub_y"}}, nil
	})
	require.NoError(t, err)
	assert.Equal(t, activationActivated, status)

	// A new activation replaces and cancels the previous subscription.
	status, _, _, err = store.activate(ctx, "challenge-5", "user:x", nil, func() (*SubscriptionResult, error) {
		return &SubscriptionResult{Subscription: SubscriptionRecord{LookupKey: "user:x", SubscriptionID: "sub_x2"}}, nil
	})
	require.NoError(t, err)
	assert.Equal(t, activationActivated, status)
	previous, err := store.Get(ctx, "sub_x")
	require.NoError(t, err)
	assert.NotEmpty(t, previous.CanceledAt)
}
