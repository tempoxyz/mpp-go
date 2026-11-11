// Package server provides the server-side MPP payment handler.
//
// It implements the HTTP 402 challenge/credential flow: when a request
// arrives without payment credentials, it returns a 402 with a
// WWW-Authenticate challenge. When valid credentials are provided,
// it verifies them and returns a receipt.
package server

import (
	"context"
	"fmt"
	"net/http"
	"reflect"
	"time"

	"github.com/tempoxyz/mpp-go/pkg/mpp"
	"github.com/tempoxyz/mpp-go/pkg/tempo/tempoparams"
)

// Intent is the interface for payment intents (server-side verification).
type Intent interface {
	Name() string
	Verify(ctx context.Context, credential *mpp.Credential, request map[string]any) (*mpp.Receipt, error)
}

// ValidatingIntent supports non-mutating credential validation.
//
// Validate must not consume replay state, sign transactions, broadcast, or
// otherwise mutate payment state. Callers must use BroadcastCredential to
// accept a payment after validation.
type ValidatingIntent interface {
	Intent
	Validate(ctx context.Context, credential *mpp.Credential, request map[string]any) (*Validation, error)
}

// BroadcastingIntent supports a split validation and settlement lifecycle.
//
// Broadcast must revalidate the credential before performing its terminal
// payment operation. Verify remains the compatibility path for callers that do
// not use the split lifecycle explicitly.
type BroadcastingIntent interface {
	ValidatingIntent
	Broadcast(ctx context.Context, credential *mpp.Credential, request map[string]any) (*mpp.Receipt, error)
}

// Validation describes a credential accepted without consuming or broadcasting it.
type Validation struct {
	// Challenge is the server-issued challenge echoed by the credential.
	Challenge mpp.ChallengeEcho
	// Credential is the accepted credential.
	Credential *mpp.Credential
	// Details contains method-specific validation metadata.
	Details map[string]any
	// Intent is the validated payment intent.
	Intent string
	// Method is the validated payment method.
	Method string
	// Request is the challenge request bound to the credential.
	Request map[string]any
	// Source is the optional payer identity.
	Source string
}

// VerifyFunc is the legacy combined credential verification hook.
type VerifyFunc func(
	ctx context.Context,
	credential *mpp.Credential,
	request map[string]any,
) (*mpp.Receipt, error)

// ValidateFunc performs an advisory credential check without mutating payment state.
type ValidateFunc func(
	ctx context.Context,
	credential *mpp.Credential,
	request map[string]any,
) (*Validation, error)

// BroadcastFunc finalizes a validated credential and may mutate payment state.
type BroadcastFunc func(
	ctx context.Context,
	credential *mpp.Credential,
	request map[string]any,
) (*mpp.Receipt, error)

// IntentHooks configures either a legacy Verify hook or split Validate and Broadcast hooks.
type IntentHooks struct {
	// Verify configures the legacy combined verification path.
	Verify VerifyFunc
	// Validate configures the advisory phase of a split verification path.
	Validate ValidateFunc
	// Broadcast configures the settlement phase of a split verification path.
	Broadcast BroadcastFunc
}

type verifyHookIntent struct {
	name   string
	verify VerifyFunc
}

type splitHookIntent struct {
	name      string
	validate  ValidateFunc
	broadcast BroadcastFunc
}

var _ BroadcastingIntent = (*splitHookIntent)(nil)

// NewIntent builds an Intent from either Verify or Validate and Broadcast hooks.
func NewIntent(name string, hooks IntentHooks) (Intent, error) {
	if name == "" {
		return nil, fmt.Errorf("server: intent name is required")
	}
	if hooks.Verify != nil && hooks.Validate == nil && hooks.Broadcast == nil {
		return &verifyHookIntent{name: name, verify: hooks.Verify}, nil
	}
	if hooks.Verify == nil && hooks.Validate != nil && hooks.Broadcast != nil {
		return &splitHookIntent{name: name, validate: hooks.Validate, broadcast: hooks.Broadcast}, nil
	}
	return nil, fmt.Errorf("server: intent hooks require either Verify or both Validate and Broadcast")
}

func (i *verifyHookIntent) Name() string { return i.name }

func (i *verifyHookIntent) Verify(
	ctx context.Context,
	credential *mpp.Credential,
	request map[string]any,
) (*mpp.Receipt, error) {
	return i.verify(ctx, credential, request)
}

func (i *splitHookIntent) Name() string { return i.name }

// Validate performs the configured non-mutating credential check.
func (i *splitHookIntent) Validate(
	ctx context.Context,
	credential *mpp.Credential,
	request map[string]any,
) (*Validation, error) {
	return i.validate(ctx, credential, request)
}

// Broadcast revalidates the credential before invoking the settlement hook.
func (i *splitHookIntent) Broadcast(
	ctx context.Context,
	credential *mpp.Credential,
	request map[string]any,
) (*mpp.Receipt, error) {
	if _, err := i.Validate(ctx, credential, request); err != nil {
		return nil, err
	}
	return i.broadcast(ctx, credential, request)
}

func (i *splitHookIntent) Verify(
	ctx context.Context,
	credential *mpp.Credential,
	request map[string]any,
) (*mpp.Receipt, error) {
	return i.Broadcast(ctx, credential, request)
}

// Method is the interface for server-side payment methods.
type Method interface {
	Name() string
	Intents() map[string]Intent
}

// ChargeRequestBuilder lets a method provide a canonical request shape for the
// generic charge helper. Tempo uses this to normalize human amounts and nest
// method-specific fields under methodDetails.
type ChargeRequestBuilder interface {
	BuildChargeRequest(params ChargeParams) (map[string]any, error)
}

// ChargeOffersBuilder lets a method issue several ordered charge offers for one
// charge, for example one per accepted currency. Charge issues one Challenge
// per returned request and verifies a Credential against the offer it echoes.
// When a method implements both interfaces, BuildChargeRequests takes
// precedence for Charge.
type ChargeOffersBuilder interface {
	BuildChargeRequests(params ChargeParams) ([]map[string]any, error)
}

// minimumSecretKeyBytes is the shortest secret key accepted for HMAC-bound
// challenge IDs. It matches the canonical mppx reference (assertSecretKey).
const minimumSecretKeyBytes = 32

// Mpp is the server-side payment handler.
type Mpp struct {
	method       Method
	realm        string
	secretKey    string
	requiresAuth bool
	// noChallengeNonce is set by WithChallengeNonce(false).
	noChallengeNonce bool
	// events is a pointer so Mpp stays comparable.
	events *Events
}

// Option configures an Mpp instance.
type Option func(*Mpp)

// WithRequiresAuth advertises header="Payment-Authorization" so Payment
// credentials leave Authorization available for application authentication.
func WithRequiresAuth(enabled bool) Option {
	return func(m *Mpp) {
		m.requiresAuth = enabled
	}
}

// WithChallengeNonce sets whether every Challenge this handler issues is made
// unique by a random _mpp_nonce (16 bytes, base64url without padding) bound
// into its opaque metadata, so identical charges never share a Challenge ID.
// It is enabled by default, as in every MPP SDK. Verification accepts
// Challenges with and without a nonce either way, and ignores _mpp_nonce and
// the legacy _mppx_nonce when it matches opaque metadata against the route's.
func WithChallengeNonce(enabled bool) Option {
	return func(m *Mpp) {
		m.noChallengeNonce = !enabled
	}
}

// New creates an Mpp instance.
//
// An empty secretKey defaults to DetectSecretKey (MPP_SECRET_KEY). An empty
// realm stays empty, as before, because the realm is bound into every
// Challenge and replicas with different realms reject each other's
// Challenges; pass DetectRealm() for the environment's realm. New returns an error when method is nil,
// when secretKey is empty and MPP_SECRET_KEY is unset, or when the secret key
// is shorter than 32 bytes, since a weak secret yields forgeable, HMAC-bound
// challenge IDs.
func New(method Method, realm, secretKey string, opts ...Option) (*Mpp, error) {
	if isNilMethod(method) {
		return nil, fmt.Errorf("server: payment method is required")
	}
	if secretKey == "" {
		// An unset MPP_SECRET_KEY leaves the key empty, which the length
		// check below rejects.
		secretKey, _ = DetectSecretKey()
	}
	if len(secretKey) < minimumSecretKeyBytes {
		return nil, fmt.Errorf("server: secret key must be at least %d bytes", minimumSecretKeyBytes)
	}
	m := &Mpp{
		method:    method,
		realm:     realm,
		secretKey: secretKey,
	}
	for _, opt := range opts {
		opt(m)
	}
	return m, nil
}

// isNilMethod reports whether method is nil or a typed nil pointer, map,
// slice, func or channel, whose Name or Intents call would panic.
func isNilMethod(method Method) bool {
	if method == nil {
		return true
	}
	v := reflect.ValueOf(method)
	switch v.Kind() {
	case reflect.Pointer, reflect.Map, reflect.Slice, reflect.Func, reflect.Chan, reflect.Interface:
		return v.IsNil()
	}
	return false
}

// Realm returns the WWW-Authenticate realm used by this payment handler.
func (m *Mpp) Realm() string {
	return m.realm
}

// RequiresAuth reports whether Payment credentials use Payment-Authorization.
func (m *Mpp) RequiresAuth() bool {
	return m.requiresAuth
}

// CredentialHeader returns the HTTP field this handler expects for Payment credentials.
func (m *Mpp) CredentialHeader() string {
	if m.requiresAuth {
		return mpp.HeaderPaymentAuthorization
	}
	return mpp.HeaderAuthorization
}

func (m *Mpp) advertisedCredentialHeader() string {
	if m.requiresAuth {
		return mpp.HeaderPaymentAuthorization
	}
	return ""
}

func (m *Mpp) paymentCredentialValue(authorization, paymentAuthorization string) string {
	if m.requiresAuth {
		return paymentAuthorization
	}
	return authorization
}

// ValidateCredential validates an issued credential without consuming or broadcasting it.
func (m *Mpp) ValidateCredential(ctx context.Context, credential *mpp.Credential) (*Validation, error) {
	intent, request, err := m.prepareCredential(credential)
	if err != nil {
		return nil, err
	}
	return validateCredential(ctx, intent, credential, request, m.method.Name())
}

// BroadcastCredential validates and settles an issued credential.
func (m *Mpp) BroadcastCredential(ctx context.Context, credential *mpp.Credential) (*mpp.Receipt, error) {
	intent, request, err := m.prepareCredential(credential)
	if err != nil {
		return nil, err
	}
	return broadcastCredential(ctx, intent, credential, request)
}

// VerifyCredential is a backwards-compatible alias for BroadcastCredential.
func (m *Mpp) VerifyCredential(ctx context.Context, credential *mpp.Credential) (*mpp.Receipt, error) {
	return m.BroadcastCredential(ctx, credential)
}

// ChargeParams contains the parameters for a charge operation.
//
// Authorization, PaymentAuthorization, Body and MppxScope describe the
// incoming request rather than the price. ChargeRequest, ChargeMiddleware and
// the framework adapters set them from the HTTP request; set them yourself
// only when calling Charge for another transport.
type ChargeParams struct {
	// Authorization is the incoming Authorization header value. ChargeRequest
	// sets it from the request.
	Authorization string
	// PaymentAuthorization is the incoming Payment-Authorization header value.
	// Used when the handler was created with WithRequiresAuth(true).
	// ChargeRequest sets it from the request.
	PaymentAuthorization string
	// Amount is the human-readable charge amount.
	Amount string
	// Currency overrides the method's default currency.
	Currency string
	// Recipient overrides the method's default recipient.
	Recipient string
	// ExternalID is copied into the request and echoed back in the Receipt.
	ExternalID string
	// Body is the actual request body to bind via the challenge digest. It is
	// digested as VerifyParams.Body is. ChargeRequest sets it from a
	// non-empty request body.
	Body any
	// Expires overrides the default Challenge expiry.
	Expires string
	// Description is exposed in the server-generated Challenge.
	Description string
	// Splits adds Tempo split-payment transfers under methodDetails.splits.
	Splits []tempoparams.SplitParams
	// Memo is a 32-byte hex Tempo memo the primary transfer must carry,
	// advertised as methodDetails.memo. Leave it empty to use the client's
	// attribution memo instead: push (hash) credentials must carry the
	// attribution memo for the Challenge, while pull (transaction)
	// credentials may carry it, another memo or none. A push payment
	// without the memo is rejected after the client has broadcast it, so
	// set Memo on push routes only when every payer's client reads
	// methodDetails.memo (mpp-go from this release, mppx); mpp-go clients
	// before it attach their attribution memo instead.
	Memo string
	// FeePayer requests the sponsored Tempo flow when the method supports it.
	// A Tempo method with a fee payer sponsors every route without it.
	FeePayer bool
	// DisableFeePayer opts this route out of sponsorship (mppx's
	// feePayer: false): its Tempo challenge does not offer the fee payer.
	DisableFeePayer bool
	// FeePayerURL is no longer supported: the Tempo charge method returns an
	// error when it is set.
	//
	// Deprecated: configure the fee payer on the server with the Tempo
	// method's Config.FeePayerURL or Config.HostedFeePayer; challenges no
	// longer carry it, as in mppx.
	FeePayerURL string
	// SupportedModes advertises the Tempo submission modes allowed for this challenge.
	SupportedModes []tempoparams.ChargeMode
	// ChainID overrides the method's default Tempo chain ID.
	ChainID int
	// Meta stores opaque Challenge metadata.
	Meta map[string]string
	// MppxScope is the route scope, such as "GET /items/:id", bound into the
	// Challenge's HMAC-bound opaque metadata as _mppx_scope, as mppx's scope
	// option is. A Credential is accepted only on a route binding the same
	// scope. When neither MppxScope nor Meta["_mppx_scope"] is set,
	// ChargeRequest and the middlewares set it from the request (see
	// ScopeFromHTTPRequest and WithDefaultScope). Setting both to different
	// values is a server error.
	MppxScope string
}

// mppxScopeKey is the reserved opaque metadata key of the route scope, as in
// mppx (Scope.reservedMetaKey).
const mppxScopeKey = "_mppx_scope"

// WithDefaultScope returns p with MppxScope set to scope, unless scope is
// empty or p already binds a scope through MppxScope or Meta["_mppx_scope"].
// Like mppx's framework adapters, the middlewares bind a request's route
// scope only to handlers without a scope of their own.
func (p ChargeParams) WithDefaultScope(scope string) ChargeParams {
	if scope == "" || p.MppxScope != "" {
		return p
	}
	// Presence, not value, as in challengeMeta: an explicit Meta scope,
	// even an empty one, is the handler's own.
	if _, ok := p.Meta[mppxScopeKey]; ok {
		return p
	}
	p.MppxScope = scope
	return p
}

// challengeMeta returns the opaque metadata bound into the params'
// Challenges: Meta with MppxScope under _mppx_scope, as mppx's Scope.merge.
func (p ChargeParams) challengeMeta() (map[string]string, error) {
	if p.MppxScope == "" {
		return p.Meta, nil
	}
	if current, ok := p.Meta[mppxScopeKey]; ok {
		if current != p.MppxScope {
			return nil, fmt.Errorf("server: conflicting scope values: MppxScope (%s) does not match Meta[%q] (%s)", p.MppxScope, mppxScopeKey, current)
		}
		return p.Meta, nil
	}
	meta := make(map[string]string, len(p.Meta)+1)
	for k, v := range p.Meta {
		meta[k] = v
	}
	meta[mppxScopeKey] = p.MppxScope
	return meta, nil
}

// ChargeResult is either a Challenge or a verified (Credential, Receipt) pair.
type ChargeResult struct {
	// Challenge is returned when the client still needs to satisfy the payment.
	// With several offers it is the offer the submitted Credential echoed, or
	// the first offer otherwise.
	Challenge *mpp.Challenge
	// Challenges lists every offered Challenge in presentation order whenever
	// Challenge is set. It contains a single element for single-offer methods.
	Challenges []*mpp.Challenge
	// Credential is the verified client credential on success.
	Credential *mpp.Credential
	// Receipt acknowledges a successfully verified payment.
	Receipt *mpp.Receipt

	// err is the error returned with this result, written by ServeHTTP.
	err error
}

// IsChallenge returns true if the result is a 402 challenge. A nil result is
// not a challenge.
func (r *ChargeResult) IsChallenge() bool {
	return r != nil && r.Challenge != nil
}

// ServeHTTP writes the payment response for the result. Call it for every
// result, before writing the resource, so a paid response carries its
// Payment-Receipt:
//
//	res, err := payment.ChargeRequest(r, params)
//	res.ServeHTTP(w, r)
//	if err != nil || res.IsChallenge() {
//		return // the 402 or problem response is written
//	}
//	// The payment is verified: write the resource.
//
// For a Challenge, or an error returned with the result, it writes them as
// WritePaymentResponse does: the 402 offering every Challenge, or the
// problem details with retry Challenges. For a verified payment it only sets
// the Payment-Receipt and Cache-Control: private headers, leaving the status
// and body to the caller. A nil result writes a 500 internal payment error.
func (r *ChargeResult) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	switch {
	case r == nil:
		WritePaymentResponse(w, errNoChargeResult)
	case r.err != nil || r.Challenge != nil:
		WritePaymentResponse(w, r.err, r.offers()...)
	default:
		receipt, _ := mpp.FormatReceiptWithError(r.Receipt)
		setPaymentReceiptForStatus(w.Header(), http.StatusOK, receipt)
	}
}

// offers returns every Challenge the result offers.
func (r *ChargeResult) offers() []*mpp.Challenge {
	if len(r.Challenges) > 0 {
		return r.Challenges
	}
	if r.Challenge != nil {
		return []*mpp.Challenge{r.Challenge}
	}
	return nil
}

// buildChargeRequest produces the canonical request map for a charge operation.
func (m *Mpp) buildChargeRequest(params ChargeParams) (map[string]any, error) {
	if builder, ok := m.method.(ChargeRequestBuilder); ok {
		request, err := builder.BuildChargeRequest(params)
		if err != nil {
			return nil, err
		}
		return request, nil
	}
	request := map[string]any{
		"amount":   params.Amount,
		"currency": params.Currency,
	}
	if params.Recipient != "" {
		request["recipient"] = params.Recipient
	}
	if params.ExternalID != "" {
		request["externalId"] = params.ExternalID
	}
	if params.FeePayer {
		request["feePayer"] = true
	}
	if params.ChainID != 0 {
		request["chainId"] = params.ChainID
	}
	return request, nil
}

// buildChargeRequests produces the ordered canonical request maps offered for
// a charge operation.
func (m *Mpp) buildChargeRequests(params ChargeParams) ([]map[string]any, error) {
	builder, ok := m.method.(ChargeOffersBuilder)
	if !ok {
		request, err := m.buildChargeRequest(params)
		if err != nil {
			return nil, err
		}
		return []map[string]any{request}, nil
	}
	requests, err := builder.BuildChargeRequests(params)
	if err != nil {
		return nil, err
	}
	if len(requests) == 0 {
		return nil, fmt.Errorf("server: method %q returned no charge offers", m.method.Name())
	}
	return requests, nil
}

// selectChargeOffer returns the index of the offer echoed by the Payment
// credential in authorization. It falls back to the first offer when there is
// no credential or no offer matches, so VerifyOrChallenge reports the error.
func selectChargeOffer(authorization string, requests []map[string]any) int {
	if len(requests) < 2 {
		return 0
	}
	header, err := mpp.FindPaymentAuthorizationStrict(authorization)
	if err != nil || header == "" {
		return 0
	}
	credential, err := mpp.ParseCredential(header)
	if err != nil {
		return 0
	}
	echoed, err := echoedRequestMap(credential)
	if err != nil {
		return 0
	}
	for i, request := range requests {
		if mpp.ChallengeBoundJSONEqual(echoed, request) {
			return i
		}
	}
	return 0
}

// Charge handles a charge intent with human-readable amounts, for any
// transport: params carries the Credential (Authorization or
// PaymentAuthorization) and the request Body and MppxScope to bind. For an
// HTTP request, ChargeRequest reads those from the request instead.
//
// Methods implementing ChargeOffersBuilder may offer several requests; Charge
// then returns one Challenge per offer and verifies a Credential against the
// offer it echoes. The result is nil only when err is non-nil and no
// Challenge could be issued.
func (m *Mpp) Charge(ctx context.Context, params ChargeParams) (*ChargeResult, error) {
	return m.chargeWithEvents(ctx, params, true)
}

// chargeWithEvents runs Charge and reports its outcome to the handler's
// events. With challengeEvents false it leaves ChallengeCreated to a composed
// flow, which reports only the Challenges it returns.
func (m *Mpp) chargeWithEvents(ctx context.Context, params ChargeParams, challengeEvents bool) (*ChargeResult, error) {
	authorization := m.paymentCredentialValue(params.Authorization, params.PaymentAuthorization)
	result, err := m.charge(ctx, params)
	if result != nil {
		result.err = err
	}
	m.emitCharge(ctx, authorization, result, err, challengeEvents)
	return result, err
}

func (m *Mpp) charge(ctx context.Context, params ChargeParams) (*ChargeResult, error) {
	intent, ok := m.method.Intents()[mpp.IntentCharge]
	if !ok {
		return nil, fmt.Errorf("method %q does not support charge intent", m.method.Name())
	}

	requests, err := m.buildChargeRequests(params)
	if err != nil {
		return nil, err
	}
	meta, err := params.challengeMeta()
	if err != nil {
		return nil, err
	}

	// Share one expiry so every offer in a response expires together.
	expires := params.Expires
	if expires == "" {
		expires = mpp.ExpiresIn(5 * time.Minute)
	}
	authorization := m.paymentCredentialValue(params.Authorization, params.PaymentAuthorization)
	selected := selectChargeOffer(authorization, requests)
	verifyParams := VerifyParams{
		DisableChallengeNonce: m.noChallengeNonce,
		Authorization:         authorization,
		Intent:                intent,
		Request:               requests[selected],
		Body:                  params.Body,
		Realm:                 m.realm,
		SecretKey:             m.secretKey,
		Method:                m.method.Name(),
		Description:           params.Description,
		Meta:                  meta,
		Expires:               expires,
		Header:                m.advertisedCredentialHeader(),
	}
	result, err := VerifyOrChallenge(ctx, verifyParams)
	if result == nil {
		return nil, err
	}

	chargeResult := &ChargeResult{
		Challenge:  result.Challenge,
		Credential: result.Credential,
		Receipt:    result.Receipt,
	}
	if result.Challenge != nil {
		challenges, offerErr := chargeOfferChallenges(ctx, verifyParams, requests, selected, result.Challenge)
		if offerErr != nil {
			return nil, offerErr
		}
		chargeResult.Challenges = challenges
	}
	return chargeResult, err
}

// chargeOfferChallenges issues a fresh Challenge for every offer, reusing the
// already issued Challenge for the selected offer.
func chargeOfferChallenges(
	ctx context.Context,
	params VerifyParams,
	requests []map[string]any,
	selected int,
	selectedChallenge *mpp.Challenge,
) ([]*mpp.Challenge, error) {
	challenges := make([]*mpp.Challenge, len(requests))
	params.Authorization = ""
	for i, request := range requests {
		if i == selected {
			challenges[i] = selectedChallenge
			continue
		}
		params.Request = request
		result, err := VerifyOrChallenge(ctx, params)
		if err != nil {
			return nil, err
		}
		challenges[i] = result.Challenge
	}
	return challenges, nil
}
