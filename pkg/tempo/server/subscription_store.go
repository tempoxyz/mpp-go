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
	AccessKey           *tempo.SubscriptionAccessKey `json:"accessKey,omitempty"`
	Amount              string                       `json:"amount"`
	BillingAnchor       string                       `json:"billingAnchor"`
	CanceledAt          string                       `json:"canceledAt,omitempty"`
	ChainID             *int64                       `json:"chainId,omitempty"`
	Currency            string                       `json:"currency"`
	ExternalID          string                       `json:"externalId,omitempty"`
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
	// once its payment settled, from when it started committing. Zero is 15
	// minutes; negative never expires.
	ActivationTimeout time.Duration
	// RenewalTimeout is how long a renewal lock lasts before another attempt
	// may replace it. Zero is 15 minutes; negative never expires.
	RenewalTimeout time.Duration
}

// SubscriptionStore persists Tempo subscriptions, their server-owned access
// keys and single-use activation claims on an AtomicStore shared by every
// replica, as mppx's subscription Store does.
type SubscriptionStore struct {
	store             tempo.AtomicStore
	prefix            string
	activationTimeout time.Duration
	renewalTimeout    time.Duration
	now               func() time.Time
}

// NewSubscriptionStore wraps store for subscriptions. It writes every value
// with CompareAndSwap, whose values never expire, so a RedisStore with a TTL
// works too.
func NewSubscriptionStore(store tempo.AtomicStore, options SubscriptionStoreOptions) (*SubscriptionStore, error) {
	if store == nil {
		return nil, errors.New("tempo server: subscription store requires an AtomicStore")
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
func (s *SubscriptionStore) accessKeyKey(key string) string {
	return s.prefix + defaultSubscriptionAccessKeyPrefix + key
}
func (s *SubscriptionStore) accessKeyAddressKey(address string) string {
	return s.prefix + defaultSubscriptionAccessKeyPrefix + "address:" + strings.ToLower(address)
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
}

// activate runs create once for a Challenge and lookup key: it claims the
// Challenge, returns an existing reusable subscription, or takes the lookup
// key's activation lock, creates the subscription and makes it current,
// canceling the one it replaces. A lock left with a payment whose outcome is
// unknown is resolved first with resolve: a payment that landed commits the
// subscription it created, and one that did not lets create run.
func (s *SubscriptionStore) activate(
	ctx context.Context,
	challengeID, lookupKey string,
	isReusable func(SubscriptionRecord) bool,
	create func() (*SubscriptionResult, error),
	resolve pendingResolver,
) (subscriptionActivationStatus, *SubscriptionResult, *SubscriptionRecord, error) {
	claim, _ := json.Marshal(map[string]string{"claimedAt": s.timestamp()})
	claimed, err := s.store.CompareAndSwap(ctx, s.credentialKey(challengeID), nil, string(claim))
	if err != nil {
		return 0, nil, nil, err
	}
	if !claimed {
		return activationReplayed, nil, nil, nil
	}
	// A recovered activation is committed first, then the Challenge is
	// checked against the subscription it made current.
	for range 2 {
		existing, err := s.GetByKey(ctx, lookupKey)
		if err != nil {
			return 0, nil, nil, err
		}
		if existing != nil && isReusable != nil && isReusable(*existing) {
			return activationExisting, nil, existing, nil
		}

		started, resumed, err := s.startActivation(ctx, lookupKey, challengeID)
		if err != nil {
			return 0, nil, nil, err
		}
		if !started {
			return activationInFlight, nil, nil, nil
		}
		if resumed == nil {
			if existing, err := s.GetByKey(ctx, lookupKey); err != nil {
				_ = s.clearActivation(ctx, lookupKey, challengeID)
				return 0, nil, nil, err
			} else if existing != nil && isReusable != nil && isReusable(*existing) {
				return activationExisting, nil, existing, s.clearActivation(ctx, lookupKey, challengeID)
			}
		} else {
			if resolve == nil {
				return 0, nil, nil, errors.New("tempo server: subscription activation has a pending payment and no resolver")
			}
			reference, err := resolve(*resumed.Payment, *resumed.Subscription)
			switch {
			case err == nil:
				record := *resumed.Subscription
				record.Reference = reference
				status, result, current, err := s.commitActivation(ctx, challengeID, &SubscriptionResult{Receipt: subscriptionReceipt(record), Subscription: record})
				if err != nil || status != activationActivated {
					return status, result, current, err
				}
				continue
			case errors.Is(err, errSubscriptionPaymentFailed):
				if _, err := s.updateActivation(ctx, lookupKey, func(marker *activationMarker) *activationMarker {
					if marker == nil || marker.ChallengeID != challengeID {
						return nil
					}
					next := *marker
					next.Payment, next.Subscription = nil, nil
					return &next
				}); err != nil {
					return 0, nil, nil, err
				}
			default:
				_ = s.markActivationUnknown(ctx, lookupKey, challengeID)
				return 0, nil, nil, err
			}
		}

		result, err := create()
		if err != nil {
			if errors.Is(err, errSubscriptionPaymentUnknown) {
				// The payment may land: keep the lock and the payment, so
				// the next attempt resolves it instead of paying again.
				_ = s.markActivationUnknown(ctx, lookupKey, challengeID)
			} else {
				_ = s.clearActivation(ctx, lookupKey, challengeID)
			}
			return 0, nil, nil, err
		}
		return s.commitActivation(ctx, challengeID, result)
	}
	return activationInFlight, nil, nil, nil
}

// commitActivation makes the subscription of a settled activation current
// while challengeID still holds the lookup key's activation lock.
func (s *SubscriptionStore) commitActivation(ctx context.Context, challengeID string, result *SubscriptionResult) (subscriptionActivationStatus, *SubscriptionResult, *SubscriptionRecord, error) {
	subscription := result.Subscription
	committed, err := s.updateActivation(ctx, subscription.LookupKey, func(marker *activationMarker) *activationMarker {
		if marker == nil || marker.ChallengeID != challengeID {
			return nil
		}
		next := *marker
		next.CommittingAt = s.timestamp()
		next.StartedAt = s.timestamp()
		return &next
	})
	if err != nil {
		_ = s.clearActivation(ctx, subscription.LookupKey, challengeID)
		return 0, nil, nil, err
	}
	if !committed {
		canceled := subscription
		if canceled.CanceledAt == "" {
			canceled.CanceledAt = s.timestamp()
		}
		if err := s.putJSON(ctx, s.recordKey(canceled.SubscriptionID), canceled); err != nil {
			return 0, nil, nil, err
		}
		return activationClaimMismatch, nil, nil, nil
	}
	// From here this attempt holds a committing marker: release it on every
	// error, so a failed commit does not lock the lookup key out until the
	// marker goes stale.
	fail := func(err error) (subscriptionActivationStatus, *SubscriptionResult, *SubscriptionRecord, error) {
		_ = s.clearActivation(ctx, subscription.LookupKey, challengeID)
		return 0, nil, nil, err
	}
	previous, err := s.GetByKey(ctx, subscription.LookupKey)
	if err != nil {
		return fail(err)
	}
	if previous != nil && previous.SubscriptionID != subscription.SubscriptionID {
		if owns, err := s.ownsActivation(ctx, subscription.LookupKey, challengeID); err != nil {
			return fail(err)
		} else if !owns {
			return activationClaimMismatch, nil, nil, nil
		}
		if previous.CanceledAt == "" {
			previous.CanceledAt = s.timestamp()
		}
		if err := s.putJSON(ctx, s.recordKey(previous.SubscriptionID), previous); err != nil {
			return fail(err)
		}
	}
	if owns, err := s.ownsActivation(ctx, subscription.LookupKey, challengeID); err != nil {
		return fail(err)
	} else if !owns {
		return activationClaimMismatch, nil, nil, nil
	}
	if err := s.putJSON(ctx, s.recordKey(subscription.SubscriptionID), subscription); err != nil {
		return fail(err)
	}
	if owns, err := s.ownsActivation(ctx, subscription.LookupKey, challengeID); err != nil {
		return fail(err)
	} else if !owns {
		return activationClaimMismatch, nil, nil, nil
	}
	if err := s.put(ctx, s.lookupKey(subscription.LookupKey), subscription.SubscriptionID); err != nil {
		return fail(err)
	}
	if err := s.clearActivation(ctx, subscription.LookupKey, challengeID); err != nil {
		return 0, nil, nil, err
	}
	return activationActivated, result, nil, nil
}

// startActivation takes the lookup key's activation lock unless a live one
// holds it. It takes over a lock whose payment's outcome is unknown, or a
// stale one, and returns the marker it then holds when that lock carried a
// payment to resolve.
func (s *SubscriptionStore) startActivation(ctx context.Context, lookupKey, challengeID string) (bool, *activationMarker, error) {
	var resumed *activationMarker
	started, err := s.updateActivation(ctx, lookupKey, func(marker *activationMarker) *activationMarker {
		resumed = nil
		stale := marker == nil || s.isStale(marker.StartedAt, marker.CommittingAt, s.activationTimeout)
		if marker != nil && marker.Payment != nil && marker.Subscription != nil && (stale || marker.Payment.Unknown) {
			payment := *marker.Payment
			payment.Unknown = false
			resumed = &activationMarker{ChallengeID: challengeID, StartedAt: s.timestamp(), Payment: &payment, Subscription: marker.Subscription}
			return resumed
		}
		if !stale {
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

// markActivationUnknown flags challengeID's pending activation payment as
// unknown, so the next attempt resolves it without waiting for the lock to
// go stale.
func (s *SubscriptionStore) markActivationUnknown(ctx context.Context, lookupKey, challengeID string) error {
	_, err := s.updateActivation(ctx, lookupKey, func(marker *activationMarker) *activationMarker {
		if marker == nil || marker.ChallengeID != challengeID || marker.Payment == nil {
			return nil
		}
		next := *marker
		payment := *marker.Payment
		payment.Unknown = true
		next.Payment = &payment
		return &next
	})
	return err
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
	key := s.activationKey(lookupKey)
	marker, raw, err := getJSON[activationMarker](ctx, s.store, key)
	if err != nil || marker == nil || marker.ChallengeID != challengeID {
		return err
	}
	_, err = s.store.CompareAndDelete(ctx, key, raw)
	return err
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

// renew bills period periodIndex of subscription id once: it locks the
// period on the record, runs charge, and commits its record while the lock
// and the lookup key are still the subscription's. A lock left with a
// payment whose outcome is unknown is resolved first with resolve: a payment
// that landed commits its period, and one that did not frees it.
func (s *SubscriptionStore) renew(
	ctx context.Context,
	id string,
	periodIndex int64,
	inFlightReference string,
	charge func(SubscriptionRecord) (*SubscriptionResult, error),
	resolve pendingResolver,
) (subscriptionRenewalStatus, *SubscriptionResult, *SubscriptionRecord, error) {
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
			if resolve == nil {
				return 0, nil, nil, errors.New("tempo server: subscription renewal has a pending payment and no resolver")
			}
			reference, err := resolve(*started.InFlightPayment, started)
			switch {
			case err == nil:
				record := clearRenewalFields(started)
				record.LastChargedPeriod = period
				record.Reference = reference
				record.Timestamp = s.timestamp()
				status, result, current, err := s.commitRenewal(ctx, id, period, attempt, &SubscriptionResult{Receipt: subscriptionReceipt(record), Subscription: record})
				if err != nil || status != renewalRenewed || period >= periodIndex {
					return status, result, current, err
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
			if errors.Is(err, errSubscriptionPaymentUnknown) {
				// The payment may land: keep the lock and the payment, so
				// the next attempt resolves it instead of paying again.
				_ = s.markRenewalUnknown(ctx, id, periodIndex, attempt)
			} else {
				_ = s.clearRenewal(ctx, id, periodIndex, attempt)
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
// holds the period's lock, and reports whether the lookup key is still the
// subscription's.
func (s *SubscriptionStore) commitRenewal(ctx context.Context, id string, periodIndex int64, attempt string, result *SubscriptionResult) (subscriptionRenewalStatus, *SubscriptionResult, *SubscriptionRecord, error) {
	lookupKey := result.Subscription.LookupKey
	if active, err := s.GetByKey(ctx, lookupKey); err != nil || active == nil || active.SubscriptionID != id {
		clearErr := s.clearRenewal(ctx, id, periodIndex, attempt)
		if err == nil {
			err = clearErr
		}
		return renewalSuperseded, nil, nil, err
	}
	for tries := 0; ; tries++ {
		if tries == subscriptionStoreMaxAttempts {
			_ = s.clearRenewal(ctx, id, periodIndex, attempt)
			return 0, nil, nil, errSubscriptionStoreContention
		}
		existing, raw, err := getJSON[SubscriptionRecord](ctx, s.store, s.recordKey(id))
		if err != nil {
			return 0, nil, nil, err
		}
		if existing == nil || existing.InFlightPeriod == nil || *existing.InFlightPeriod != periodIndex || existing.InFlightAttempt != attempt {
			return renewalClaimMismatch, nil, nil, nil
		}
		committed := clearRenewalFields(result.Subscription)
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
			return 0, nil, nil, err
		}
		if swapped {
			break
		}
	}
	owns, err := s.store.CompareAndSwap(ctx, s.lookupKey(lookupKey), &id, id)
	if err != nil {
		return 0, nil, nil, err
	}
	if !owns {
		return renewalSuperseded, nil, nil, nil
	}
	return renewalRenewed, result, nil, nil
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
