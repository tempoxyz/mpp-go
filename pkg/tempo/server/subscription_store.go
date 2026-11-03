package chargeserver

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/crypto"

	"github.com/tempoxyz/mpp-go/pkg/mpp"
	"github.com/tempoxyz/mpp-go/pkg/tempo"
)

// SubscriptionPayer is the root account that authorized a subscription.
type SubscriptionPayer struct {
	// Address is the payer's address.
	Address string `json:"address"`
	// ChainID is the chain the payer authorized the subscription on.
	ChainID int64 `json:"chainId"`
}

// SubscriptionRecord is a persisted Tempo subscription, as mppx stores it.
type SubscriptionRecord struct {
	AccessKey     *tempo.SubscriptionAccessKey `json:"accessKey,omitempty"`
	Amount        string                       `json:"amount"`
	BillingAnchor string                       `json:"billingAnchor"`
	CanceledAt    string                       `json:"canceledAt,omitempty"`
	ChainID       *int64                       `json:"chainId,omitempty"`
	Currency      string                       `json:"currency"`
	ExternalID    string                       `json:"externalId,omitempty"`
	// Held is set when the payment of Reference was included but did not
	// credit the recipient, as a transfer a receive policy (TIP-1028) holds:
	// the payer's funds went to the policy's guard, so the activation or
	// period counts as paid and is not billed again.
	Held                bool                         `json:"held,omitempty"`
	InFlightAttempt     string                       `json:"inFlightAttempt,omitempty"`
	InFlightPayment     *SubscriptionPendingPayment  `json:"inFlightPayment,omitempty"`
	InFlightPeriod      *int64                       `json:"inFlightPeriod,omitempty"`
	InFlightReference   string                       `json:"inFlightReference,omitempty"`
	InFlightStartedAt   string                       `json:"inFlightStartedAt,omitempty"`
	KeyAuthorization    string                       `json:"keyAuthorization,omitempty"`
	LastChargedPeriod   int64                        `json:"lastChargedPeriod"`
	LookupKey           string                       `json:"lookupKey"`
	Payer               *SubscriptionPayer           `json:"payer,omitempty"`
	PeriodCount         string                       `json:"periodCount"`
	PeriodUnit          tempo.SubscriptionPeriodUnit `json:"periodUnit"`
	Recipient           string                       `json:"recipient"`
	Reference           string                       `json:"reference"`
	RevokedAt           string                       `json:"revokedAt,omitempty"`
	SubscriptionExpires string                       `json:"subscriptionExpires"`
	SubscriptionID      string                       `json:"subscriptionId"`
	Timestamp           string                       `json:"timestamp"`
}

// SubscriptionPendingPayment is a signed automatic billing payment, kept with
// its activation or renewal lock from before it is broadcast until its
// outcome is known, so a later attempt finds whether it was included instead
// of billing the period again with a new nonce.
type SubscriptionPendingPayment struct {
	// Amount is the transfer amount in base units.
	Amount string `json:"amount"`
	// ChainID is the chain the transaction was signed for.
	ChainID int64 `json:"chainId"`
	// Currency is the TIP-20 token transferred.
	Currency string `json:"currency"`
	// Hash is the transaction hash, computed before broadcasting.
	Hash string `json:"hash"`
	// Memo is the transfer's attribution memo.
	Memo string `json:"memo"`
	// Recipient is the payee.
	Recipient string `json:"recipient"`
	// Transaction is the signed, serialized transaction, broadcast again
	// while it is not included.
	Transaction string `json:"transaction"`
	// Unknown is set once the attempt that broadcast it ended without
	// learning its outcome, so the next attempt resolves it at once.
	Unknown bool `json:"unknown,omitempty"`
}

// pendingResolver resolves a pending payment of record: it returns the
// payment's reference once it paid, an error wrapping
// errSubscriptionPaymentFailed once it cannot, and any other error while its
// outcome is still unknown.
type pendingResolver func(payment SubscriptionPendingPayment, record SubscriptionRecord) (string, error)

// SubscriptionAccessKeyRecord is a server-owned access key the subscription
// store keeps for billing.
type SubscriptionAccessKeyRecord struct {
	tempo.SubscriptionAccessKey
	// PrivateKey is the access key's secp256k1 private key, 0x-prefixed hex.
	PrivateKey string `json:"privateKey"`
}

// Default subscription store key prefixes, as mppx's.
const (
	defaultSubscriptionRecordPrefix     = "tempo:subscription:record:"
	defaultSubscriptionKeyPrefix        = "tempo:subscription:key:"
	defaultSubscriptionActivationPrefix = "tempo:subscription:activation:"
	defaultSubscriptionAccessKeyPrefix  = "tempo:subscription:access-key:"
	defaultSubscriptionPendingPrefix    = "tempo:subscription:pending-access-key:"
	defaultSubscriptionCredentialPrefix = "tempo:subscription:credential:"
	defaultSubscriptionLockTimeout      = 15 * time.Minute
	// subscriptionStoreMaxAttempts bounds every compare-and-swap retry loop,
	// so a store that never accepts a swap (or a key under constant
	// contention) fails the request instead of spinning.
	subscriptionStoreMaxAttempts = 64
)

// errSubscriptionStoreContention is returned when a compare-and-swap loop
// runs out of attempts.
var errSubscriptionStoreContention = errors.New("tempo server: subscription store is contended: compare-and-swap kept failing")

// SubscriptionStoreOptions configures a SubscriptionStore.
type SubscriptionStoreOptions struct {
	// KeyPrefix is prepended to every key the store writes.
	KeyPrefix string
	// ActivationTimeout is how long an activation lock lasts before another
	// attempt may replace it, counted from when the activation started or,
	// once its payment settled, from when it started committing. A stale
	// committing lock is finished from its settled record, never paid again.
	// Zero is 15 minutes; negative never expires.
	ActivationTimeout time.Duration
	// RenewalTimeout is how long a renewal lock lasts before another attempt
	// may replace it. Zero is 15 minutes; negative never expires.
	RenewalTimeout time.Duration
	// ExpireCredentialClaims lets a Challenge's activation claim expire once
	// the Challenge can no longer be verified, as the charge intent's
	// ExpireReplayKeys does for replay keys. It requires a store that
	// implements tempo.ExpiringStore; RedisStore's PutIfAbsentUntil needs
	// Redis 6.2 or later (SET NX EXAT). Otherwise claims are kept until
	// released.
	ExpireCredentialClaims bool
}

// SubscriptionStore persists Tempo subscriptions, their server-owned access
// keys and single-use activation claims on an AtomicStore shared by every
// replica, as mppx's subscription Store does.
type SubscriptionStore struct {
	store             tempo.AtomicStore
	prefix            string
	activationTimeout time.Duration
	renewalTimeout    time.Duration
	expireClaims      bool
	now               func() time.Time
}

// NewSubscriptionStore wraps store for subscriptions. It writes every value
// with CompareAndSwap, whose values never expire, so a RedisStore with a TTL
// works too. The one exception is a Challenge's activation claim: with
// ExpireCredentialClaims it lasts only until the Challenge can no longer be
// verified. A claim is released when its attempt paid nothing.
func NewSubscriptionStore(store tempo.AtomicStore, options SubscriptionStoreOptions) (*SubscriptionStore, error) {
	if store == nil {
		return nil, errors.New("tempo server: subscription store requires an AtomicStore")
	}
	if _, ok := store.(tempo.ExpiringStore); options.ExpireCredentialClaims && !ok {
		return nil, errors.New("tempo server: ExpireCredentialClaims requires a store that implements tempo.ExpiringStore")
	}
	activation := options.ActivationTimeout
	if activation == 0 {
		activation = defaultSubscriptionLockTimeout
	}
	renewal := options.RenewalTimeout
	if renewal == 0 {
		renewal = defaultSubscriptionLockTimeout
	}
	return &SubscriptionStore{
		store:             store,
		prefix:            options.KeyPrefix,
		activationTimeout: activation,
		renewalTimeout:    renewal,
		expireClaims:      options.ExpireCredentialClaims,
		now:               time.Now,
	}, nil
}

func (s *SubscriptionStore) recordKey(id string) string {
	return s.prefix + defaultSubscriptionRecordPrefix + id
}
func (s *SubscriptionStore) lookupKey(key string) string {
	return s.prefix + defaultSubscriptionKeyPrefix + key
}
func (s *SubscriptionStore) activationKey(key string) string {
	return s.prefix + defaultSubscriptionActivationPrefix + key
}
func (s *SubscriptionStore) credentialKey(challengeID string) string {
	return s.prefix + defaultSubscriptionCredentialPrefix + challengeID
}

// Segments under the access key prefix: the address index, and escaped
// lookup keys (accessKeyKey).
const (
	subscriptionAccessKeyAddressSegment = "address:"
	subscriptionAccessKeyLookupSegment  = "lookup:"
)

// accessKeyKey is where a lookup key's access key is stored: the access key
// prefix and the lookup key, as mppx's, except that a lookup key starting
// with "address:" would land on an address index entry (accessKeyAddressKey)
// and could overwrite or read another subscriber's key. Such a key, and one
// starting with "lookup:", is stored under "lookup:" + key instead, so no
// lookup key reaches the index and no two share a slot. mppx keeps every
// lookup key as is, so these two kinds alone are not shared with an mppx
// server on the same store.
func (s *SubscriptionStore) accessKeyKey(key string) string {
	if strings.HasPrefix(key, subscriptionAccessKeyAddressSegment) || strings.HasPrefix(key, subscriptionAccessKeyLookupSegment) {
		key = subscriptionAccessKeyLookupSegment + key
	}
	return s.prefix + defaultSubscriptionAccessKeyPrefix + key
}

// accessKeyAddressKey is the address index entry of the access key with
// address, as mppx's.
func (s *SubscriptionStore) accessKeyAddressKey(address string) string {
	return s.prefix + defaultSubscriptionAccessKeyPrefix + subscriptionAccessKeyAddressSegment + strings.ToLower(address)
}
func (s *SubscriptionStore) sealingKeyKey() string {
	return s.prefix + defaultSubscriptionPendingPrefix + "sealing-key"
}

func (s *SubscriptionStore) timestamp() string { return formatTimestamp(s.now()) }

func formatTimestamp(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }

func getJSON[T any](ctx context.Context, store tempo.Store, key string) (*T, string, error) {
	raw, ok, err := store.Get(ctx, key)
	if err != nil || !ok {
		return nil, "", err
	}
	var value T
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		return nil, raw, fmt.Errorf("tempo server: decode %s: %w", key, err)
	}
	return &value, raw, nil
}

// put stores value under key with CompareAndSwap, so it never expires
// whatever TTL the store applies to Put.
func (s *SubscriptionStore) put(ctx context.Context, key, value string) error {
	for range subscriptionStoreMaxAttempts {
		current, ok, err := s.store.Get(ctx, key)
		if err != nil {
			return err
		}
		if ok && current == value {
			return nil
		}
		var old *string
		if ok {
			old = &current
		}
		swapped, err := s.store.CompareAndSwap(ctx, key, old, value)
		if err != nil || swapped {
			return err
		}
	}
	return errSubscriptionStoreContention
}

func (s *SubscriptionStore) putJSON(ctx context.Context, key string, value any) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return s.put(ctx, key, string(encoded))
}

// Get returns the subscription with id, or nil.
func (s *SubscriptionStore) Get(ctx context.Context, id string) (*SubscriptionRecord, error) {
	record, _, err := getJSON[SubscriptionRecord](ctx, s.store, s.recordKey(id))
	return record, err
}

// GetByKey returns the current subscription of a resolved lookup key, or nil.
func (s *SubscriptionStore) GetByKey(ctx context.Context, key string) (*SubscriptionRecord, error) {
	id, ok, err := s.store.Get(ctx, s.lookupKey(key))
	if err != nil || !ok || id == "" {
		return nil, err
	}
	return s.Get(ctx, id)
}

// Put stores record and makes it its lookup key's current subscription.
func (s *SubscriptionStore) Put(ctx context.Context, record SubscriptionRecord) error {
	if err := s.putJSON(ctx, s.recordKey(record.SubscriptionID), record); err != nil {
		return err
	}
	return s.put(ctx, s.lookupKey(record.LookupKey), record.SubscriptionID)
}

// ReleaseCredential releases a Challenge's activation claim so it can be retried.
func (s *SubscriptionStore) ReleaseCredential(ctx context.Context, challengeID string) error {
	return s.store.Delete(ctx, s.credentialKey(challengeID))
}

// GetAccessKey returns the server-owned access key of a lookup key, or nil.
func (s *SubscriptionStore) GetAccessKey(ctx context.Context, key string) (*SubscriptionAccessKeyRecord, error) {
	record, _, err := getJSON[SubscriptionAccessKeyRecord](ctx, s.store, s.accessKeyKey(key))
	return record, err
}

// GetAccessKeyByAddress returns the server-owned access key with address, or
// the one sealed in keyToken when the store has not retained it yet, or nil.
func (s *SubscriptionStore) GetAccessKeyByAddress(ctx context.Context, address, keyToken string) (*SubscriptionAccessKeyRecord, error) {
	record, _, err := getJSON[SubscriptionAccessKeyRecord](ctx, s.store, s.accessKeyAddressKey(address))
	if err != nil || record != nil || keyToken == "" {
		return record, err
	}
	raw, err := hex.DecodeString(strings.TrimPrefix(keyToken, "0x"))
	// 12-byte nonce, 32-byte private key and 16-byte authentication tag.
	if err != nil || len(raw) != 60 {
		return nil, nil
	}
	aead, err := s.sealingCipher(ctx)
	if err != nil {
		return nil, err
	}
	privateKey, err := aead.Open(nil, raw[:12], raw[12:], nil)
	if err != nil {
		return nil, nil
	}
	opened, err := accessKeyRecord(privateKey)
	if err != nil || !strings.EqualFold(opened.AccessKeyAddress, address) {
		return nil, nil
	}
	opened.KeyToken = strings.ToLower(keyToken)
	return opened, nil
}

// GetOrCreateAccessKey returns the server-owned access key of a lookup key,
// creating it on first use.
func (s *SubscriptionStore) GetOrCreateAccessKey(ctx context.Context, key string) (*SubscriptionAccessKeyRecord, error) {
	return s.accessKeyFor(ctx, key, "")
}

// rotateAccessKey replaces the lookup key's server-owned access key with a
// fresh one while it is still retired, the address of a key a subscription
// already authorized. The Tempo keychain refuses to authorize a key ID an
// account registered before (KeyAlreadyExists, KeyAlreadyRevoked), so a new
// subscription needs a new key; the retired one stays reachable by address
// for renewals of the subscription that holds it.
func (s *SubscriptionStore) rotateAccessKey(ctx context.Context, key, retired string) (*SubscriptionAccessKeyRecord, error) {
	return s.accessKeyFor(ctx, key, retired)
}

// accessKeyFor returns the lookup key's access key, creating one when there
// is none or when the current one has address retired.
func (s *SubscriptionStore) accessKeyFor(ctx context.Context, key, retired string) (*SubscriptionAccessKeyRecord, error) {
	for range subscriptionStoreMaxAttempts {
		existing, raw, err := getJSON[SubscriptionAccessKeyRecord](ctx, s.store, s.accessKeyKey(key))
		if err != nil {
			return nil, err
		}
		if existing != nil && (retired == "" || !strings.EqualFold(existing.AccessKeyAddress, retired)) {
			return existing, s.indexAccessKey(ctx, *existing)
		}
		privateKey := make([]byte, 32)
		if _, err := rand.Read(privateKey); err != nil {
			return nil, err
		}
		candidate, err := accessKeyRecord(privateKey)
		if err != nil {
			return nil, err
		}
		// Index the candidate first so a key the lookup slot names is always
		// reachable by address.
		if err := s.indexAccessKey(ctx, *candidate); err != nil {
			return nil, err
		}
		encoded, err := json.Marshal(candidate) //nolint:gosec // G117: the store keeps server-owned access keys to bill with, as mppx's does.
		if err != nil {
			return nil, err
		}
		var old *string
		if existing != nil {
			old = &raw
		}
		stored, err := s.store.CompareAndSwap(ctx, s.accessKeyKey(key), old, string(encoded))
		if err != nil {
			return nil, err
		}
		if stored {
			return candidate, nil
		}
	}
	return nil, errSubscriptionStoreContention
}

// CreateChallengeAccessKey creates an access key sealed into its own
// KeyToken, for a Challenge issued before the subscriber is known, without
// storing anything per Challenge.
func (s *SubscriptionStore) CreateChallengeAccessKey(ctx context.Context) (*SubscriptionAccessKeyRecord, error) {
	privateKey := make([]byte, 32)
	if _, err := rand.Read(privateKey); err != nil {
		return nil, err
	}
	record, err := accessKeyRecord(privateKey)
	if err != nil {
		return nil, err
	}
	aead, err := s.sealingCipher(ctx)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, 12)
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	record.KeyToken = "0x" + hex.EncodeToString(aead.Seal(nonce, nonce, privateKey, nil))
	return record, nil
}

// RetainAccessKey keeps an access key after an activation payment it signed
// was submitted, for later renewals, and makes it the lookup key's.
func (s *SubscriptionStore) RetainAccessKey(ctx context.Context, key string, record SubscriptionAccessKeyRecord) error {
	record.KeyToken = ""
	if err := s.putJSON(ctx, s.accessKeyKey(key), record); err != nil {
		return err
	}
	return s.putJSON(ctx, s.accessKeyAddressKey(record.AccessKeyAddress), record)
}

// retainAccessKeyAddress keeps an access key reachable by its address after
// a renewal it signed, without making it the lookup key's again: the lookup
// key may have rotated to a fresh key for a new subscription.
func (s *SubscriptionStore) retainAccessKeyAddress(ctx context.Context, record SubscriptionAccessKeyRecord) error {
	record.KeyToken = ""
	return s.putJSON(ctx, s.accessKeyAddressKey(record.AccessKeyAddress), record)
}

func (s *SubscriptionStore) indexAccessKey(ctx context.Context, record SubscriptionAccessKeyRecord) error {
	encoded, err := json.Marshal(record) //nolint:gosec // G117: the store keeps server-owned access keys to bill with, as mppx's does.
	if err != nil {
		return err
	}
	_, err = s.store.CompareAndSwap(ctx, s.accessKeyAddressKey(record.AccessKeyAddress), nil, string(encoded))
	return err
}

// sealingCipher returns the AES-GCM cipher sealing challenge access keys,
// with one 32-byte secret shared through the store by every replica.
func (s *SubscriptionStore) sealingCipher(ctx context.Context) (cipher.AEAD, error) {
	candidate := make([]byte, 32)
	if _, err := rand.Read(candidate); err != nil {
		return nil, err
	}
	secretHex := "0x" + hex.EncodeToString(candidate)
	if _, err := s.store.CompareAndSwap(ctx, s.sealingKeyKey(), nil, secretHex); err != nil {
		return nil, err
	}
	current, ok, err := s.store.Get(ctx, s.sealingKeyKey())
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, errors.New("tempo server: subscription sealing key is missing")
	}
	secret, err := hex.DecodeString(strings.TrimPrefix(current, "0x"))
	if err != nil || len(secret) != 32 {
		return nil, errors.New("tempo server: subscription sealing key is invalid")
	}
	block, err := aes.NewCipher(secret)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func accessKeyRecord(privateKey []byte) (*SubscriptionAccessKeyRecord, error) {
	key, err := crypto.ToECDSA(privateKey)
	if err != nil {
		return nil, err
	}
	return &SubscriptionAccessKeyRecord{
		SubscriptionAccessKey: tempo.SubscriptionAccessKey{
			AccessKeyAddress: strings.ToLower(crypto.PubkeyToAddress(key.PublicKey).Hex()),
			KeyType:          tempo.SubscriptionKeyTypeSecp256k1,
		},
		PrivateKey: "0x" + hex.EncodeToString(privateKey),
	}, nil
}

// subscriptionActivationStatus is the outcome of SubscriptionStore.activate.
type subscriptionActivationStatus int

const (
	activationActivated subscriptionActivationStatus = iota
	activationClaimMismatch
	activationExisting
	activationInFlight
	activationReplayed
)

type activationMarker struct {
	ChallengeID  string `json:"challengeId,omitempty"`
	CommittingAt string `json:"committingAt,omitempty"`
	StartedAt    string `json:"startedAt,omitempty"`
	// Payment and Subscription are the activation's signed payment and the
	// subscription it creates, kept until the payment's outcome is known.
	Payment      *SubscriptionPendingPayment `json:"payment,omitempty"`
	Subscription *SubscriptionRecord         `json:"subscription,omitempty"`
	// Settled is the settled subscription a committing activation makes
	// current, written before anything else can fail: a later attempt
	// finishes committing it instead of paying again.
	Settled *SubscriptionRecord `json:"settled,omitempty"`
	// Retry marks a commit that failed, so the next attempt finishes it
	// without waiting for the marker to go stale.
	Retry bool `json:"retry,omitempty"`
}

// activationHooks are the optional parts of SubscriptionStore.activate.
type activationHooks struct {
	// claimUntil, when set, is when the Challenge's claim may expire with
	// ExpireCredentialClaims: past it no Credential for the Challenge verifies.
	claimUntil time.Time
	// resolve resolves a payment an earlier attempt left pending.
	resolve pendingResolver
	// recovered reports an earlier attempt's paid activation that this
	// attempt committed: it is reported here, by the attempt that commits
	// it, and nowhere else.
	recovered func(SubscriptionResult)
}

// activate runs create once for a Challenge and lookup key: it claims the
// Challenge, returns an existing reusable subscription, or takes the lookup
// key's activation lock, creates the subscription and makes it current,
// canceling the one it replaces. A lock left by an earlier attempt is
// finished first: a settled activation is committed, and a payment whose
// outcome is unknown is resolved with hooks.resolve, committing the
// subscription it created when it landed and letting create run only on
// proof it cannot. A recovered activation is reported to hooks.recovered.
func (s *SubscriptionStore) activate(
	ctx context.Context,
	challengeID, lookupKey string,
	isReusable func(SubscriptionRecord) bool,
	create func() (*SubscriptionResult, error),
	hooks *activationHooks,
) (subscriptionActivationStatus, *SubscriptionResult, *SubscriptionRecord, error) {
	if hooks == nil {
		hooks = &activationHooks{}
	}
	claimed, err := s.claimCredential(ctx, challengeID, hooks.claimUntil)
	if err != nil {
		return 0, nil, nil, err
	}
	if !claimed {
		return activationReplayed, nil, nil, nil
	}
	// unclaim releases the Challenge's claim when this attempt ends before
	// its own payment could go out, so the Credential leaves no claim behind
	// and may be retried.
	unclaim := func(status subscriptionActivationStatus, err error) (subscriptionActivationStatus, *SubscriptionResult, *SubscriptionRecord, error) {
		if releaseErr := s.ReleaseCredential(ctx, challengeID); err == nil {
			err = releaseErr
		}
		return status, nil, nil, err
	}
	// A recovered activation is committed first, then the Challenge is
	// checked against the subscription it made current.
	for range 2 {
		existing, err := s.GetByKey(ctx, lookupKey)
		if err != nil {
			return unclaim(0, err)
		}
		if existing != nil && isReusable != nil && isReusable(*existing) {
			return activationExisting, nil, existing, nil
		}

		started, resumed, err := s.startActivation(ctx, lookupKey, challengeID)
		if err != nil {
			return unclaim(0, err)
		}
		if !started {
			return activationInFlight, nil, nil, nil
		}
		var recovered *SubscriptionResult
		switch {
		case resumed == nil:
			if existing, err := s.GetByKey(ctx, lookupKey); err != nil {
				_ = s.clearActivation(ctx, lookupKey, challengeID)
				return unclaim(0, err)
			} else if existing != nil && isReusable != nil && isReusable(*existing) {
				return activationExisting, nil, existing, s.clearActivation(ctx, lookupKey, challengeID)
			}
		case resumed.Settled != nil:
			// An earlier attempt settled and failed to commit: finish its
			// commit rather than paying again.
			record := *resumed.Settled
			recovered = &SubscriptionResult{Receipt: subscriptionReceipt(record), Subscription: record}
		default:
			if hooks.resolve == nil {
				_ = s.markActivationUnknown(ctx, lookupKey, challengeID)
				return unclaim(0, errors.New("tempo server: subscription activation has a pending payment and no resolver"))
			}
			reference, err := hooks.resolve(*resumed.Payment, *resumed.Subscription)
			hash, held := heldPayment(err)
			switch {
			case err == nil, held:
				// A held payment moved the payer's funds too: it is
				// committed as paid, never paid again.
				record := *resumed.Subscription
				record.Reference, record.Held = reference, held
				if held {
					record.Reference = hash
				}
				recovered = &SubscriptionResult{Receipt: subscriptionReceipt(record), Subscription: record}
			case errors.Is(err, errSubscriptionPaymentFailed):
				if _, err := s.updateActivation(ctx, lookupKey, func(marker *activationMarker) *activationMarker {
					if marker == nil || marker.ChallengeID != challengeID {
						return nil
					}
					next := *marker
					next.Payment, next.Subscription = nil, nil
					return &next
				}); err != nil {
					return unclaim(0, err)
				}
			default:
				_ = s.markActivationUnknown(ctx, lookupKey, challengeID)
				return unclaim(0, err)
			}
		}
		if recovered != nil {
			status, result, _, err := s.commitActivation(ctx, challengeID, recovered)
			if err != nil || status != activationActivated {
				return unclaim(status, err)
			}
			if hooks.recovered != nil {
				hooks.recovered(*result)
			}
			continue
		}

		result, err := create()
		if err != nil {
			if errors.Is(err, ErrSubscriptionPaymentNotSent) || errors.Is(err, errSubscriptionPaymentFailed) {
				_ = s.clearActivation(ctx, lookupKey, challengeID)
				return unclaim(0, err)
			}
			// The payment may have gone out: keep the lock and its
			// payment, so the next attempt resolves it instead of
			// paying again.
			_ = s.holdActivation(ctx, lookupKey, challengeID)
			return 0, nil, nil, err
		}
		return s.commitActivation(ctx, challengeID, result)
	}
	return unclaim(activationInFlight, nil)
}

// claimCredential claims a Challenge once. With ExpireCredentialClaims and
// until set, the claim lasts until then, when the Challenge can no longer be
// verified; otherwise it is kept until released.
func (s *SubscriptionStore) claimCredential(ctx context.Context, challengeID string, until time.Time) (bool, error) {
	claim, _ := json.Marshal(map[string]string{"claimedAt": s.timestamp()})
	if expiring, ok := s.store.(tempo.ExpiringStore); ok && s.expireClaims && !until.IsZero() {
		return expiring.PutIfAbsentUntil(ctx, s.credentialKey(challengeID), string(claim), until)
	}
	return s.store.CompareAndSwap(ctx, s.credentialKey(challengeID), nil, string(claim))
}

// commitActivation makes a settled activation's subscription current while
// challengeID holds the lookup key's activation lock. It first saves the
// settled record in the marker, so every later failure leaves it for the
// next attempt to commit, and only that, rather than pay again. Every write
// after that is fenced so that an attempt that lost the lock never
// overwrites what another committed: the record is only ever created, and
// the lookup key and the subscription it replaced change only by
// compare-and-swap on what they hold. It reports activationActivated only to
// the attempt that releases the marker, so a paid activation is reported
// exactly once.
func (s *SubscriptionStore) commitActivation(ctx context.Context, challengeID string, result *SubscriptionResult) (subscriptionActivationStatus, *SubscriptionResult, *SubscriptionRecord, error) {
	subscription := result.Subscription
	lookupKey, id := subscription.LookupKey, subscription.SubscriptionID
	committed, err := s.updateActivation(ctx, lookupKey, func(marker *activationMarker) *activationMarker {
		if marker == nil || marker.ChallengeID != challengeID {
			return nil
		}
		next := *marker
		settled := subscription
		next.Settled = &settled
		// The settled record supersedes the pending payment it came from.
		next.Payment, next.Subscription = nil, nil
		next.Retry = false
		next.CommittingAt = s.timestamp()
		next.StartedAt = next.CommittingAt
		return &next
	})
	if err != nil {
		// The settled record may not be saved. Keep the lock (with a pending
		// payment flagged for the next attempt to resolve) rather than
		// release it and let another attempt pay again.
		_ = s.markActivationUnknown(ctx, lookupKey, challengeID)
		return 0, nil, nil, err
	}
	if !committed {
		if err := s.keepSupersededActivation(ctx, subscription); err != nil {
			return 0, nil, nil, err
		}
		return activationClaimMismatch, nil, nil, nil
	}
	// From here this attempt holds a committing marker with the settled
	// record. The payment went out, so an error keeps the marker, flagged
	// for the next attempt to finish this commit at once.
	fail := func(err error) (subscriptionActivationStatus, *SubscriptionResult, *SubscriptionRecord, error) {
		_, _ = s.updateActivation(ctx, lookupKey, func(marker *activationMarker) *activationMarker {
			if marker == nil || marker.ChallengeID != challengeID {
				return nil
			}
			next := *marker
			next.Retry = true
			return &next
		})
		return 0, nil, nil, err
	}
	encoded, err := json.Marshal(subscription)
	if err != nil {
		return fail(err)
	}
	if err := s.createActivationRecord(ctx, subscription, string(encoded)); err != nil {
		return fail(err)
	}
	for tries := 0; ; tries++ {
		if tries == subscriptionStoreMaxAttempts {
			return fail(errSubscriptionStoreContention)
		}
		current, ok, err := s.store.Get(ctx, s.lookupKey(lookupKey))
		if err != nil {
			return fail(err)
		}
		if ok && current == id {
			break
		}
		if owns, err := s.ownsActivation(ctx, lookupKey, challengeID); err != nil {
			return fail(err)
		} else if !owns {
			// The attempt that took the lock over commits this record.
			return activationClaimMismatch, nil, nil, nil
		}
		// Cancel what the lookup key holds before replacing exactly that.
		if ok && current != "" {
			if err := s.cancelRecord(ctx, current); err != nil {
				return fail(err)
			}
		}
		var old *string
		if ok {
			old = &current
		}
		swapped, err := s.store.CompareAndSwap(ctx, s.lookupKey(lookupKey), old, id)
		if err != nil {
			return fail(err)
		}
		if swapped {
			break
		}
	}
	released, err := s.releaseActivation(ctx, lookupKey, challengeID)
	if err != nil {
		return fail(err)
	}
	if !released {
		// The attempt that took the lock over finishes and reports it.
		return activationClaimMismatch, nil, nil, nil
	}
	return activationActivated, result, nil, nil
}

// createActivationRecord stores a settled activation's record, encoded,
// without ever dropping it or overwriting live state. A record already under
// its ID is kept when it is this same activation's (same BillingAnchor, and
// the same Reference or renewed since), which an earlier attempt of this
// commit created and may have renewed since. Another activation's record,
// such as one a custom Activate that returns a deterministic SubscriptionID
// left, is replaced by compare-and-swap on what was read when it has ended
// (canceled, revoked or expired) or belongs to the same lookup key, whose
// current subscription this activation replaces anyway, and only while no
// renewal is in flight on it. Anything else is an error, so the settled
// record stays in the activation marker for a later attempt to commit.
func (s *SubscriptionStore) createActivationRecord(ctx context.Context, subscription SubscriptionRecord, encoded string) error {
	key := s.recordKey(subscription.SubscriptionID)
	for range subscriptionStoreMaxAttempts {
		existing, raw, err := getJSON[SubscriptionRecord](ctx, s.store, key)
		if err != nil {
			return err
		}
		var old *string
		if existing != nil {
			if existing.BillingAnchor == subscription.BillingAnchor &&
				(existing.Reference == subscription.Reference || existing.LastChargedPeriod > subscription.LastChargedPeriod) {
				return nil
			}
			if existing.InFlightPeriod != nil || existing.InFlightPayment != nil ||
				!s.recordEnded(*existing) && existing.LookupKey != subscription.LookupKey {
				return fmt.Errorf("tempo server: subscription %s already holds another activation; the settled activation is kept for a later attempt", subscription.SubscriptionID)
			}
			old = &raw
		}
		swapped, err := s.store.CompareAndSwap(ctx, key, old, encoded)
		if err != nil || swapped {
			return err
		}
	}
	return errSubscriptionStoreContention
}

// recordEnded reports whether a subscription no longer bills: it was
// canceled or revoked, or it expired.
func (s *SubscriptionStore) recordEnded(record SubscriptionRecord) bool {
	if record.CanceledAt != "" || record.RevokedAt != "" {
		return true
	}
	expires, err := time.Parse(time.RFC3339Nano, record.SubscriptionExpires)
	return err == nil && !expires.After(s.now())
}

// keepSupersededActivation keeps the paid subscription of an attempt that
// lost the activation lock. The attempt that took the lock over commits it
// when the lock carries it; otherwise it is stored canceled, and only when
// no record has that ID: never over one another attempt committed.
func (s *SubscriptionStore) keepSupersededActivation(ctx context.Context, subscription SubscriptionRecord) error {
	marker, _, err := getJSON[activationMarker](ctx, s.store, s.activationKey(subscription.LookupKey))
	if err != nil {
		return err
	}
	if marker != nil && (marker.Settled != nil && marker.Settled.SubscriptionID == subscription.SubscriptionID ||
		marker.Subscription != nil && marker.Subscription.SubscriptionID == subscription.SubscriptionID) {
		return nil
	}
	canceled := subscription
	if canceled.CanceledAt == "" {
		canceled.CanceledAt = s.timestamp()
	}
	encoded, err := json.Marshal(canceled)
	if err != nil {
		return err
	}
	_, err = s.store.CompareAndSwap(ctx, s.recordKey(canceled.SubscriptionID), nil, string(encoded))
	return err
}

// cancelRecord marks the subscription with id canceled by compare-and-swap
// on its current value, so a renewal committed meanwhile is kept.
func (s *SubscriptionStore) cancelRecord(ctx context.Context, id string) error {
	for range subscriptionStoreMaxAttempts {
		current, raw, err := getJSON[SubscriptionRecord](ctx, s.store, s.recordKey(id))
		if err != nil {
			return err
		}
		if current == nil || current.CanceledAt != "" {
			return nil
		}
		next := *current
		next.CanceledAt = s.timestamp()
		encoded, err := json.Marshal(next)
		if err != nil {
			return err
		}
		swapped, err := s.store.CompareAndSwap(ctx, s.recordKey(id), &raw, string(encoded))
		if err != nil || swapped {
			return err
		}
	}
	return errSubscriptionStoreContention
}

// startActivation takes the lookup key's activation lock unless a live one
// holds it, and returns the marker it then holds when it took over an
// earlier attempt's lock to finish. A settled activation is only ever
// committed, never paid again: its stale or failed commit is resumed with
// its settled record, and a live one holds the lock. A payment whose outcome
// is unknown, or a stale one, is resumed to be resolved.
func (s *SubscriptionStore) startActivation(ctx context.Context, lookupKey, challengeID string) (bool, *activationMarker, error) {
	var resumed *activationMarker
	started, err := s.updateActivation(ctx, lookupKey, func(marker *activationMarker) *activationMarker {
		resumed = nil
		stale := marker == nil || s.isStale(marker.StartedAt, marker.CommittingAt, s.activationTimeout)
		switch {
		case marker != nil && marker.Settled != nil:
			if !stale && !marker.Retry {
				return nil
			}
			resumed = &activationMarker{ChallengeID: challengeID, StartedAt: s.timestamp(), Settled: marker.Settled}
			return resumed
		case marker != nil && marker.Payment != nil && marker.Subscription != nil:
			if !stale && !marker.Payment.Unknown {
				return nil
			}
			payment := *marker.Payment
			payment.Unknown = false
			resumed = &activationMarker{ChallengeID: challengeID, StartedAt: s.timestamp(), Payment: &payment, Subscription: marker.Subscription}
			return resumed
		case !stale:
			return nil
		}
		return &activationMarker{ChallengeID: challengeID, StartedAt: s.timestamp()}
	})
	return started, resumed, err
}

// recordActivationPayment keeps payment and the subscription it creates with
// challengeID's activation lock, before the payment is broadcast.
func (s *SubscriptionStore) recordActivationPayment(ctx context.Context, lookupKey, challengeID string, payment SubscriptionPendingPayment, record SubscriptionRecord) error {
	recorded, err := s.updateActivation(ctx, lookupKey, func(marker *activationMarker) *activationMarker {
		if marker == nil || marker.ChallengeID != challengeID {
			return nil
		}
		next := *marker
		next.Payment, next.Subscription = &payment, &record
		return &next
	})
	if err != nil {
		return err
	}
	if !recorded {
		return mpp.ErrVerificationFailed("subscription activation lock was lost")
	}
	return nil
}

// markActivationUnknown keeps challengeID's activation lock for the next
// attempt to finish at once, without releasing it: a settled record is
// flagged to commit, and a pending payment to resolve. A lock with neither
// stays until it goes stale.
func (s *SubscriptionStore) markActivationUnknown(ctx context.Context, lookupKey, challengeID string) error {
	_, err := s.updateActivation(ctx, lookupKey, func(marker *activationMarker) *activationMarker {
		return flagActivation(marker, challengeID)
	})
	return err
}

// holdActivation keeps challengeID's activation lock after a failure that
// may follow a payment, as markActivationUnknown, and releases a lock that
// has neither a settled record nor a recorded payment.
func (s *SubscriptionStore) holdActivation(ctx context.Context, lookupKey, challengeID string) error {
	held, err := s.updateActivation(ctx, lookupKey, func(marker *activationMarker) *activationMarker {
		return flagActivation(marker, challengeID)
	})
	if err != nil || held {
		return err
	}
	return s.clearActivation(ctx, lookupKey, challengeID)
}

// flagActivation returns challengeID's marker flagged for the next attempt
// to finish, or nil when it is not challengeID's or has nothing to finish.
func flagActivation(marker *activationMarker, challengeID string) *activationMarker {
	if marker == nil || marker.ChallengeID != challengeID {
		return nil
	}
	next := *marker
	switch {
	case marker.Settled != nil:
		next.Retry = true
	case marker.Payment != nil:
		payment := *marker.Payment
		payment.Unknown = true
		next.Payment = &payment
	default:
		return nil
	}
	return &next
}

// updateActivation atomically replaces the activation marker with next(current);
// a nil next leaves it unchanged and reports false.
func (s *SubscriptionStore) updateActivation(ctx context.Context, lookupKey string, next func(*activationMarker) *activationMarker) (bool, error) {
	key := s.activationKey(lookupKey)
	for range subscriptionStoreMaxAttempts {
		marker, raw, err := getJSON[activationMarker](ctx, s.store, key)
		if err != nil {
			return false, err
		}
		updated := next(marker)
		if updated == nil {
			return false, nil
		}
		encoded, err := json.Marshal(updated)
		if err != nil {
			return false, err
		}
		var old *string
		if marker != nil {
			old = &raw
		}
		swapped, err := s.store.CompareAndSwap(ctx, key, old, string(encoded))
		if err != nil {
			return false, err
		}
		if swapped {
			return true, nil
		}
	}
	return false, errSubscriptionStoreContention
}

func (s *SubscriptionStore) clearActivation(ctx context.Context, lookupKey, challengeID string) error {
	_, err := s.releaseActivation(ctx, lookupKey, challengeID)
	return err
}

// releaseActivation deletes challengeID's activation marker, and reports
// whether this call deleted it.
func (s *SubscriptionStore) releaseActivation(ctx context.Context, lookupKey, challengeID string) (bool, error) {
	key := s.activationKey(lookupKey)
	marker, raw, err := getJSON[activationMarker](ctx, s.store, key)
	if err != nil || marker == nil || marker.ChallengeID != challengeID {
		return false, err
	}
	return s.store.CompareAndDelete(ctx, key, raw)
}

func (s *SubscriptionStore) ownsActivation(ctx context.Context, lookupKey, challengeID string) (bool, error) {
	marker, _, err := getJSON[activationMarker](ctx, s.store, s.activationKey(lookupKey))
	if err != nil {
		return false, err
	}
	return marker != nil && marker.ChallengeID == challengeID, nil
}

// isStale reports whether a lock started at startedAt, or committing since
// committingAt, has outlived timeout. Unlike mppx, a committing activation
// goes stale too, counted from when it started committing: committing only
// writes the store, so a marker still there after the timeout belongs to a
// replica that crashed or lost the store, and would otherwise lock the
// lookup key out for good.
func (s *SubscriptionStore) isStale(startedAt, committingAt string, timeout time.Duration) bool {
	if timeout < 0 {
		return false
	}
	if committingAt != "" {
		startedAt = committingAt
	}
	started, err := time.Parse(time.RFC3339Nano, startedAt)
	if err != nil {
		return true
	}
	return s.now().Sub(started) >= timeout
}

// subscriptionRenewalStatus is the outcome of SubscriptionStore.renew.
type subscriptionRenewalStatus int

const (
	renewalRenewed subscriptionRenewalStatus = iota
	renewalCharged
	renewalInFlight
	renewalMissing
	renewalSuperseded
	renewalClaimMismatch
)

// renewalHooks are the optional parts of SubscriptionStore.renew.
type renewalHooks struct {
	// resolve resolves a payment an earlier attempt left pending.
	resolve pendingResolver
	// recovered reports an earlier attempt's paid period that this attempt
	// committed: it is reported here, by the attempt that commits it, and
	// nowhere else.
	recovered func(periodIndex int64, result SubscriptionResult)
}

// renew bills period periodIndex of subscription id once: it locks the
// period on the record, runs charge, and commits its record while the lock
// is still this attempt's. A lock left with a payment whose outcome is
// unknown is resolved first with hooks.resolve: a payment that landed
// commits its period, reported to hooks.recovered, and only proof that it
// cannot land frees the period. A paid period is committed even when the
// lookup key moved to another subscription meanwhile, and returned with
// renewalSuperseded.
func (s *SubscriptionStore) renew(
	ctx context.Context,
	id string,
	periodIndex int64,
	inFlightReference string,
	charge func(SubscriptionRecord) (*SubscriptionResult, error),
	hooks *renewalHooks,
) (subscriptionRenewalStatus, *SubscriptionResult, *SubscriptionRecord, error) {
	if hooks == nil {
		hooks = &renewalHooks{}
	}
	attemptBytes := make([]byte, 16)
	if _, err := rand.Read(attemptBytes); err != nil {
		return 0, nil, nil, err
	}
	attempt := hex.EncodeToString(attemptBytes)
	// A resolved payment frees or commits its period, then the requested
	// period is billed.
	for range 3 {
		var started SubscriptionRecord
		resumed := false
		for tries := 0; ; tries++ {
			if tries == subscriptionStoreMaxAttempts {
				return 0, nil, nil, errSubscriptionStoreContention
			}
			current, raw, err := getJSON[SubscriptionRecord](ctx, s.store, s.recordKey(id))
			if err != nil {
				return 0, nil, nil, err
			}
			if current == nil {
				return renewalMissing, nil, nil, nil
			}
			if current.LastChargedPeriod >= periodIndex {
				return renewalCharged, nil, current, nil
			}
			next := *current
			resumed = false
			if current.InFlightPeriod != nil {
				stale := s.isStale(current.InFlightStartedAt, "", s.renewalTimeout)
				if payment := current.InFlightPayment; payment != nil && (stale || payment.Unknown) {
					// Keep the period, reference and payment: only their
					// outcome decides whether the period is paid.
					resumed = true
					resumedPayment := *payment
					resumedPayment.Unknown = false
					next.InFlightPayment = &resumedPayment
				} else if !stale {
					return renewalInFlight, nil, current, nil
				}
			}
			if !resumed {
				// A subscription canceled or revoked before this claim must
				// not be billed again; a payment already sent is still
				// resolved above.
				if current.CanceledAt != "" || current.RevokedAt != "" {
					return renewalMissing, nil, nil, nil
				}
				period := periodIndex
				next.InFlightPayment = nil
				next.InFlightPeriod = &period
				next.InFlightReference = inFlightReference
			}
			next.InFlightAttempt = attempt
			next.InFlightStartedAt = s.timestamp()
			encoded, err := json.Marshal(next)
			if err != nil {
				return 0, nil, nil, err
			}
			swapped, err := s.store.CompareAndSwap(ctx, s.recordKey(id), &raw, string(encoded))
			if err != nil {
				return 0, nil, nil, err
			}
			if swapped {
				started = next
				break
			}
		}

		if resumed {
			period := *started.InFlightPeriod
			if hooks.resolve == nil {
				_ = s.markRenewalUnknown(ctx, id, period, attempt)
				return 0, nil, nil, errors.New("tempo server: subscription renewal has a pending payment and no resolver")
			}
			reference, err := hooks.resolve(*started.InFlightPayment, started)
			hash, held := heldPayment(err)
			switch {
			case err == nil, held:
				// A held payment moved the payer's funds too: its period
				// is committed as paid, never billed again.
				record := clearRenewalFields(started)
				record.LastChargedPeriod = period
				record.Reference, record.Held = reference, held
				if held {
					record.Reference = hash
				}
				record.Timestamp = s.timestamp()
				status, result, _, err := s.commitRenewal(ctx, id, period, attempt, &SubscriptionResult{Receipt: subscriptionReceipt(record), Subscription: record})
				if err != nil || status == renewalClaimMismatch {
					return status, nil, nil, err
				}
				if period == periodIndex {
					// The requested period itself: the caller reports it.
					return status, result, nil, nil
				}
				if hooks.recovered != nil {
					hooks.recovered(period, *result)
				}
				if status == renewalSuperseded {
					return renewalSuperseded, nil, &result.Subscription, nil
				}
				if period > periodIndex {
					return renewalCharged, nil, &result.Subscription, nil
				}
			case errors.Is(err, errSubscriptionPaymentFailed):
				if err := s.clearRenewal(ctx, id, period, attempt); err != nil {
					return 0, nil, nil, err
				}
			default:
				_ = s.markRenewalUnknown(ctx, id, period, attempt)
				return 0, nil, nil, err
			}
			continue
		}

		if active, err := s.GetByKey(ctx, started.LookupKey); err != nil || active == nil || active.SubscriptionID != id {
			clearErr := s.clearRenewal(ctx, id, periodIndex, attempt)
			if err == nil {
				err = clearErr
			}
			return renewalSuperseded, nil, &started, err
		}

		result, err := charge(started)
		if err != nil {
			if errors.Is(err, ErrSubscriptionPaymentNotSent) || errors.Is(err, errSubscriptionPaymentFailed) {
				_ = s.clearRenewal(ctx, id, periodIndex, attempt)
			} else {
				// The payment may have gone out: keep the lock and its
				// payment, so the next attempt resolves it instead of
				// paying again.
				_ = s.holdRenewal(ctx, id, periodIndex, attempt)
			}
			return 0, nil, nil, err
		}
		committed := result.Subscription
		committed.LastChargedPeriod = periodIndex
		return s.commitRenewal(ctx, id, periodIndex, attempt, &SubscriptionResult{Receipt: result.Receipt, Subscription: committed})
	}
	return renewalInFlight, nil, nil, nil
}

// commitRenewal writes the settled record of periodIndex while attempt still
// holds the period's lock, whatever the lookup key holds: a paid period is
// always recorded. It returns renewalSuperseded, with the committed result,
// when the lookup key no longer names the subscription.
func (s *SubscriptionStore) commitRenewal(ctx context.Context, id string, periodIndex int64, attempt string, result *SubscriptionResult) (subscriptionRenewalStatus, *SubscriptionResult, *SubscriptionRecord, error) {
	var committed SubscriptionRecord
	for tries := 0; ; tries++ {
		if tries == subscriptionStoreMaxAttempts {
			// The period is paid: keep its payment for the next attempt.
			_ = s.holdRenewal(ctx, id, periodIndex, attempt)
			return 0, nil, nil, errSubscriptionStoreContention
		}
		existing, raw, err := getJSON[SubscriptionRecord](ctx, s.store, s.recordKey(id))
		if err != nil {
			_ = s.holdRenewal(ctx, id, periodIndex, attempt)
			return 0, nil, nil, err
		}
		if existing == nil || existing.InFlightPeriod == nil || *existing.InFlightPeriod != periodIndex || existing.InFlightAttempt != attempt {
			return renewalClaimMismatch, nil, nil, nil
		}
		committed = clearRenewalFields(result.Subscription)
		committed.LastChargedPeriod = periodIndex
		committed.SubscriptionID = id
		if existing.CanceledAt != "" {
			committed.CanceledAt = existing.CanceledAt
		}
		if existing.RevokedAt != "" {
			committed.RevokedAt = existing.RevokedAt
		}
		encoded, err := json.Marshal(committed)
		if err != nil {
			return 0, nil, nil, err
		}
		swapped, err := s.store.CompareAndSwap(ctx, s.recordKey(id), &raw, string(encoded))
		if err != nil {
			_ = s.holdRenewal(ctx, id, periodIndex, attempt)
			return 0, nil, nil, err
		}
		if swapped {
			break
		}
	}
	out := &SubscriptionResult{Receipt: result.Receipt, Subscription: committed}
	// The period is committed: a lookup key read that fails reports it as
	// renewed, and the next request checks the lookup key again.
	if current, ok, err := s.store.Get(ctx, s.lookupKey(committed.LookupKey)); err == nil && (!ok || current != id) {
		return renewalSuperseded, out, nil, nil
	}
	return renewalRenewed, out, nil, nil
}

// recordRenewalPayment keeps payment with attempt's lock on periodIndex,
// before the payment is broadcast.
func (s *SubscriptionStore) recordRenewalPayment(ctx context.Context, id string, periodIndex int64, attempt string, payment SubscriptionPendingPayment) error {
	recorded, err := s.updateRenewal(ctx, id, periodIndex, attempt, func(record *SubscriptionRecord) {
		record.InFlightPayment = &payment
	})
	if err != nil {
		return err
	}
	if !recorded {
		return mpp.ErrVerificationFailed("subscription renewal lock was lost")
	}
	return nil
}

// markRenewalUnknown flags attempt's pending renewal payment as unknown, so
// the next attempt resolves it without waiting for the lock to go stale.
func (s *SubscriptionStore) markRenewalUnknown(ctx context.Context, id string, periodIndex int64, attempt string) error {
	_, err := s.updateRenewal(ctx, id, periodIndex, attempt, func(record *SubscriptionRecord) {
		if record.InFlightPayment != nil {
			payment := *record.InFlightPayment
			payment.Unknown = true
			record.InFlightPayment = &payment
		}
	})
	return err
}

// holdRenewal keeps attempt's lock on periodIndex after a failure that may
// follow a payment: a recorded payment is flagged unknown, for the next
// attempt to resolve; a lock without one is released, and a custom Renew
// runs again with the same InFlightReference.
func (s *SubscriptionStore) holdRenewal(ctx context.Context, id string, periodIndex int64, attempt string) error {
	held := false
	if _, err := s.updateRenewal(ctx, id, periodIndex, attempt, func(record *SubscriptionRecord) {
		if record.InFlightPayment != nil {
			held = true
			payment := *record.InFlightPayment
			payment.Unknown = true
			record.InFlightPayment = &payment
		}
	}); err != nil || held {
		return err
	}
	return s.clearRenewal(ctx, id, periodIndex, attempt)
}

// updateRenewal applies update to the record while attempt holds its lock on
// periodIndex, and reports whether it did.
func (s *SubscriptionStore) updateRenewal(ctx context.Context, id string, periodIndex int64, attempt string, update func(*SubscriptionRecord)) (bool, error) {
	for range subscriptionStoreMaxAttempts {
		current, raw, err := getJSON[SubscriptionRecord](ctx, s.store, s.recordKey(id))
		if err != nil {
			return false, err
		}
		if current == nil || current.InFlightPeriod == nil || *current.InFlightPeriod != periodIndex || current.InFlightAttempt != attempt {
			return false, nil
		}
		next := *current
		update(&next)
		encoded, err := json.Marshal(next)
		if err != nil {
			return false, err
		}
		swapped, err := s.store.CompareAndSwap(ctx, s.recordKey(id), &raw, string(encoded))
		if err != nil {
			return false, err
		}
		if swapped {
			return true, nil
		}
	}
	return false, errSubscriptionStoreContention
}

func (s *SubscriptionStore) clearRenewal(ctx context.Context, id string, periodIndex int64, attempt string) error {
	for range subscriptionStoreMaxAttempts {
		current, raw, err := getJSON[SubscriptionRecord](ctx, s.store, s.recordKey(id))
		if err != nil {
			return err
		}
		if current == nil || current.InFlightPeriod == nil || *current.InFlightPeriod != periodIndex || current.InFlightAttempt != attempt {
			return nil
		}
		encoded, err := json.Marshal(clearRenewalFields(*current))
		if err != nil {
			return err
		}
		swapped, err := s.store.CompareAndSwap(ctx, s.recordKey(id), &raw, string(encoded))
		if err != nil || swapped {
			return err
		}
	}
	return errSubscriptionStoreContention
}

func clearRenewalFields(record SubscriptionRecord) SubscriptionRecord {
	record.InFlightAttempt = ""
	record.InFlightPayment = nil
	record.InFlightPeriod = nil
	record.InFlightReference = ""
	record.InFlightStartedAt = ""
	return record
}
