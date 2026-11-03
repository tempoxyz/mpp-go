package tempo

import (
	"encoding/base64"
	"errors"
	"fmt"
	"math/big"
	"regexp"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/tempoxyz/tempo-go/pkg/keychain"
	"github.com/tempoxyz/tempo-go/pkg/signer"

	"github.com/tempoxyz/mpp-go/pkg/mpp"
)

// IntentSubscription is the Tempo subscription intent token
// (draft-tempo-subscription-00): recurring TIP-20 payments authorized once by
// a key authorization that lets a server-held access key make one transfer per
// billing period.
const IntentSubscription = mpp.IntentSubscription

// CredentialTypeKeyAuthorization carries a signed Tempo key authorization for
// a subscription.
const CredentialTypeKeyAuthorization CredentialType = "keyAuthorization"

// SubscriptionPeriodUnit is the unit of a subscription's billing period.
type SubscriptionPeriodUnit string

const (
	// SubscriptionPeriodDay bills every PeriodCount days.
	SubscriptionPeriodDay SubscriptionPeriodUnit = "day"
	// SubscriptionPeriodWeek bills every PeriodCount weeks.
	SubscriptionPeriodWeek SubscriptionPeriodUnit = "week"
	// SubscriptionPeriodDevSecond bills every PeriodCount seconds. It is for
	// development and tests only.
	SubscriptionPeriodDevSecond SubscriptionPeriodUnit = "dev_second"
)

// subscriptionPeriodUnitSeconds is the length of each period unit in seconds.
var subscriptionPeriodUnitSeconds = map[SubscriptionPeriodUnit]uint64{
	SubscriptionPeriodDevSecond: 1,
	SubscriptionPeriodDay:       86_400,
	SubscriptionPeriodWeek:      604_800,
}

// maxSafeInteger is JavaScript's Number.MAX_SAFE_INTEGER. mppx rejects
// periods and expiries above it, so Go servers never issue a subscription a
// TypeScript client or server cannot represent.
const maxSafeInteger = 1<<53 - 1

// Access key types as subscription requests name them.
const (
	SubscriptionKeyTypeSecp256k1 = "secp256k1"
	SubscriptionKeyTypeP256      = "p256"
	SubscriptionKeyTypeWebAuthn  = "webAuthn"
)

// SubscriptionAccessKey is the access key a subscription authorizes: the key
// the server bills each period with.
type SubscriptionAccessKey struct {
	// AccessKeyAddress is the access key's address, lowercase.
	AccessKeyAddress string `json:"accessKeyAddress"`
	// KeyType is secp256k1, p256 or webAuthn.
	KeyType string `json:"keyType"`
	// KeyToken is an opaque, server-sealed copy of the key for challenges
	// issued before the subscriber is known (0x-prefixed hex, 60 bytes).
	KeyToken string `json:"keyToken,omitempty"`
}

// Map converts the access key into its request form.
func (k SubscriptionAccessKey) Map() map[string]any {
	out := map[string]any{
		"accessKeyAddress": k.AccessKeyAddress,
		"keyType":          k.KeyType,
	}
	if k.KeyToken != "" {
		out["keyToken"] = k.KeyToken
	}
	return out
}

// SubscriptionRequest is the canonical Tempo subscription request embedded in
// a Challenge.
type SubscriptionRequest struct {
	// Amount is the base-unit amount charged each period.
	Amount string
	// Currency is the TIP-20 token address, lowercase.
	Currency string
	// Recipient is the payee address, lowercase.
	Recipient string
	// Description is a human-readable description.
	Description string
	// ExternalID is application metadata echoed into receipts.
	ExternalID string
	// PeriodCount is the number of PeriodUnit in one billing period, a
	// positive decimal integer.
	PeriodCount string
	// PeriodUnit is the billing period's unit.
	PeriodUnit SubscriptionPeriodUnit
	// SubscriptionExpires is when the subscription ends, an RFC 3339
	// timestamp in whole seconds, as mppx writes it (2006-01-02T15:04:05.000Z).
	SubscriptionExpires string
	// ChainID binds the subscription to a Tempo chain (methodDetails.chainId).
	ChainID *int64
	// AccessKey is the server's access key (methodDetails.accessKey).
	AccessKey *SubscriptionAccessKey
}

// SubscriptionRequestParams are the human-friendly inputs used to build a
// SubscriptionRequest.
type SubscriptionRequestParams struct {
	// Amount is the human-readable decimal amount charged each period.
	Amount string
	// Decimals converts Amount to base units; zero uses DefaultDecimals.
	Decimals int
	// Currency is the TIP-20 token address.
	Currency string
	// Recipient is the payee address.
	Recipient string
	// Description is a human-readable description.
	Description string
	// ExternalID is application metadata echoed into receipts.
	ExternalID string
	// PeriodCount is the number of PeriodUnit in one billing period.
	PeriodCount string
	// PeriodUnit is the billing period's unit.
	PeriodUnit SubscriptionPeriodUnit
	// SubscriptionExpires is when the subscription ends; it must be whole seconds.
	SubscriptionExpires time.Time
	// ChainID binds the subscription to a Tempo chain when set.
	ChainID int64
	// AccessKey is the server's access key, when known.
	AccessKey *SubscriptionAccessKey
}

// SubscriptionCredentialPayload is the payload of a subscription Credential.
type SubscriptionCredentialPayload struct {
	// Type is always keyAuthorization.
	Type CredentialType
	// Signature is the RLP-encoded signed key authorization, 0x-prefixed hex.
	Signature string
}

// Map converts the payload into the generic Credential payload map.
func (p SubscriptionCredentialPayload) Map() map[string]any {
	return map[string]any{"signature": p.Signature, "type": string(p.Type)}
}

// ParseSubscriptionCredentialPayload parses a subscription Credential payload.
func ParseSubscriptionCredentialPayload(input map[string]any) (SubscriptionCredentialPayload, error) {
	if CredentialType(asString(input["type"])) != CredentialTypeKeyAuthorization {
		return SubscriptionCredentialPayload{}, errors.New("tempo: invalid keyAuthorization payload")
	}
	signature, ok := input["signature"].(string)
	if !ok || !isHexBytes(signature) {
		return SubscriptionCredentialPayload{}, errors.New("tempo: invalid keyAuthorization payload")
	}
	return SubscriptionCredentialPayload{Type: CredentialTypeKeyAuthorization, Signature: signature}, nil
}

// FormatSubscriptionExpires formats t as mppx writes subscriptionExpires
// (Date.toISOString). It fails unless t is whole seconds.
func FormatSubscriptionExpires(t time.Time) (string, error) {
	if t.IsZero() {
		return "", errors.New("tempo: subscriptionExpires is required")
	}
	if t.Nanosecond() != 0 {
		return "", errors.New("tempo: subscriptionExpires must be representable as whole seconds")
	}
	return t.UTC().Format("2006-01-02T15:04:05.000Z"), nil
}

// NormalizeSubscriptionRequest validates params and builds the canonical
// subscription request, as mppx's subscription request schema does.
func NormalizeSubscriptionRequest(params SubscriptionRequestParams) (SubscriptionRequest, error) {
	decimals := params.Decimals
	if decimals == 0 {
		decimals = DefaultDecimals
	}
	if params.Amount == "" {
		return SubscriptionRequest{}, errors.New("tempo: amount is required")
	}
	amount, err := parseUnitsString(params.Amount, decimals)
	if err != nil {
		return SubscriptionRequest{}, err
	}
	expires, err := FormatSubscriptionExpires(params.SubscriptionExpires)
	if err != nil {
		return SubscriptionRequest{}, err
	}
	request := SubscriptionRequest{
		Amount:              amount,
		Currency:            params.Currency,
		Recipient:           params.Recipient,
		Description:         params.Description,
		ExternalID:          params.ExternalID,
		PeriodCount:         params.PeriodCount,
		PeriodUnit:          params.PeriodUnit,
		SubscriptionExpires: expires,
	}
	if params.ChainID != 0 {
		chainID := params.ChainID
		request.ChainID = &chainID
	}
	if params.AccessKey != nil {
		key := *params.AccessKey
		request.AccessKey = &key
	}
	return request.normalized()
}

// ParseSubscriptionRequest parses and validates a subscription request map,
// such as a Challenge's request.
func ParseSubscriptionRequest(input map[string]any) (SubscriptionRequest, error) {
	request := SubscriptionRequest{
		Amount:              asString(input["amount"]),
		Currency:            asString(input["currency"]),
		Recipient:           asString(input["recipient"]),
		Description:         asString(input["description"]),
		ExternalID:          asString(input["externalId"]),
		PeriodCount:         asString(input["periodCount"]),
		PeriodUnit:          SubscriptionPeriodUnit(asString(input["periodUnit"])),
		SubscriptionExpires: asString(input["subscriptionExpires"]),
	}
	if raw, ok := input["methodDetails"]; ok && raw != nil {
		details, ok := raw.(map[string]any)
		if !ok {
			return SubscriptionRequest{}, errors.New("tempo: methodDetails must be an object")
		}
		if chainID, ok, err := asInt64(details["chainId"]); err != nil {
			return SubscriptionRequest{}, err
		} else if ok {
			request.ChainID = &chainID
		}
		if rawKey, ok := details["accessKey"]; ok && rawKey != nil {
			keyMap, ok := rawKey.(map[string]any)
			if !ok {
				return SubscriptionRequest{}, errors.New("tempo: methodDetails.accessKey must be an object")
			}
			request.AccessKey = &SubscriptionAccessKey{
				AccessKeyAddress: asString(keyMap["accessKeyAddress"]),
				KeyType:          asString(keyMap["keyType"]),
				KeyToken:         asString(keyMap["keyToken"]),
			}
		}
	}
	expires, err := time.Parse(time.RFC3339Nano, request.SubscriptionExpires)
	if err != nil {
		return SubscriptionRequest{}, errors.New("tempo: subscriptionExpires must be a valid date")
	}
	request.SubscriptionExpires, err = FormatSubscriptionExpires(expires)
	if err != nil {
		return SubscriptionRequest{}, err
	}
	return request.normalized()
}

var keyTokenPattern = regexp.MustCompile("^0x[0-9a-fA-F]{120}$")

// normalized validates the request and lowercases its addresses.
func (r SubscriptionRequest) normalized() (SubscriptionRequest, error) {
	amount, err := parseBaseUnitAmount(r.Amount)
	if err != nil {
		return SubscriptionRequest{}, err
	}
	if amount.Sign() <= 0 {
		return SubscriptionRequest{}, errors.New("tempo: subscription amount must be greater than 0")
	}
	r.Amount = amount.String()
	if r.Currency, err = lowerAddress("currency", r.Currency); err != nil {
		return SubscriptionRequest{}, err
	}
	if r.Recipient, err = lowerAddress("recipient", r.Recipient); err != nil {
		return SubscriptionRequest{}, err
	}
	if _, err := SubscriptionPeriodSeconds(r.PeriodCount, r.PeriodUnit); err != nil {
		return SubscriptionRequest{}, err
	}
	if _, err := SubscriptionExpirySeconds(r.SubscriptionExpires); err != nil {
		return SubscriptionRequest{}, err
	}
	if r.AccessKey != nil {
		key := *r.AccessKey
		if key.AccessKeyAddress, err = lowerAddress("accessKeyAddress", key.AccessKeyAddress); err != nil {
			return SubscriptionRequest{}, err
		}
		if _, err := subscriptionKeyType(key.KeyType); err != nil {
			return SubscriptionRequest{}, err
		}
		if key.KeyToken != "" && !keyTokenPattern.MatchString(key.KeyToken) {
			return SubscriptionRequest{}, errors.New("tempo: accessKey.keyToken is invalid")
		}
		r.AccessKey = &key
	}
	return r, nil
}

// Map converts the request into the generic request map embedded in a
// Challenge, with Tempo-specific fields under methodDetails.
func (r SubscriptionRequest) Map() map[string]any {
	request := map[string]any{
		"amount":              r.Amount,
		"currency":            r.Currency,
		"periodCount":         r.PeriodCount,
		"periodUnit":          string(r.PeriodUnit),
		"recipient":           r.Recipient,
		"subscriptionExpires": r.SubscriptionExpires,
	}
	if r.Description != "" {
		request["description"] = r.Description
	}
	if r.ExternalID != "" {
		request["externalId"] = r.ExternalID
	}
	details := map[string]any{}
	if r.ChainID != nil {
		details["chainId"] = *r.ChainID
	}
	if r.AccessKey != nil {
		details["accessKey"] = r.AccessKey.Map()
	}
	if len(details) > 0 {
		request["methodDetails"] = details
	}
	return request
}

// SubscriptionPeriodSeconds returns the length of a billing period in
// seconds. It fails for a period that is not a positive integer, overflows
// uint64, or exceeds what mppx can represent exactly (2^53-1 seconds).
func SubscriptionPeriodSeconds(periodCount string, unit SubscriptionPeriodUnit) (uint64, error) {
	if !periodCountPattern.MatchString(periodCount) {
		return 0, errors.New("tempo: periodCount is invalid")
	}
	unitSeconds, ok := subscriptionPeriodUnitSeconds[unit]
	if !ok {
		return 0, errors.New("tempo: periodUnit is invalid")
	}
	count, ok := new(big.Int).SetString(periodCount, 10)
	if !ok {
		return 0, errors.New("tempo: periodCount is invalid")
	}
	value := new(big.Int).Mul(count, new(big.Int).SetUint64(unitSeconds))
	if !value.IsUint64() {
		return 0, errors.New("tempo: subscription period cannot be represented as an unsigned 64-bit integer")
	}
	if value.Uint64() > maxSafeInteger {
		return 0, errors.New("tempo: subscription period cannot be represented exactly by Tempo clients")
	}
	return value.Uint64(), nil
}

var periodCountPattern = regexp.MustCompile("^[1-9][0-9]*$")

// SubscriptionExpirySeconds returns subscriptionExpires as the Unix seconds a
// Tempo key authorization's expiry carries.
func SubscriptionExpirySeconds(subscriptionExpires string) (uint64, error) {
	expires, err := time.Parse(time.RFC3339Nano, subscriptionExpires)
	if err != nil {
		return 0, errors.New("tempo: subscriptionExpires is invalid")
	}
	if expires.Nanosecond() != 0 {
		return 0, errors.New("tempo: subscriptionExpires must be representable as whole seconds")
	}
	seconds := expires.Unix()
	if seconds <= 0 || seconds > maxSafeInteger/1000 {
		return 0, errors.New("tempo: subscriptionExpires cannot be represented in a Tempo key authorization")
	}
	return uint64(seconds), nil
}

// ValidateSubscriptionTiming checks that the request's period and expiry are
// representable and, when challengeExpires is set, that the subscription
// outlives the Challenge.
func ValidateSubscriptionTiming(request SubscriptionRequest, challengeExpires string) error {
	if _, err := SubscriptionPeriodSeconds(request.PeriodCount, request.PeriodUnit); err != nil {
		return err
	}
	if _, err := SubscriptionExpirySeconds(request.SubscriptionExpires); err != nil {
		return err
	}
	if challengeExpires != "" {
		expiry, _ := time.Parse(time.RFC3339Nano, request.SubscriptionExpires)
		challengeExpiry, err := time.Parse(time.RFC3339Nano, challengeExpires)
		if err != nil || expiry.Unix() <= challengeExpiry.Unix() {
			return errors.New("tempo: subscriptionExpires must be strictly later than challenge expires")
		}
	}
	return nil
}

// SubscriptionChallengeWitness decodes a Challenge ID into the 32-byte
// witness a subscription key authorization signs, binding it to one Challenge.
func SubscriptionChallengeWitness(challengeID string) (common.Hash, error) {
	trimmed := strings.TrimRight(challengeID, "=")
	decoded, err := base64.RawURLEncoding.DecodeString(trimmed)
	if err != nil {
		decoded, err = base64.RawStdEncoding.DecodeString(trimmed)
	}
	if err != nil || len(decoded) != common.HashLength {
		return common.Hash{}, errors.New("tempo: challenge id must encode 32 bytes")
	}
	return common.BytesToHash(decoded), nil
}

// NewSubscriptionKeyAuthorization builds the unsigned key authorization a
// subscriber signs: accessKey may spend Amount of Currency once per period,
// only through transferWithMemo to Recipient, until SubscriptionExpires, on
// chainID, bound to challengeID.
func NewSubscriptionKeyAuthorization(
	accessKey SubscriptionAccessKey,
	challengeID string,
	chainID int64,
	request SubscriptionRequest,
) (*keychain.KeyAuthorization, error) {
	if chainID <= 0 {
		return nil, errors.New("tempo: subscription chainId is required")
	}
	keyType, err := subscriptionKeyType(accessKey.KeyType)
	if err != nil {
		return nil, err
	}
	keyAddress, err := lowerAddress("accessKeyAddress", accessKey.AccessKeyAddress)
	if err != nil {
		return nil, err
	}
	expiry, err := SubscriptionExpirySeconds(request.SubscriptionExpires)
	if err != nil {
		return nil, err
	}
	period, err := SubscriptionPeriodSeconds(request.PeriodCount, request.PeriodUnit)
	if err != nil {
		return nil, err
	}
	amount, err := parseBaseUnitAmount(request.Amount)
	if err != nil {
		return nil, err
	}
	witness, err := SubscriptionChallengeWitness(challengeID)
	if err != nil {
		return nil, err
	}
	currency := common.HexToAddress(request.Currency)
	recipient := common.HexToAddress(request.Recipient)
	return keychain.NewKeyAuthorization(uint64(chainID), keyType, common.HexToAddress(keyAddress)).
		WithExpiry(expiry).
		WithLimits([]keychain.TokenLimit{{Token: currency, Amount: amount, Period: period}}).
		WithAllowedCalls([]keychain.CallScope{{
			Target: currency,
			SelectorRules: []keychain.SelectorRule{{
				Selector:   keychain.SelectorTransferWithMemo,
				Recipients: []common.Address{recipient},
			}},
		}}).
		WithWitness(witness), nil
}

// isKeychainSignature reports whether sig is a keychain signature envelope.
// A 65-byte signature is a primitive secp256k1 one whatever its first byte,
// as ox's SignatureEnvelope.from decides by size: one whose r starts with
// 0x04 is not a keychain envelope.
func isKeychainSignature(sig []byte) bool {
	return len(sig) != 65 && len(sig) > 0 && sig[0] == 0x04
}

// SignSubscriptionKeyAuthorization builds and signs the subscription key
// authorization with the subscriber's root key and returns it serialized, as
// a subscription Credential's signature carries it.
func SignSubscriptionKeyAuthorization(
	root signer.EnvelopeSigner,
	accessKey SubscriptionAccessKey,
	challengeID string,
	chainID int64,
	request SubscriptionRequest,
) (string, error) {
	if root == nil {
		return "", errors.New("tempo: subscription signer is required")
	}
	authorization, err := NewSubscriptionKeyAuthorization(accessKey, challengeID, chainID, request)
	if err != nil {
		return "", err
	}
	signed, err := authorization.SignWith(root)
	if err != nil {
		return "", err
	}
	if sig, ok := signed[1].([]byte); ok && isKeychainSignature(sig) {
		return "", errors.New("tempo: keyAuthorization must use a primitive signature")
	}
	return SerializeKeyAuthorization(signed)
}

// SerializeKeyAuthorization RLP-encodes a signed key authorization, as
// keychain.KeyAuthorization.Sign returns it, into 0x-prefixed hex (ox
// KeyAuthorization.serialize).
func SerializeKeyAuthorization(signed []interface{}) (string, error) {
	encoded, err := rlp.EncodeToBytes(signed)
	if err != nil {
		return "", fmt.Errorf("tempo: encode key authorization: %w", err)
	}
	return hexutil.Encode(encoded), nil
}

// DeserializeKeyAuthorization decodes a serialized signed key authorization
// into the form transaction.Tx.KeyAuthorization takes.
func DeserializeKeyAuthorization(serialized string) ([]interface{}, error) {
	raw, err := hexutil.Decode(serialized)
	if err != nil {
		return nil, errors.New("tempo: invalid keyAuthorization payload")
	}
	var signed []interface{}
	if err := rlp.DecodeBytes(raw, &signed); err != nil {
		return nil, errors.New("tempo: invalid keyAuthorization payload")
	}
	return signed, nil
}

// VerifiedSubscriptionAuthorization is a subscription key authorization that
// matched its request.
type VerifiedSubscriptionAuthorization struct {
	// Authorization is the decoded key authorization.
	Authorization *keychain.KeyAuthorization
	// Signed is the signed authorization as transaction.Tx.KeyAuthorization takes it.
	Signed []interface{}
	// Source is the root account that signed it.
	Source common.Address
	// ChainID is the chain it authorizes.
	ChainID int64
}

// VerifySubscriptionKeyAuthorization checks that payload carries a key
// authorization scoped to request: its chain, access key (when accessKey is
// set), Challenge witness, expiry, a single per-period limit of Amount in
// Currency, and a single transferWithMemo scope to Recipient. It returns the
// root account that signed it.
func VerifySubscriptionKeyAuthorization(
	accessKey *SubscriptionAccessKey,
	challengeID string,
	chainID int64,
	payload SubscriptionCredentialPayload,
	request SubscriptionRequest,
) (*VerifiedSubscriptionAuthorization, error) {
	if payload.Type != CredentialTypeKeyAuthorization {
		return nil, errors.New("tempo: invalid keyAuthorization payload")
	}
	signed, err := DeserializeKeyAuthorization(payload.Signature)
	if err != nil {
		return nil, err
	}
	if len(signed) == 2 {
		if sig, ok := signed[1].([]byte); ok && isKeychainSignature(sig) {
			return nil, errors.New("tempo: keyAuthorization must use a primitive signature")
		}
	}
	authorization, signature, err := keychain.DecodeKeyAuthorization(signed)
	if err != nil {
		if len(signed) == 1 {
			return nil, errors.New("tempo: keyAuthorization must use a primitive signature")
		}
		return nil, errors.New("tempo: invalid keyAuthorization payload")
	}
	if authorization.IsAdmin || authorization.Account != nil {
		return nil, errors.New("tempo: invalid keyAuthorization payload")
	}
	if chainID <= 0 || authorization.ChainID != uint64(chainID) {
		return nil, errors.New("tempo: keyAuthorization chainId mismatch")
	}
	if accessKey != nil {
		keyAddress, err := lowerAddress("accessKeyAddress", accessKey.AccessKeyAddress)
		if err != nil {
			return nil, err
		}
		if authorization.KeyID != common.HexToAddress(keyAddress) {
			return nil, errors.New("tempo: keyAuthorization access key mismatch")
		}
		keyType, err := subscriptionKeyType(accessKey.KeyType)
		if err != nil {
			return nil, err
		}
		if authorization.KeyType != keyType {
			return nil, errors.New("tempo: keyAuthorization key type mismatch")
		}
	}
	witness, err := SubscriptionChallengeWitness(challengeID)
	if err != nil {
		return nil, err
	}
	if authorization.Witness == nil || *authorization.Witness != witness {
		return nil, errors.New("tempo: keyAuthorization challenge mismatch")
	}
	if err := ValidateSubscriptionTiming(request, ""); err != nil {
		return nil, err
	}
	expiry, _ := SubscriptionExpirySeconds(request.SubscriptionExpires)
	if authorization.Expiry != expiry {
		return nil, errors.New("tempo: keyAuthorization expiry mismatch")
	}
	if authorization.Limits == nil || len(*authorization.Limits) != 1 {
		return nil, errors.New("tempo: keyAuthorization must contain exactly one token limit")
	}
	limit := (*authorization.Limits)[0]
	currency := common.HexToAddress(request.Currency)
	recipient := common.HexToAddress(request.Recipient)
	if limit.Token != currency {
		return nil, errors.New("tempo: keyAuthorization currency mismatch")
	}
	amount, err := parseBaseUnitAmount(request.Amount)
	if err != nil {
		return nil, err
	}
	if limit.Amount == nil || limit.Amount.Cmp(amount) != 0 {
		return nil, errors.New("tempo: keyAuthorization amount mismatch")
	}
	period, _ := SubscriptionPeriodSeconds(request.PeriodCount, request.PeriodUnit)
	if limit.Period != period {
		return nil, errors.New("tempo: keyAuthorization period mismatch")
	}
	if err := verifySubscriptionScopes(authorization.AllowedCalls, currency, recipient); err != nil {
		return nil, err
	}
	hash, err := authorization.SignatureHash()
	if err != nil {
		return nil, errors.New("tempo: invalid keyAuthorization payload")
	}
	source, _, err := signer.RecoverPrimitive(hash, signature)
	if err != nil {
		return nil, errors.New("tempo: keyAuthorization signature is invalid")
	}
	return &VerifiedSubscriptionAuthorization{
		Authorization: authorization,
		Signed:        signed,
		Source:        source,
		ChainID:       chainID,
	}, nil
}

// verifySubscriptionScopes requires exactly one call scope: currency, with
// transferWithMemo (and optionally transfer) each restricted to recipient
// alone, as mppx's assertAuthorizationScopes does on ox's flat scopes.
func verifySubscriptionScopes(scopes *[]keychain.CallScope, currency, recipient common.Address) error {
	errScope := errors.New("tempo: keyAuthorization must contain a recipient-scoped transferWithMemo call")
	if scopes == nil || len(*scopes) != 1 {
		return errScope
	}
	scope := (*scopes)[0]
	if scope.Target != currency {
		return errors.New("tempo: keyAuthorization call target mismatch")
	}
	if len(scope.SelectorRules) == 0 {
		return errScope
	}
	seen := map[[4]byte]bool{}
	for _, rule := range scope.SelectorRules {
		if rule.Selector != keychain.SelectorTransfer && rule.Selector != keychain.SelectorTransferWithMemo {
			return errors.New("tempo: keyAuthorization selector not allowed")
		}
		if seen[rule.Selector] {
			return errors.New("tempo: keyAuthorization duplicate selector")
		}
		seen[rule.Selector] = true
		if len(rule.Recipients) != 1 || rule.Recipients[0] != recipient {
			return errors.New("tempo: keyAuthorization recipient mismatch")
		}
	}
	if !seen[keychain.SelectorTransferWithMemo] {
		return errors.New("tempo: keyAuthorization must allow transferWithMemo")
	}
	return nil
}

// subscriptionKeyType maps a request key type to its Tempo signature type.
func subscriptionKeyType(keyType string) (uint8, error) {
	switch keyType {
	case SubscriptionKeyTypeSecp256k1:
		return keychain.SignatureTypeSecp256k1, nil
	case SubscriptionKeyTypeP256:
		return keychain.SignatureTypeP256, nil
	case SubscriptionKeyTypeWebAuthn:
		return keychain.SignatureTypeWebAuthn, nil
	default:
		return 0, fmt.Errorf("tempo: invalid access key type %q", keyType)
	}
}

// SubscriptionKeyTypeName returns the request name of a Tempo signature type.
func SubscriptionKeyTypeName(keyType uint8) (string, error) {
	switch keyType {
	case keychain.SignatureTypeSecp256k1:
		return SubscriptionKeyTypeSecp256k1, nil
	case keychain.SignatureTypeP256:
		return SubscriptionKeyTypeP256, nil
	case keychain.SignatureTypeWebAuthn:
		return SubscriptionKeyTypeWebAuthn, nil
	default:
		return "", fmt.Errorf("tempo: invalid access key type %d", keyType)
	}
}

func lowerAddress(field, value string) (string, error) {
	if !common.IsHexAddress(value) || !strings.HasPrefix(value, "0x") && !strings.HasPrefix(value, "0X") {
		return "", fmt.Errorf("tempo: %s must be an address", field)
	}
	return strings.ToLower(common.HexToAddress(value).Hex()), nil
}

func isHexBytes(value string) bool {
	_, err := hexutil.Decode(value)
	return err == nil
}
