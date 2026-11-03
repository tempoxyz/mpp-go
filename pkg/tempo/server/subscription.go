package chargeserver

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"math"
	"math/big"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	temporpc "github.com/tempoxyz/tempo-go/pkg/client"
	"github.com/tempoxyz/tempo-go/pkg/keychain"
	temposigner "github.com/tempoxyz/tempo-go/pkg/signer"
	tempotx "github.com/tempoxyz/tempo-go/pkg/transaction"

	"github.com/tempoxyz/mpp-go/pkg/mpp"
	mppserver "github.com/tempoxyz/mpp-go/pkg/server"
	"github.com/tempoxyz/mpp-go/pkg/tempo"
)

// SubscriptionLookup is the resolved identity a subscription belongs to,
// such as an authenticated user and plan.
type SubscriptionLookup struct {
	// Key identifies the subscription: one active subscription per key.
	Key string
	// AccessKey is the access key to authorize for this key, when the
	// application manages access keys itself (with Activate).
	AccessKey *tempo.SubscriptionAccessKey
}

// SubscriptionResult is a subscription and the receipt of its latest payment.
type SubscriptionResult struct {
	// Receipt acknowledges the activation or renewal payment.
	Receipt *mpp.Receipt
	// Subscription is the persisted subscription.
	Subscription SubscriptionRecord
}

// SubscriptionResolveParams are passed to SubscriptionConfig.Resolve.
type SubscriptionResolveParams struct {
	// Request is the HTTP request being paid for; nil outside HTTP.
	Request *http.Request
	// Subscription is the subscription request of this route.
	Subscription tempo.SubscriptionRequest
	// Source is the payer recovered from a verified key authorization, when
	// resolving after a Credential was verified.
	Source *SubscriptionPayer
}

// SubscriptionAccessKeyParams are passed to SubscriptionConfig.AccessKey.
type SubscriptionAccessKeyParams struct {
	SubscriptionResolveParams
	// Resolved is the resolved lookup.
	Resolved SubscriptionLookup
}

// SubscriptionActivateParams are passed to SubscriptionConfig.Activate.
type SubscriptionActivateParams struct {
	// AccessKey is the authorized access key; a custom activation must check
	// it is the resolved subscription's.
	AccessKey tempo.SubscriptionAccessKey
	// Credential is the verified subscription Credential.
	Credential *mpp.Credential
	// Payload is its key authorization payload.
	Payload tempo.SubscriptionCredentialPayload
	// Request is the HTTP request; nil outside HTTP.
	Request *http.Request
	// Subscription is the verified subscription request.
	Subscription tempo.SubscriptionRequest
	// Resolved is the resolved lookup.
	Resolved SubscriptionLookup
	// Source is the payer that signed the key authorization.
	Source SubscriptionPayer
}

// SubscriptionRenewParams are passed to SubscriptionConfig.Renew.
type SubscriptionRenewParams struct {
	// InFlightReference is a stable idempotency key for this period's
	// renewal, persisted before Renew runs.
	InFlightReference string
	// PeriodIndex is the billing period being charged.
	PeriodIndex int64
	// Subscription is the subscription; a custom renewal must keep its
	// amount, currency, recipient, period, expiry and lookup key.
	Subscription SubscriptionRecord
}

// SubscriptionConfig configures a Tempo subscription server method.
type SubscriptionConfig struct {
	// Realm is the Challenge realm.
	Realm string
	// SecretKey signs Challenge IDs; empty uses MPP_SECRET_KEY.
	SecretKey string
	// RequiresAuth reads Credentials from Payment-Authorization, as
	// server.WithRequiresAuth does.
	RequiresAuth bool

	// Recipient is the default payee.
	Recipient string
	// Currency is the default TIP-20 token; empty uses the chain's default.
	Currency string
	// Decimals converts human amounts; zero uses tempo.DefaultDecimals.
	Decimals int
	// ChainID is the default chain; zero uses Tempo Moderato, as mppx does.
	ChainID int64
	// Amount, PeriodCount, PeriodUnit, SubscriptionExpires, Description and
	// ExternalID are request defaults that SubscribeParams override.
	Amount              string
	PeriodCount         string
	PeriodUnit          tempo.SubscriptionPeriodUnit
	SubscriptionExpires time.Time
	Description         string
	ExternalID          string

	// Resolve identifies the caller's subscription. It must authenticate and
	// authorize the caller before returning a lookup; return nil when the
	// caller cannot subscribe. Required.
	Resolve func(context.Context, SubscriptionResolveParams) (*SubscriptionLookup, error)
	// AccessKey supplies the access key for a lookup that has none, when
	// Activate manages access keys.
	AccessKey func(context.Context, SubscriptionAccessKeyParams) (*tempo.SubscriptionAccessKey, error)
	// Activate settles a new subscription itself. Without it the method
	// creates a server-owned access key per lookup key and bills the first
	// period on chain with it.
	Activate func(context.Context, SubscriptionActivateParams) (*SubscriptionResult, error)
	// Renew bills an overdue period itself. Without it the method bills on
	// chain with the stored access key.
	Renew func(context.Context, SubscriptionRenewParams) (*SubscriptionResult, error)
	// OnActivated and OnRenewed run after a subscription is activated or renewed.
	OnActivated func(context.Context, SubscriptionResult)
	OnRenewed   func(context.Context, int64, SubscriptionResult)
	// RequireCredential requires a fresh Credential even while a
	// subscription is active, binding reuse to the stored payer.
	RequireCredential bool

	// Store persists subscriptions. Required.
	Store *SubscriptionStore
	// RPC or RPCURL reach the chain for automatic billing; empty uses the
	// chain's default RPC URL.
	RPC    tempo.RPCClient
	RPCURL string
	// Optimistic broadcasts automatic payments without waiting for their
	// receipt. It cannot detect a transfer a receive policy holds; leave it
	// false (mppx's waitForConfirmation: true) unless you accept that.
	Optimistic bool
}

// SubscribeParams are the per-route inputs of Subscription.Subscribe.
type SubscribeParams struct {
	// Authorization and PaymentAuthorization are the incoming headers.
	Authorization        string
	PaymentAuthorization string
	// Request is the HTTP request, passed to Resolve.
	Request *http.Request
	// Body binds the Challenge to the request body, as ChargeParams.Body.
	Body any
	// Expires overrides the 5 minute Challenge expiry.
	Expires string
	// Meta is opaque Challenge metadata.
	Meta map[string]string
	// Amount, PeriodCount, PeriodUnit, SubscriptionExpires, Recipient,
	// Currency, ChainID, Description and ExternalID override the
	// configured defaults.
	Amount              string
	PeriodCount         string
	PeriodUnit          tempo.SubscriptionPeriodUnit
	SubscriptionExpires time.Time
	Recipient           string
	Currency            string
	ChainID             int64
	Description         string
	ExternalID          string
}

// ErrSubscriptionRenewalInFlight is returned, with the current receipt, when
// another request is already renewing the caller's subscription; a
// Credential rejected for that reason wraps it. Respond 409 with
// Retry-After: 1, as mppx does; Subscription.Middleware does.
var ErrSubscriptionRenewalInFlight = errors.New("tempo server: subscription renewal is already in flight")

// Subscription is the Tempo subscription server method
// (draft-tempo-subscription-00), a port of mppx's tempo.subscription.
type Subscription struct {
	config    SubscriptionConfig
	secretKey string
	mpp       *mppserver.Mpp
	intent    *subscriptionIntent
	now       func() time.Time
}

var _ mppserver.Method = (*Subscription)(nil)

// NewSubscription constructs a Tempo subscription server method.
func NewSubscription(config SubscriptionConfig) (*Subscription, error) {
	if config.Resolve == nil {
		return nil, errors.New("tempo server: subscription requires Resolve")
	}
	if config.Store == nil {
		return nil, errors.New("tempo server: subscription requires a Store")
	}
	secretKey := config.SecretKey
	if secretKey == "" {
		secretKey, _ = mppserver.DetectSecretKey()
	}
	s := &Subscription{config: config, secretKey: secretKey, now: time.Now}
	s.intent = &subscriptionIntent{s: s}
	payment, err := mppserver.New(s, config.Realm, secretKey, mppserver.WithRequiresAuth(config.RequiresAuth))
	if err != nil {
		return nil, err
	}
	s.mpp = payment
	return s, nil
}

// Name returns tempo.
func (s *Subscription) Name() string { return tempo.MethodName }

// Intents exposes the subscription intent.
func (s *Subscription) Intents() map[string]mppserver.Intent {
	return map[string]mppserver.Intent{tempo.IntentSubscription: s.intent}
}

// Mpp returns the payment handler verifying this method's Credentials, such
// as with VerifyCredential.
func (s *Subscription) Mpp() *mppserver.Mpp { return s.mpp }

type subscriptionRequestKey struct{}

// Subscribe authorizes a request: it returns the receipt of an active
// subscription (renewing an overdue one), verifies a subscription Credential,
// or returns a Challenge. Write errors and Challenges with
// server.WritePaymentResponse.
func (s *Subscription) Subscribe(ctx context.Context, params SubscribeParams) (*mppserver.ChargeResult, error) {
	authorization := params.Authorization
	if s.config.RequiresAuth {
		authorization = params.PaymentAuthorization
	}
	ctx = context.WithValue(ctx, subscriptionRequestKey{}, params.Request)
	request, err := s.baseRequest(params)
	if err != nil {
		return nil, err
	}
	var credential *mpp.Credential
	if header, _ := mpp.FindPaymentAuthorizationStrict(authorization); header != "" {
		if parsed, err := mpp.ParseCredential(header); err == nil {
			credential = parsed
		}
	}
	if credential == nil && !s.config.RequireCredential {
		result, err := s.authorize(ctx, params.Request, request)
		if errors.Is(err, ErrSubscriptionRenewalInFlight) || err == nil && result != nil {
			return result, err
		}
		// A renewal that failed leaves the caller without a usable
		// subscription: issue a fresh Challenge so it can subscribe again
		// instead of failing every request until the subscription expires.
	}
	challengeRequest, err := s.challengeRequest(ctx, params.Request, request, credential)
	if err != nil {
		return nil, err
	}
	header := ""
	if s.config.RequiresAuth {
		header = mpp.HeaderPaymentAuthorization
	}
	result, err := mppserver.VerifyOrChallenge(ctx, mppserver.VerifyParams{
		Authorization: authorization,
		Intent:        s.intent,
		Request:       challengeRequest.Map(),
		Body:          params.Body,
		Realm:         s.config.Realm,
		SecretKey:     s.secretKey,
		Method:        tempo.MethodName,
		Description:   request.Description,
		Meta:          params.Meta,
		Expires:       params.Expires,
		Header:        header,
	})
	if result == nil {
		return nil, err
	}
	if result.Challenge != nil {
		// Verify rejects a Challenge that expires no earlier than the
		// subscription, so issuing one would only fail after the payer
		// signed it: report the route's misconfiguration instead.
		if timingErr := tempo.ValidateSubscriptionTiming(challengeRequest, result.Challenge.Expires); timingErr != nil {
			return nil, fmt.Errorf("tempo server: subscription Challenge cannot be paid: %s (Challenge expires %s, subscription expires %s)", trimTempoPrefix(timingErr), result.Challenge.Expires, challengeRequest.SubscriptionExpires)
		}
	}
	out := &mppserver.ChargeResult{Challenge: result.Challenge, Credential: result.Credential, Receipt: result.Receipt}
	if result.Challenge != nil {
		out.Challenges = []*mpp.Challenge{result.Challenge}
	}
	return out, err
}

// SubscribeRequest runs Subscribe for an HTTP request, reading its headers
// and body.
func (s *Subscription) SubscribeRequest(r *http.Request, params SubscribeParams) (*mppserver.ChargeResult, error) {
	params.Request = r
	params.Authorization = r.Header.Get(mpp.HeaderAuthorization)
	params.PaymentAuthorization = r.Header.Get(mpp.HeaderPaymentAuthorization)
	if params.Body == nil && r.Body != nil && r.Body != http.NoBody {
		body, err := mppserver.ReadRequestBody(r)
		if err != nil {
			return nil, mppserver.RequestBodyError(err)
		}
		if len(body) > 0 {
			params.Body = body
		}
	}
	return s.Subscribe(r.Context(), params)
}

// Middleware serves next only for requests with an active or newly
// activated subscription, with its Payment-Receipt.
func (s *Subscription) Middleware(params SubscribeParams) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			result, err := s.SubscribeRequest(r, params)
			if errors.Is(err, ErrSubscriptionRenewalInFlight) {
				w.Header().Set("Retry-After", "1")
				w.WriteHeader(http.StatusConflict)
				return
			}
			if err != nil || result == nil || result.IsChallenge() {
				var challenges []*mpp.Challenge
				if result != nil {
					challenges = result.Challenges
				}
				mppserver.WritePaymentResponse(w, err, challenges...)
				return
			}
			result.ServeHTTP(w, r)
			next.ServeHTTP(w, r.WithContext(mppserver.ContextWithPayment(r.Context(), result.Credential, result.Receipt)))
		})
	}
}

// baseRequest builds the route's subscription request without an access key.
func (s *Subscription) baseRequest(params SubscribeParams) (tempo.SubscriptionRequest, error) {
	pick := func(value, fallback string) string {
		if value != "" {
			return value
		}
		return fallback
	}
	chainID := params.ChainID
	if chainID == 0 {
		chainID = s.config.ChainID
	}
	if chainID == 0 {
		chainID = tempo.ChainIDModerato
	}
	currency := pick(params.Currency, s.config.Currency)
	if currency == "" {
		currency = tempo.DefaultCurrencyForChain(chainID)
	}
	expires := params.SubscriptionExpires
	if expires.IsZero() {
		expires = s.config.SubscriptionExpires
	}
	unit := params.PeriodUnit
	if unit == "" {
		unit = s.config.PeriodUnit
	}
	request, err := tempo.NormalizeSubscriptionRequest(tempo.SubscriptionRequestParams{
		Amount:              pick(params.Amount, s.config.Amount),
		Decimals:            s.config.Decimals,
		Currency:            currency,
		Recipient:           pick(params.Recipient, s.config.Recipient),
		Description:         pick(params.Description, s.config.Description),
		ExternalID:          pick(params.ExternalID, s.config.ExternalID),
		PeriodCount:         pick(params.PeriodCount, s.config.PeriodCount),
		PeriodUnit:          unit,
		SubscriptionExpires: expires,
		ChainID:             chainID,
	})
	if err != nil {
		return tempo.SubscriptionRequest{}, fmt.Errorf("tempo server: %w", err)
	}
	return request, nil
}

// challengeRequest adds the access key to the route's request, as mppx's
// subscription request() does: the echoed one for a Credential, otherwise
// the lookup's server-owned key (created on first use), the application's,
// or one sealed for a caller not yet resolved.
func (s *Subscription) challengeRequest(
	ctx context.Context,
	input *http.Request,
	request tempo.SubscriptionRequest,
	credential *mpp.Credential,
) (tempo.SubscriptionRequest, error) {
	var accessKey *tempo.SubscriptionAccessKey
	if credential != nil && credential.Challenge.Method == tempo.MethodName && credential.Challenge.Intent == tempo.IntentSubscription {
		if echoed, err := mpp.B64Decode(credential.Challenge.Request); err == nil {
			if parsed, err := tempo.ParseSubscriptionRequest(echoed); err == nil {
				accessKey = parsed.AccessKey
			}
		}
	}
	// A foreign or malformed Credential names no access key: resolve one as
	// for a request without a Credential, so the caller gets a fresh
	// Challenge rather than an error.
	if accessKey == nil {
		resolved, err := s.config.Resolve(ctx, SubscriptionResolveParams{Request: input, Subscription: request})
		if err != nil {
			return tempo.SubscriptionRequest{}, err
		}
		switch {
		case resolved != nil && s.config.Activate == nil:
			// Automatic mode owns the access key, so applications issue
			// Challenges from only their lookup key.
			stored, err := s.automaticAccessKey(ctx, resolved.Key, request)
			if err != nil {
				return tempo.SubscriptionRequest{}, err
			}
			accessKey = &tempo.SubscriptionAccessKey{AccessKeyAddress: stored.AccessKeyAddress, KeyType: stored.KeyType}
		case resolved != nil:
			accessKey, err = s.resolveAccessKey(ctx, input, request, *resolved)
			if err != nil {
				return tempo.SubscriptionRequest{}, err
			}
			if accessKey == nil {
				existing, err := s.config.Store.GetByKey(ctx, resolved.Key)
				if err != nil {
					return tempo.SubscriptionRequest{}, err
				}
				if existing != nil && s.isActive(*existing) {
					accessKey = existing.AccessKey
				}
			}
		case s.config.RequireCredential && s.config.Activate == nil:
			stored, err := s.config.Store.CreateChallengeAccessKey(ctx)
			if err != nil {
				return tempo.SubscriptionRequest{}, err
			}
			accessKey = &stored.SubscriptionAccessKey
		}
	}
	if accessKey == nil {
		return tempo.SubscriptionRequest{}, mpp.ErrVerificationFailed("subscription accessKey is missing")
	}
	key := *accessKey
	request.AccessKey = &key
	return request, nil
}

// automaticAccessKey returns the lookup key's server-owned access key for a
// new Challenge. Once the lookup key's subscription authorized that key, a
// Challenge that would create another subscription (the old one ended, was
// replaced or changed terms) gets a fresh key: the Tempo keychain refuses to
// authorize a key ID the payer registered before. mppx reuses the key, which
// the keychain then rejects.
func (s *Subscription) automaticAccessKey(ctx context.Context, lookupKey string, request tempo.SubscriptionRequest) (*SubscriptionAccessKeyRecord, error) {
	store := s.config.Store
	stored, err := store.GetOrCreateAccessKey(ctx, lookupKey)
	if err != nil {
		return nil, err
	}
	existing, err := store.GetByKey(ctx, lookupKey)
	if err != nil {
		return nil, err
	}
	if existing == nil || existing.AccessKey == nil ||
		!strings.EqualFold(existing.AccessKey.AccessKeyAddress, stored.AccessKeyAddress) ||
		s.isReusable(*existing, bindingOfRequest(request)) {
		return stored, nil
	}
	return store.rotateAccessKey(ctx, lookupKey, stored.AccessKeyAddress)
}

// isReusable reports whether record serves a Credential for binding without
// a new subscription.
func (s *Subscription) isReusable(record SubscriptionRecord, binding subscriptionBinding) bool {
	if !s.isActive(record) || !subscriptionMatches(record, binding) {
		return false
	}
	return s.config.RequireCredential || s.periodIndex(record) <= record.LastChargedPeriod
}

func (s *Subscription) resolveAccessKey(
	ctx context.Context,
	input *http.Request,
	request tempo.SubscriptionRequest,
	resolved SubscriptionLookup,
) (*tempo.SubscriptionAccessKey, error) {
	if resolved.AccessKey != nil {
		return resolved.AccessKey, nil
	}
	if s.config.AccessKey == nil {
		return nil, nil
	}
	return s.config.AccessKey(ctx, SubscriptionAccessKeyParams{
		SubscriptionResolveParams: SubscriptionResolveParams{Request: input, Subscription: request},
		Resolved:                  resolved,
	})
}

// authorize returns the receipt of the caller's active subscription matching
// request, renewing it first when a period is due. It returns nil, nil when
// the caller must pay with a Credential.
func (s *Subscription) authorize(ctx context.Context, input *http.Request, request tempo.SubscriptionRequest) (*mppserver.ChargeResult, error) {
	resolved, err := s.config.Resolve(ctx, SubscriptionResolveParams{Request: input, Subscription: request})
	if err != nil || resolved == nil {
		return nil, err
	}
	subscription, err := s.config.Store.GetByKey(ctx, resolved.Key)
	if err != nil || subscription == nil || !s.isActive(*subscription) || !subscriptionMatches(*subscription, bindingOfRequest(request)) {
		return nil, err
	}
	periodIndex := s.periodIndex(*subscription)
	if periodIndex <= subscription.LastChargedPeriod {
		return &mppserver.ChargeResult{Receipt: subscriptionReceipt(*subscription)}, nil
	}
	renew := s.renewalHandler(*subscription)
	if renew == nil {
		return nil, nil
	}
	status, result, current, err := s.settleRenewal(ctx, resolved.Key, periodIndex, renew, &request, *subscription)
	if err != nil {
		return nil, err
	}
	switch status {
	case renewalCharged:
		return &mppserver.ChargeResult{Receipt: subscriptionReceipt(*current)}, nil
	case renewalInFlight:
		return &mppserver.ChargeResult{Receipt: subscriptionReceipt(*current)}, ErrSubscriptionRenewalInFlight
	case renewalRenewed:
		if s.config.OnRenewed != nil {
			s.config.OnRenewed(ctx, periodIndex, *result)
		}
		return &mppserver.ChargeResult{Receipt: result.Receipt}, nil
	}
	return nil, nil
}

// subscriptionIntent verifies subscription Credentials.
type subscriptionIntent struct{ s *Subscription }

func (i *subscriptionIntent) Name() string { return tempo.IntentSubscription }

// Verify verifies a subscription Credential's key authorization and
// activates the subscription, or returns the active one it proves.
func (i *subscriptionIntent) Verify(ctx context.Context, credential *mpp.Credential, requestMap map[string]any) (*mpp.Receipt, error) {
	s := i.s
	input, _ := ctx.Value(subscriptionRequestKey{}).(*http.Request)
	request, err := tempo.ParseSubscriptionRequest(requestMap)
	if err != nil {
		return nil, mpp.ErrVerificationFailed(trimTempoPrefix(err))
	}
	if err := tempo.ValidateSubscriptionTiming(request, credential.Challenge.Expires); err != nil {
		return nil, mpp.ErrVerificationFailed(trimTempoPrefix(err))
	}
	payload, err := tempo.ParseSubscriptionCredentialPayload(credential.Payload)
	if err != nil {
		return nil, mpp.ErrVerificationFailed(trimTempoPrefix(err))
	}
	var resolved *SubscriptionLookup
	accessKey := request.AccessKey
	if accessKey == nil {
		resolved, err = s.config.Resolve(ctx, SubscriptionResolveParams{Request: input, Subscription: request})
		if err != nil {
			return nil, err
		}
		if resolved == nil {
			return nil, mpp.ErrVerificationFailed("subscription could not be resolved")
		}
		if accessKey, err = s.resolveAccessKey(ctx, input, request, *resolved); err != nil {
			return nil, err
		}
	}
	if accessKey == nil {
		return nil, mpp.ErrVerificationFailed("subscription accessKey is missing")
	}
	chainID := tempo.ChainIDModerato
	if request.ChainID != nil {
		chainID = *request.ChainID
	}
	verified, err := tempo.VerifySubscriptionKeyAuthorization(accessKey, credential.Challenge.ID, chainID, payload, request)
	if err != nil {
		return nil, mpp.ErrVerificationFailed(trimTempoPrefix(err))
	}
	source := SubscriptionPayer{Address: strings.ToLower(verified.Source.Hex()), ChainID: chainID}
	if credential.Source != "" {
		declared, err := parseSourceDID(credential.Source)
		if err != nil || declared.chainID != chainID || !strings.EqualFold(declared.address, source.Address) {
			return nil, mpp.ErrVerificationFailed("credential source does not match signature")
		}
	}
	if again, err := s.config.Resolve(ctx, SubscriptionResolveParams{Request: input, Subscription: request, Source: &source}); err != nil {
		return nil, err
	} else if again != nil {
		resolved = again
	}
	if resolved == nil {
		return nil, mpp.ErrVerificationFailed("subscription could not be resolved")
	}
	lookup := *resolved
	binding := bindingOfRequest(request)

	isReusable := func(record SubscriptionRecord) bool { return s.isReusable(record, binding) }
	create := func() (*SubscriptionResult, error) {
		result, err := s.activateSubscription(ctx, *accessKey, credential, payload, input, request, lookup, source)
		if err != nil {
			return nil, err
		}
		if result.Subscription.AccessKey == nil {
			key := *accessKey
			result.Subscription.AccessKey = &key
		}
		if err := s.validateSettlement(*result, lookup.Key, 0, "", binding); err != nil {
			return nil, err
		}
		return result, nil
	}
	status, result, existing, err := s.config.Store.activate(ctx, credential.Challenge.ID, lookup.Key, isReusable, create, func(payment SubscriptionPendingPayment, record SubscriptionRecord) (string, error) {
		return s.resolvePayment(ctx, payment, record, true)
	})
	if err != nil {
		return nil, err
	}
	switch status {
	case activationReplayed:
		return nil, mpp.ErrVerificationFailed("subscription credential has already been used")
	case activationInFlight:
		if err := s.config.Store.ReleaseCredential(ctx, credential.Challenge.ID); err != nil {
			return nil, err
		}
		return nil, mpp.ErrVerificationFailed("subscription activation is already in flight")
	case activationClaimMismatch:
		return nil, mpp.ErrVerificationFailed("subscription activation claim mismatch")
	case activationExisting:
		if existing.Payer == nil {
			if s.config.RequireCredential {
				return nil, mpp.ErrVerificationFailed("subscription payer is missing")
			}
		} else if existing.Payer.ChainID != source.ChainID || !strings.EqualFold(existing.Payer.Address, source.Address) {
			return nil, mpp.ErrVerificationFailed("subscription payer mismatch")
		}
		periodIndex := s.periodIndex(*existing)
		if periodIndex <= existing.LastChargedPeriod {
			return subscriptionReceipt(*existing), nil
		}
		renew := s.renewalHandler(*existing)
		if renew == nil {
			return nil, mpp.ErrVerificationFailed("subscription renewal is required")
		}
		status, renewed, current, err := s.settleRenewal(ctx, lookup.Key, periodIndex, renew, &request, *existing)
		if err != nil {
			return nil, err
		}
		switch status {
		case renewalCharged:
			return subscriptionReceipt(*current), nil
		case renewalInFlight:
			if err := s.config.Store.ReleaseCredential(ctx, credential.Challenge.ID); err != nil {
				return nil, err
			}
			return nil, mpp.ErrVerificationFailed("subscription renewal is already in flight").WithCause(ErrSubscriptionRenewalInFlight)
		case renewalRenewed:
			if s.config.OnRenewed != nil {
				s.config.OnRenewed(ctx, periodIndex, *renewed)
			}
			return renewed.Receipt, nil
		}
		return nil, mpp.ErrVerificationFailed("subscription renewal failed")
	}
	if s.config.OnActivated != nil {
		s.config.OnActivated(ctx, *result)
	}
	return result.Receipt, nil
}

// activateSubscription settles the first period: through Activate, or by
// submitting the payment with the key authorization on chain.
func (s *Subscription) activateSubscription(
	ctx context.Context,
	accessKey tempo.SubscriptionAccessKey,
	credential *mpp.Credential,
	payload tempo.SubscriptionCredentialPayload,
	input *http.Request,
	request tempo.SubscriptionRequest,
	lookup SubscriptionLookup,
	source SubscriptionPayer,
) (*SubscriptionResult, error) {
	if s.config.Activate != nil {
		result, err := s.config.Activate(ctx, SubscriptionActivateParams{
			AccessKey:    accessKey,
			Credential:   credential,
			Payload:      payload,
			Request:      input,
			Subscription: request,
			Resolved:     lookup,
			Source:       source,
		})
		if err != nil {
			return nil, err
		}
		if result == nil {
			return nil, mpp.ErrVerificationFailed("subscription activation returned no result")
		}
		return result, nil
	}
	id, err := newSubscriptionID()
	if err != nil {
		return nil, err
	}
	timestamp := formatTimestamp(s.now())
	key := accessKey
	record := SubscriptionRecord{
		AccessKey:           &key,
		Amount:              request.Amount,
		BillingAnchor:       timestamp,
		ChainID:             request.ChainID,
		Currency:            request.Currency,
		ExternalID:          request.ExternalID,
		KeyAuthorization:    payload.Signature,
		LastChargedPeriod:   0,
		LookupKey:           lookup.Key,
		Payer:               &source,
		PeriodCount:         request.PeriodCount,
		PeriodUnit:          request.PeriodUnit,
		Recipient:           request.Recipient,
		SubscriptionExpires: request.SubscriptionExpires,
		SubscriptionID:      id,
		Timestamp:           timestamp,
	}
	reference, err := s.submitPayment(ctx, paymentParams{
		accessKey:        accessKey,
		keyAuthorization: payload.Signature,
		lookupKey:        lookup.Key,
		amount:           request.Amount,
		currency:         request.Currency,
		recipient:        request.Recipient,
		chainID:          source.ChainID,
		reference:        credential.Challenge.ID,
		source:           source,
		record: func(payment SubscriptionPendingPayment) error {
			pending := record
			pending.Reference = payment.Hash
			return s.config.Store.recordActivationPayment(ctx, lookup.Key, credential.Challenge.ID, payment, pending)
		},
	})
	if err != nil {
		return nil, err
	}
	record.Reference = reference
	return &SubscriptionResult{Receipt: subscriptionReceipt(record), Subscription: record}, nil
}

type renewalFunc func(ctx context.Context, params SubscriptionRenewParams) (*SubscriptionResult, error)

// renewalHandler returns Renew, or automatic on-chain billing for a
// subscription with a stored access key and payer, or nil.
func (s *Subscription) renewalHandler(subscription SubscriptionRecord) renewalFunc {
	if s.config.Renew != nil {
		return s.config.Renew
	}
	if subscription.AccessKey == nil || subscription.Payer == nil {
		return nil
	}
	return func(ctx context.Context, params SubscriptionRenewParams) (*SubscriptionResult, error) {
		record := params.Subscription
		chainID := record.Payer.ChainID
		if record.ChainID != nil {
			chainID = *record.ChainID
		}
		reference, err := s.submitPayment(ctx, paymentParams{
			accessKey: *record.AccessKey,
			lookupKey: record.LookupKey,
			amount:    record.Amount,
			currency:  record.Currency,
			recipient: record.Recipient,
			chainID:   chainID,
			reference: params.InFlightReference,
			source:    *record.Payer,
			record: func(payment SubscriptionPendingPayment) error {
				return s.config.Store.recordRenewalPayment(ctx, record.SubscriptionID, params.PeriodIndex, record.InFlightAttempt, payment)
			},
		})
		if err != nil {
			return nil, err
		}
		record.LastChargedPeriod = params.PeriodIndex
		record.Reference = reference
		record.Timestamp = formatTimestamp(s.now())
		return &SubscriptionResult{Receipt: subscriptionReceipt(record), Subscription: record}, nil
	}
}

// settleRenewal bills periodIndex once through the store and validates the
// result keeps the subscription's identity and terms.
func (s *Subscription) settleRenewal(
	ctx context.Context,
	lookupKey string,
	periodIndex int64,
	renew renewalFunc,
	request *tempo.SubscriptionRequest,
	subscription SubscriptionRecord,
) (subscriptionRenewalStatus, *SubscriptionResult, *SubscriptionRecord, error) {
	reference := fmt.Sprintf("renewal:%s:%d", subscription.SubscriptionID, periodIndex)
	status, result, current, err := s.config.Store.renew(ctx, subscription.SubscriptionID, periodIndex, reference, func(started SubscriptionRecord) (*SubscriptionResult, error) {
		renewed, err := renew(ctx, SubscriptionRenewParams{InFlightReference: reference, PeriodIndex: periodIndex, Subscription: started})
		if err != nil {
			return nil, err
		}
		if renewed == nil {
			return nil, mpp.ErrVerificationFailed("subscription renewal returned no result")
		}
		if renewed.Subscription.AccessKey == nil && started.AccessKey != nil {
			key := *started.AccessKey
			renewed.Subscription.AccessKey = &key
		}
		binding := bindingOfRecord(started)
		if request != nil {
			binding = bindingOfRequest(*request)
		}
		if err := s.validateSettlement(*renewed, lookupKey, periodIndex, subscription.SubscriptionID, binding); err != nil {
			return nil, err
		}
		return renewed, nil
	}, func(payment SubscriptionPendingPayment, record SubscriptionRecord) (string, error) {
		return s.resolvePayment(ctx, payment, record, false)
	})
	if err != nil {
		return 0, nil, nil, err
	}
	if status == renewalClaimMismatch {
		return 0, nil, nil, mpp.ErrVerificationFailed("subscription renewal claim mismatch")
	}
	return status, result, current, nil
}

// Renew bills an overdue subscription outside the request path, such as
// from a scheduled job. It returns nil when the subscription is missing,
// inactive, superseded or already paid for the current period.
func (s *Subscription) Renew(ctx context.Context, subscriptionID string) (*SubscriptionResult, error) {
	record, err := s.config.Store.Get(ctx, subscriptionID)
	if err != nil || record == nil || !s.isActive(*record) {
		return nil, err
	}
	active, err := s.config.Store.GetByKey(ctx, record.LookupKey)
	if err != nil || active == nil || active.SubscriptionID != record.SubscriptionID {
		return nil, err
	}
	periodIndex := s.periodIndex(*record)
	if periodIndex <= record.LastChargedPeriod {
		return nil, nil
	}
	renew := s.renewalHandler(*record)
	if renew == nil {
		return nil, nil
	}
	status, result, _, err := s.settleRenewal(ctx, record.LookupKey, periodIndex, renew, nil, *record)
	if err != nil || status != renewalRenewed {
		return nil, err
	}
	if s.config.OnRenewed != nil {
		s.config.OnRenewed(ctx, periodIndex, *result)
	}
	return result, nil
}

// periodIndex is the billing period now falls in, or MaxInt64 once the
// subscription expired or its terms are unreadable.
func (s *Subscription) periodIndex(subscription SubscriptionRecord) int64 {
	anchor, err1 := time.Parse(time.RFC3339Nano, subscription.BillingAnchor)
	expires, err2 := time.Parse(time.RFC3339Nano, subscription.SubscriptionExpires)
	now := s.now()
	if err1 != nil || err2 != nil || !now.Before(expires) {
		return math.MaxInt64
	}
	period, err := tempo.SubscriptionPeriodSeconds(subscription.PeriodCount, subscription.PeriodUnit)
	if err != nil {
		return math.MaxInt64
	}
	elapsed := now.Sub(anchor).Milliseconds()
	if elapsed < 0 {
		return 0
	}
	return elapsed / (int64(period) * 1000) //nolint:gosec // G115: SubscriptionPeriodSeconds caps period at 2^53-1.
}

func (s *Subscription) isActive(subscription SubscriptionRecord) bool {
	if subscription.CanceledAt != "" || subscription.RevokedAt != "" {
		return false
	}
	expires, err := time.Parse(time.RFC3339Nano, subscription.SubscriptionExpires)
	return err == nil && expires.After(s.now())
}

// subscriptionBinding is the economic identity a subscription must keep.
type subscriptionBinding struct {
	amount, currency, externalID, periodCount, recipient, subscriptionExpires string
	periodUnit                                                                tempo.SubscriptionPeriodUnit
	chainID                                                                   *int64
}

func bindingOfRequest(request tempo.SubscriptionRequest) subscriptionBinding {
	return subscriptionBinding{
		amount: request.Amount, currency: strings.ToLower(request.Currency), externalID: request.ExternalID,
		periodCount: request.PeriodCount, periodUnit: request.PeriodUnit, recipient: strings.ToLower(request.Recipient),
		subscriptionExpires: request.SubscriptionExpires, chainID: request.ChainID,
	}
}

func bindingOfRecord(record SubscriptionRecord) subscriptionBinding {
	return subscriptionBinding{
		amount: record.Amount, currency: strings.ToLower(record.Currency), externalID: record.ExternalID,
		periodCount: record.PeriodCount, periodUnit: record.PeriodUnit, recipient: strings.ToLower(record.Recipient),
		subscriptionExpires: record.SubscriptionExpires, chainID: record.ChainID,
	}
}

func subscriptionMatches(record SubscriptionRecord, expected subscriptionBinding) bool {
	actual := bindingOfRecord(record)
	sameChain := (actual.chainID == nil) == (expected.chainID == nil) &&
		(actual.chainID == nil || *actual.chainID == *expected.chainID)
	actual.chainID, expected.chainID = nil, nil
	return sameChain && actual == expected
}

var (
	base64URLPattern = regexp.MustCompile("^[A-Za-z0-9_-]+$")
	txHashPattern    = regexp.MustCompile("^0x[0-9a-fA-F]{64}$")
)

// validateSettlement checks an activation or renewal result, as mppx's
// validateSubscriptionSettlement: a successful receipt for the record, a
// well-formed record for the lookup key and period, and unchanged terms.
func (s *Subscription) validateSettlement(result SubscriptionResult, lookupKey string, periodIndex int64, subscriptionID string, binding subscriptionBinding) error {
	receipt, record := result.Receipt, result.Subscription
	if receipt == nil || receipt.Method != tempo.MethodName || receipt.Status != "success" {
		return mpp.ErrVerificationFailed("subscription receipt is invalid")
	}
	if receipt.SubscriptionID != record.SubscriptionID {
		return mpp.ErrVerificationFailed("subscription receipt id mismatch")
	}
	if receipt.Reference != record.Reference {
		return mpp.ErrVerificationFailed("subscription receipt reference mismatch")
	}
	timestamp, err := time.Parse(time.RFC3339Nano, record.Timestamp)
	if err != nil || !receipt.Timestamp.Equal(timestamp) {
		return mpp.ErrVerificationFailed("subscription receipt timestamp mismatch")
	}
	if !base64URLPattern.MatchString(record.SubscriptionID) {
		return mpp.ErrVerificationFailed("subscriptionId must be base64url")
	}
	if !txHashPattern.MatchString(record.Reference) {
		return mpp.ErrVerificationFailed("subscription reference must be a transaction hash")
	}
	anchor, err := time.Parse(time.RFC3339Nano, record.BillingAnchor)
	if err != nil {
		return mpp.ErrVerificationFailed("subscription billingAnchor is invalid")
	}
	expires, err := time.Parse(time.RFC3339Nano, record.SubscriptionExpires)
	if err != nil {
		return mpp.ErrVerificationFailed("subscriptionExpires is invalid")
	}
	if record.LookupKey != lookupKey {
		return mpp.ErrVerificationFailed("subscription lookupKey does not match the resolved key")
	}
	if record.LastChargedPeriod != periodIndex {
		return mpp.ErrVerificationFailed("subscription lastChargedPeriod does not match the settled period")
	}
	if subscriptionID != "" && record.SubscriptionID != subscriptionID {
		return mpp.ErrVerificationFailed("subscriptionId does not match the active subscription")
	}
	if !anchor.Before(expires) {
		return mpp.ErrVerificationFailed("subscription billingAnchor must be before subscriptionExpires")
	}
	if !subscriptionMatches(record, binding) {
		return mpp.ErrVerificationFailed("subscription record does not match request")
	}
	return nil
}

// subscriptionReceipt is the receipt of a subscription's latest payment.
func subscriptionReceipt(record SubscriptionRecord) *mpp.Receipt {
	opts := []mpp.ReceiptOption{mpp.WithSubscriptionID(record.SubscriptionID)}
	if record.ExternalID != "" {
		opts = append(opts, mpp.WithExternalID(record.ExternalID))
	}
	receipt := mpp.Success(tempo.MethodName, record.Reference, opts...)
	if timestamp, err := time.Parse(time.RFC3339Nano, record.Timestamp); err == nil {
		receipt.Timestamp = timestamp
	}
	return receipt
}

func newSubscriptionID() (string, error) {
	raw := make([]byte, 18)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

type paymentParams struct {
	accessKey                   tempo.SubscriptionAccessKey
	keyAuthorization            string
	lookupKey                   string
	amount, currency, recipient string
	chainID                     int64
	reference                   string
	source                      SubscriptionPayer
	// record persists the signed payment before it is broadcast; an error
	// stops the broadcast.
	record func(SubscriptionPendingPayment) error
}

// submitPayment bills one period on chain: a transferWithMemo from the
// payer, signed by the server-owned access key (with the key authorization
// on activation), and, unless Optimistic, confirms the recipient was credited.
// It hands the signed transaction to params.record before broadcasting it, so
// a payment whose outcome stays unknown is resolved by a later attempt
// instead of charged again with a new nonce.
func (s *Subscription) submitPayment(ctx context.Context, params paymentParams) (string, error) {
	store := s.config.Store
	stored, err := store.GetAccessKeyByAddress(ctx, params.accessKey.AccessKeyAddress, params.accessKey.KeyToken)
	if err != nil {
		return "", err
	}
	if stored == nil {
		if stored, err = store.GetAccessKey(ctx, params.lookupKey); err != nil {
			return "", err
		}
	}
	if stored == nil {
		return "", mpp.ErrVerificationFailed("subscription access key is missing")
	}
	accessSigner, err := temposigner.NewSigner(stored.PrivateKey)
	if err != nil || !strings.EqualFold(accessSigner.Address().Hex(), params.accessKey.AccessKeyAddress) {
		return "", mpp.ErrVerificationFailed("subscription access key does not match stored key")
	}
	rpc, err := s.resolveRPC(params.chainID)
	if err != nil {
		return "", err
	}
	amount, ok := new(big.Int).SetString(params.amount, 10)
	if !ok {
		return "", mpp.ErrVerificationFailed("subscription amount is invalid")
	}
	memo := tempo.EncodeAttribution(params.lookupKey, "", params.reference)
	data, err := tempo.EncodeTransferWithMemo(params.recipient, amount, memo)
	if err != nil {
		return "", err
	}
	root := common.HexToAddress(params.source.Address)
	token := common.HexToAddress(params.currency)
	gasPrice, err := rpcGasPrice(ctx, rpc)
	if err != nil {
		return "", err
	}
	nonce, err := rpc.GetTransactionCount(ctx, root.Hex())
	if err != nil {
		return "", fmt.Errorf("tempo server: get nonce: %w", err)
	}
	build := func(gas uint64) (*tempotx.Tx, error) {
		tx, err := tempotx.NewBuilder(big.NewInt(params.chainID)).
			SetMaxFeePerGas(gasPrice).
			SetMaxPriorityFeePerGas(new(big.Int).Set(gasPrice)).
			SetGas(gas).
			SetNonceKey(big.NewInt(0)).
			SetNonce(nonce).
			SetFeeToken(token).
			AddCall(token, big.NewInt(0), common.FromHex(data)).
			BuildAndValidate()
		if err != nil {
			return nil, fmt.Errorf("tempo server: build subscription transaction: %w", err)
		}
		tx.From = root
		if params.keyAuthorization != "" {
			if tx.KeyAuthorization, err = tempo.DeserializeKeyAuthorization(params.keyAuthorization); err != nil {
				return nil, mpp.ErrVerificationFailed("invalid keyAuthorization payload")
			}
		}
		return tx, nil
	}
	tx, err := build(tempo.DefaultGasLimit)
	if err != nil {
		return "", err
	}
	// Estimating the whole transaction, key authorization included, also
	// simulates it before anything is broadcast.
	gas, err := estimateTransactionGas(ctx, rpc, tx)
	if err != nil {
		return "", err
	}
	if tx, err = build(gas); err != nil {
		return "", err
	}
	if err := keychain.SignWithAccessKey(tx, accessSigner, root); err != nil {
		return "", fmt.Errorf("tempo server: sign subscription transaction: %w", err)
	}
	serialized, err := tempotx.Serialize(tx, nil)
	if err != nil {
		return "", fmt.Errorf("tempo server: serialize subscription transaction: %w", err)
	}
	hash, err := tempotx.ComputeHash(serialized)
	if err != nil {
		return "", fmt.Errorf("tempo server: hash subscription transaction: %w", err)
	}
	pending := SubscriptionPendingPayment{
		Amount:      params.amount,
		ChainID:     params.chainID,
		Currency:    params.currency,
		Hash:        strings.ToLower(hash.Hex()),
		Memo:        memo,
		Recipient:   params.recipient,
		Transaction: serialized,
	}
	if params.record != nil {
		if err := params.record(pending); err != nil {
			return "", err
		}
	}
	reference, err := s.broadcastPayment(ctx, rpc, pending)
	if err != nil {
		return "", err
	}
	// Only an activation makes the key the lookup key's: a renewal of an
	// older subscription must not undo a rotation to a fresh key.
	if params.keyAuthorization != "" {
		err = store.RetainAccessKey(ctx, params.lookupKey, *stored)
	} else {
		err = store.retainAccessKeyAddress(ctx, *stored)
	}
	if err != nil {
		return "", subscriptionPaymentUnknown(pending, err)
	}
	return reference, nil
}

// errSubscriptionPaymentUnknown marks a payment that may have been included:
// its lock stays, with the signed transaction, until a later attempt finds
// the outcome.
var errSubscriptionPaymentUnknown = errors.New("tempo server: subscription payment outcome is unknown")

// errSubscriptionPaymentFailed marks a payment known not to have paid the
// recipient (rejected, reverted or held), so the period may be billed again.
var errSubscriptionPaymentFailed = errors.New("tempo server: subscription payment failed")

func subscriptionPaymentUnknown(pending SubscriptionPendingPayment, cause error) error {
	unknown := mpp.ErrPaymentOutcomeUnknown(pending.Hash)
	unknown.Detail += " The subscription transaction may still be included; it is checked before this period is billed again."
	if cause != nil {
		return unknown.WithCause(fmt.Errorf("%w: %w", errSubscriptionPaymentUnknown, cause))
	}
	return unknown.WithCause(errSubscriptionPaymentUnknown)
}

func subscriptionPaymentFailed(err *mpp.PaymentError, cause error) error {
	if cause != nil {
		return err.WithCause(fmt.Errorf("%w: %w", errSubscriptionPaymentFailed, cause))
	}
	return err.WithCause(errSubscriptionPaymentFailed)
}

// broadcastPayment broadcasts a signed payment and confirms it, recovering a
// transaction included before its RPC reply failed, as charge.go does.
func (s *Subscription) broadcastPayment(ctx context.Context, rpc tempo.RPCClient, pending SubscriptionPendingPayment) (string, error) {
	sent, err := rpc.SendRawTransaction(ctx, pending.Transaction)
	if err != nil {
		_, receipt, ok := lookupConfirmedReceipt(ctx, rpc, pending.Transaction, err)
		if !ok {
			// A node that already holds the transaction may include it
			// later; a transport error leaves the send itself unknown.
			var rpcErr *temporpc.JSONRPCError
			if isDuplicateSubmission(err) || !errors.As(err, &rpcErr) {
				return "", subscriptionPaymentUnknown(pending, nil)
			}
			return "", subscriptionPaymentFailed(mpp.ErrVerificationFailed("subscription transaction was rejected"), err)
		}
		return s.checkPaymentReceipt(pending, receipt)
	}
	if !strings.EqualFold(sent, pending.Hash) {
		return "", subscriptionPaymentUnknown(pending, errors.New("broadcast transaction hash does not match the signed transaction"))
	}
	if s.config.Optimistic {
		return pending.Hash, nil
	}
	// The receipt wait must outlive a canceled request: abandoning it would
	// leave an included payment unrecorded.
	receiptCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), receiptWaitBudget)
	receipt, err := fetchReceipt(receiptCtx, rpc, pending.Hash)
	cancel()
	if err != nil {
		if isTransactionReverted(err) {
			return "", subscriptionPaymentFailed(mpp.ErrVerificationFailed("subscription transaction reverted: "+pending.Hash), nil)
		}
		return "", subscriptionPaymentUnknown(pending, err)
	}
	return s.checkPaymentReceipt(pending, receipt)
}

// checkPaymentReceipt returns the hash of an included payment that credited
// the recipient.
func (s *Subscription) checkPaymentReceipt(pending SubscriptionPendingPayment, receipt map[string]any) (string, error) {
	if asString(receipt["status"]) != "0x1" {
		return "", subscriptionPaymentFailed(mpp.ErrVerificationFailed("subscription transaction reverted: "+pending.Hash), nil)
	}
	amount, ok := new(big.Int).SetString(pending.Amount, 10)
	if !ok {
		return "", subscriptionPaymentFailed(mpp.ErrVerificationFailed("subscription amount is invalid"), nil)
	}
	if !receiptCredits(receipt, common.HexToAddress(pending.Currency), common.HexToAddress(pending.Recipient), amount, pending.Memo) {
		return "", subscriptionPaymentFailed(mpp.ErrVerificationFailed(fmt.Sprintf("subscription transfer was not credited to the recipient (funds may have been held by a receive policy): %s", pending.Hash)), nil)
	}
	hash := asString(receipt["transactionHash"])
	if hash == "" {
		hash = asString(receipt["hash"])
	}
	if hash == "" {
		hash = pending.Hash
	}
	return strings.ToLower(hash), nil
}

// resolvePayment finds the outcome of a payment an earlier attempt left
// unknown: an included transaction settles it, and one not yet included is
// broadcast again, which the node accepts at most once. It returns
// errSubscriptionPaymentFailed only when the payment did not pay the
// recipient; every other error leaves the outcome unknown.
func (s *Subscription) resolvePayment(ctx context.Context, pending SubscriptionPendingPayment, record SubscriptionRecord, activation bool) (string, error) {
	rpc, err := s.resolveRPC(pending.ChainID)
	if err != nil {
		return "", subscriptionPaymentUnknown(pending, err)
	}
	var reference string
	if receipt := lookupIncludedReceipt(ctx, rpc, pending.Hash); receipt != nil {
		reference, err = s.checkPaymentReceipt(pending, receipt)
	} else {
		reference, err = s.broadcastPayment(ctx, rpc, pending)
	}
	if err != nil {
		return "", err
	}
	if record.AccessKey != nil {
		stored, err := s.config.Store.GetAccessKeyByAddress(ctx, record.AccessKey.AccessKeyAddress, record.AccessKey.KeyToken)
		if err == nil && stored != nil {
			if activation {
				err = s.config.Store.RetainAccessKey(ctx, record.LookupKey, *stored)
			} else {
				err = s.config.Store.retainAccessKeyAddress(ctx, *stored)
			}
		}
		if err != nil {
			return "", subscriptionPaymentUnknown(pending, err)
		}
	}
	return reference, nil
}

func (s *Subscription) resolveRPC(chainID int64) (tempo.RPCClient, error) {
	if s.config.RPC != nil {
		return s.config.RPC, nil
	}
	url := s.config.RPCURL
	if url == "" {
		var err error
		if url, err = tempo.RPCURLForChain(chainID); err != nil {
			return nil, err
		}
	}
	return tempo.NewRPCClient(url), nil
}

func rpcGasPrice(ctx context.Context, rpc tempo.RPCClient) (*big.Int, error) {
	response, err := rpc.SendRequest(ctx, "eth_gasPrice")
	if err != nil {
		return nil, fmt.Errorf("tempo server: eth_gasPrice: %w", err)
	}
	if err := response.CheckError(); err != nil {
		return nil, fmt.Errorf("tempo server: eth_gasPrice: %w", err)
	}
	return tempo.ParseHexBigInt(asString(response.Result))
}

func estimateTransactionGas(ctx context.Context, rpc tempo.RPCClient, tx *tempotx.Tx) (uint64, error) {
	callObject, err := transactionCallObject(tx)
	if err != nil {
		return 0, mpp.ErrVerificationFailed("subscription payment preflight failed")
	}
	delete(callObject, "gas")
	response, err := rpc.SendRequest(ctx, "eth_estimateGas", callObject)
	if err != nil {
		return 0, mpp.ErrVerificationFailed("subscription payment preflight failed").WithCause(err)
	}
	if err := response.CheckError(); err != nil {
		return 0, mpp.ErrVerificationFailed("subscription payment preflight failed").WithCause(err)
	}
	gas, err := tempo.ParseHexUint64(asString(response.Result))
	if err != nil || gas == 0 {
		return 0, mpp.ErrVerificationFailed("subscription payment preflight failed")
	}
	return gas, nil
}

// receiptCredits reports whether receipt has a TransferWithMemo of amount
// with memo from token to recipient. Under a receive policy (TIP-1028) a
// held transfer still succeeds but credits the guard, not the recipient.
func receiptCredits(receipt map[string]any, token, recipient common.Address, amount *big.Int, memo string) bool {
	logs, _ := receipt["logs"].([]any)
	for _, raw := range logs {
		entry, ok := raw.(map[string]any)
		if !ok || !strings.EqualFold(asString(entry["address"]), token.Hex()) {
			continue
		}
		topics, _ := entry["topics"].([]any)
		if len(topics) < 4 || !strings.EqualFold(asString(topics[0]), tempo.TransferWithMemoTopic.Hex()) {
			continue
		}
		if !strings.EqualFold(tempo.ParseTopicAddress(asString(topics[2])), recipient.Hex()) ||
			!strings.EqualFold(asString(topics[3]), memo) {
			continue
		}
		value, err := tempo.ParseHexBigInt(asString(entry["data"]))
		if err == nil && value.Cmp(amount) == 0 {
			return true
		}
	}
	return false
}

func trimTempoPrefix(err error) string {
	return strings.TrimPrefix(err.Error(), "tempo: ")
}
