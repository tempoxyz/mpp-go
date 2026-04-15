package tempo

import (
	"encoding/hex"
	"fmt"
	"math/big"
	"strings"

	"github.com/ethereum/go-ethereum/common"
)

// ChargeMode declares which Tempo credential flow a challenge accepts.
type ChargeMode string

const (
	// ChargeModePull allows clients to return a signed transaction credential.
	ChargeModePull ChargeMode = "pull"
	// ChargeModePush allows clients to broadcast first and return a hash credential.
	ChargeModePush ChargeMode = "push"
)

// CredentialType identifies the Tempo credential payload shape.
type CredentialType string

const (
	// CredentialTypeTransaction carries a signed Tempo transaction.
	CredentialTypeTransaction CredentialType = "transaction"
	// CredentialTypeHash carries a submitted Tempo transaction hash.
	CredentialTypeHash CredentialType = "hash"
	// CredentialTypeProof carries a zero-amount proof signature.
	CredentialTypeProof CredentialType = "proof"
)

// Split is a canonical additional transfer nested under methodDetails.splits.
type Split struct {
	// Amount is the base-unit amount routed to this split recipient.
	Amount string
	// Memo is an optional split-specific Tempo memo.
	Memo string
	// Recipient is the split recipient address.
	Recipient string
}

// SplitParams are the human-friendly inputs used to build a canonical Split.
type SplitParams struct {
	// Amount is the human-readable amount routed to this split recipient.
	Amount string
	// Memo is an optional split-specific Tempo memo.
	Memo string
	// Recipient is the split recipient address.
	Recipient string
}

// MethodDetails holds Tempo-specific request fields nested under methodDetails.
type MethodDetails struct {
	// ChainID binds the charge to a specific Tempo chain when present.
	ChainID *int64
	// FeePayer enables the sponsored transaction flow.
	FeePayer bool
	// FeePayerURL points at a remote fee-payer signer.
	FeePayerURL string
	// Memo overrides the default attribution memo for the primary transfer.
	Memo string
	// Splits lists any additional transfers included in the charge.
	Splits []Split
	// SupportedModes restricts which credential submission modes are allowed.
	SupportedModes []ChargeMode
}

// ChargeRequest is the canonical Tempo request shape embedded in a Challenge.
type ChargeRequest struct {
	// Amount is the canonical base-unit amount.
	Amount string
	// Currency is the token contract address.
	Currency string
	// Recipient is the primary payee address.
	Recipient string
	// Description is a human-readable challenge description.
	Description string
	// ExternalID is application metadata echoed into receipts.
	ExternalID string
	// MethodDetails stores Tempo-specific extensions for the charge.
	MethodDetails MethodDetails
}

// ChargeRequestParams are the human-friendly inputs used to build a ChargeRequest.
type ChargeRequestParams struct {
	// Amount is the human-readable decimal amount.
	Amount string
	// Currency is the token contract address.
	Currency string
	// Recipient is the primary payee address.
	Recipient string
	// Decimals controls how Amount values are converted to base units.
	Decimals int
	// Description is copied into the challenge for display.
	Description string
	// ExternalID is opaque application metadata echoed into receipts.
	ExternalID string
	// ChainID binds the charge to a specific Tempo chain when set.
	ChainID int64
	// FeePayer enables the sponsored transaction flow.
	FeePayer bool
	// FeePayerURL points at a remote fee-payer signer.
	FeePayerURL string
	// Memo overrides the default attribution memo for the primary transfer.
	Memo string
	// Splits lists any additional transfers included in the charge.
	Splits []SplitParams
	// SupportedModes restricts which credential submission modes are allowed.
	SupportedModes []ChargeMode
}

// ChargeCredentialPayload is the Tempo-specific payload carried by a Credential.
type ChargeCredentialPayload struct {
	// Type selects the payload variant.
	Type CredentialType
	// Hash stores the submitted transaction hash for push flows.
	Hash string
	// Signature stores the raw transaction or proof signature.
	Signature string
}

// NormalizeChargeRequest validates request parameters and produces the canonical Tempo shape.
func NormalizeChargeRequest(params ChargeRequestParams) (ChargeRequest, error) {
	if params.Amount == "" {
		return ChargeRequest{}, fmt.Errorf("tempo: amount is required")
	}
	if params.Currency == "" {
		return ChargeRequest{}, fmt.Errorf("tempo: currency is required")
	}
	if params.Recipient == "" {
		return ChargeRequest{}, fmt.Errorf("tempo: recipient is required")
	}
	decimals := params.Decimals
	if decimals == 0 {
		decimals = DefaultDecimals
	}
	amount, err := parseUnitsString(params.Amount, decimals)
	if err != nil {
		return ChargeRequest{}, err
	}
	currency, err := normalizeAddress("currency", params.Currency)
	if err != nil {
		return ChargeRequest{}, err
	}
	recipient, err := normalizeAddress("recipient", params.Recipient)
	if err != nil {
		return ChargeRequest{}, err
	}
	memo, err := normalizeMemo(params.Memo)
	if err != nil {
		return ChargeRequest{}, err
	}
	splits, err := normalizeSplits(params.Splits, decimals, amount)
	if err != nil {
		return ChargeRequest{}, err
	}
	request := ChargeRequest{
		Amount:      amount,
		Currency:    currency,
		Recipient:   recipient,
		Description: params.Description,
		ExternalID:  params.ExternalID,
		MethodDetails: MethodDetails{
			FeePayer:       params.FeePayer,
			FeePayerURL:    params.FeePayerURL,
			Memo:           memo,
			Splits:         splits,
			SupportedModes: append([]ChargeMode(nil), params.SupportedModes...),
		},
	}
	if params.ChainID != 0 {
		chainID := params.ChainID
		request.MethodDetails.ChainID = &chainID
	}
	return request, nil
}

// ParseChargeRequest parses a generic request map into the canonical Tempo shape.
func ParseChargeRequest(input map[string]any) (ChargeRequest, error) {
	request := ChargeRequest{
		Amount:      asString(input["amount"]),
		Currency:    asString(input["currency"]),
		Recipient:   asString(input["recipient"]),
		Description: asString(input["description"]),
		ExternalID:  asString(input["externalId"]),
	}
	if request.Amount == "" || request.Currency == "" || request.Recipient == "" {
		return ChargeRequest{}, fmt.Errorf("tempo: charge request requires amount, currency, and recipient")
	}
	if _, err := parseBaseUnitAmount(request.Amount); err != nil {
		return ChargeRequest{}, err
	}
	var err error
	request.Currency, err = normalizeAddress("currency", request.Currency)
	if err != nil {
		return ChargeRequest{}, err
	}
	request.Recipient, err = normalizeAddress("recipient", request.Recipient)
	if err != nil {
		return ChargeRequest{}, err
	}
	if raw, ok := input["methodDetails"].(map[string]any); ok {
		if chainID, ok := asInt64(raw["chainId"]); ok {
			request.MethodDetails.ChainID = &chainID
		}
		request.MethodDetails.FeePayer = asBool(raw["feePayer"])
		request.MethodDetails.FeePayerURL = asString(raw["feePayerUrl"])
		request.MethodDetails.Memo, err = normalizeMemo(asString(raw["memo"]))
		if err != nil {
			return ChargeRequest{}, err
		}
		request.MethodDetails.Splits, err = parseSplits(raw["splits"])
		if err != nil {
			return ChargeRequest{}, err
		}
		request.MethodDetails.SupportedModes = parseModes(raw["supportedModes"])
	}
	if err := validateCanonicalSplits(request.Amount, request.MethodDetails.Splits); err != nil {
		return ChargeRequest{}, err
	}
	return request, nil
}

// ParseChargeCredentialPayload parses a generic payload map into a Tempo payload.
func ParseChargeCredentialPayload(input map[string]any) (ChargeCredentialPayload, error) {
	typeValue := CredentialType(asString(input["type"]))
	switch typeValue {
	case CredentialTypeHash:
		hash := asString(input["hash"])
		if hash == "" {
			return ChargeCredentialPayload{}, fmt.Errorf("tempo: hash credential payload is missing hash")
		}
		return ChargeCredentialPayload{Type: typeValue, Hash: hash}, nil
	case CredentialTypeTransaction:
		signature := asString(input["signature"])
		if signature == "" {
			return ChargeCredentialPayload{}, fmt.Errorf("tempo: transaction credential payload is missing signature")
		}
		return ChargeCredentialPayload{Type: typeValue, Signature: signature}, nil
	case CredentialTypeProof:
		signature := asString(input["signature"])
		if signature == "" {
			return ChargeCredentialPayload{}, fmt.Errorf("tempo: proof credential payload is missing signature")
		}
		return ChargeCredentialPayload{Type: typeValue, Signature: signature}, nil
	default:
		return ChargeCredentialPayload{}, fmt.Errorf("tempo: unsupported credential payload type %q", typeValue)
	}
}

// Map converts a Tempo payload back into the generic Credential payload shape.
func (p ChargeCredentialPayload) Map() map[string]any {
	switch p.Type {
	case CredentialTypeHash:
		return map[string]any{"type": string(p.Type), "hash": p.Hash}
	case CredentialTypeProof:
		return map[string]any{"type": string(CredentialTypeProof), "signature": p.Signature}
	default:
		return map[string]any{"type": string(CredentialTypeTransaction), "signature": p.Signature}
	}
}

// Allows reports whether the request accepts the supplied credential type.
func (r ChargeRequest) Allows(credentialType CredentialType) bool {
	if credentialType == CredentialTypeProof {
		return true
	}
	if len(r.MethodDetails.SupportedModes) == 0 {
		return true
	}
	for _, mode := range r.MethodDetails.SupportedModes {
		if mode == ChargeModePull && credentialType == CredentialTypeTransaction {
			return true
		}
		if mode == ChargeModePush && credentialType == CredentialTypeHash {
			return true
		}
	}
	return false
}

// Map converts a ChargeRequest into the generic request map embedded in a Challenge.
func (r ChargeRequest) Map() map[string]any {
	request := map[string]any{
		"amount":    r.Amount,
		"currency":  r.Currency,
		"recipient": r.Recipient,
	}
	if r.Description != "" {
		request["description"] = r.Description
	}
	if r.ExternalID != "" {
		request["externalId"] = r.ExternalID
	}
	methodDetails := map[string]any{}
	if r.MethodDetails.ChainID != nil {
		methodDetails["chainId"] = *r.MethodDetails.ChainID
	}
	if r.MethodDetails.FeePayer {
		methodDetails["feePayer"] = true
	}
	if r.MethodDetails.FeePayerURL != "" {
		methodDetails["feePayerUrl"] = r.MethodDetails.FeePayerURL
	}
	if r.MethodDetails.Memo != "" {
		methodDetails["memo"] = r.MethodDetails.Memo
	}
	if len(r.MethodDetails.Splits) > 0 {
		splits := make([]map[string]any, 0, len(r.MethodDetails.Splits))
		for _, split := range r.MethodDetails.Splits {
			entry := map[string]any{
				"amount":    split.Amount,
				"recipient": split.Recipient,
			}
			if split.Memo != "" {
				entry["memo"] = split.Memo
			}
			splits = append(splits, entry)
		}
		methodDetails["splits"] = splits
	}
	if len(r.MethodDetails.SupportedModes) > 0 {
		modes := make([]string, 0, len(r.MethodDetails.SupportedModes))
		for _, mode := range r.MethodDetails.SupportedModes {
			modes = append(modes, string(mode))
		}
		methodDetails["supportedModes"] = modes
	}
	if len(methodDetails) > 0 {
		request["methodDetails"] = methodDetails
	}
	return request
}

func parseUnitsString(value string, decimals int) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("tempo: amount is required")
	}
	if decimals < 0 {
		return "", fmt.Errorf("tempo: decimals must be non-negative")
	}
	if strings.HasPrefix(value, "-") {
		return "", fmt.Errorf("tempo: amount must be non-negative")
	}
	intPart, fracPart := value, ""
	if dot := strings.IndexByte(value, '.'); dot >= 0 {
		intPart = value[:dot]
		fracPart = value[dot+1:]
	}
	if intPart == "" {
		intPart = "0"
	}
	for _, ch := range intPart + fracPart {
		if ch < '0' || ch > '9' {
			return "", fmt.Errorf("tempo: invalid amount %q", value)
		}
	}
	if len(fracPart) > decimals {
		for _, ch := range fracPart[decimals:] {
			if ch != '0' {
				return "", fmt.Errorf("tempo: amount %q with %d decimals produces fractional base units", value, decimals)
			}
		}
		fracPart = fracPart[:decimals]
	}
	for len(fracPart) < decimals {
		fracPart += "0"
	}
	combined := strings.TrimLeft(intPart+fracPart, "0")
	if combined == "" {
		return "0", nil
	}
	return combined, nil
}

func asString(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case fmt.Stringer:
		return typed.String()
	case nil:
		return ""
	default:
		return fmt.Sprintf("%v", value)
	}
}

func asBool(value any) bool {
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		return typed == "true"
	default:
		return false
	}
}

func asInt64(value any) (int64, bool) {
	switch typed := value.(type) {
	case int:
		return int64(typed), true
	case int64:
		return typed, true
	case float64:
		return int64(typed), true
	case string:
		if typed == "" {
			return 0, false
		}
		var result int64
		_, err := fmt.Sscan(typed, &result)
		return result, err == nil
	default:
		return 0, false
	}
}

func parseModes(value any) []ChargeMode {
	rawModes, ok := value.([]any)
	if !ok {
		if strings, ok := value.([]string); ok {
			modes := make([]ChargeMode, 0, len(strings))
			for _, mode := range strings {
				modes = append(modes, ChargeMode(mode))
			}
			return modes
		}
		return nil
	}
	modes := make([]ChargeMode, 0, len(rawModes))
	for _, mode := range rawModes {
		if value := asString(mode); value != "" {
			modes = append(modes, ChargeMode(value))
		}
	}
	return modes
}

func normalizeSplits(splits []SplitParams, decimals int, totalAmount string) ([]Split, error) {
	if len(splits) == 0 {
		return nil, nil
	}
	if len(splits) > 10 {
		return nil, fmt.Errorf("tempo: splits must contain at most 10 entries")
	}
	canonical := make([]Split, 0, len(splits))
	for _, split := range splits {
		amount, err := parseUnitsString(split.Amount, decimals)
		if err != nil {
			return nil, err
		}
		recipient, err := normalizeAddress("split recipient", split.Recipient)
		if err != nil {
			return nil, err
		}
		memo, err := normalizeMemo(split.Memo)
		if err != nil {
			return nil, err
		}
		canonical = append(canonical, Split{Amount: amount, Recipient: recipient, Memo: memo})
	}
	if err := validateCanonicalSplits(totalAmount, canonical); err != nil {
		return nil, err
	}
	return canonical, nil
}

func parseSplits(value any) ([]Split, error) {
	if value == nil {
		return nil, nil
	}
	var rawSplits []map[string]any
	switch typed := value.(type) {
	case []any:
		rawSplits = make([]map[string]any, 0, len(typed))
		for _, rawSplit := range typed {
			splitMap, ok := rawSplit.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("tempo: split must be an object")
			}
			rawSplits = append(rawSplits, splitMap)
		}
	case []map[string]any:
		rawSplits = typed
	default:
		return nil, fmt.Errorf("tempo: splits must be an array")
	}
	if len(rawSplits) == 0 {
		return nil, fmt.Errorf("tempo: splits must contain at least one entry")
	}
	if len(rawSplits) > 10 {
		return nil, fmt.Errorf("tempo: splits must contain at most 10 entries")
	}
	splits := make([]Split, 0, len(rawSplits))
	for _, splitMap := range rawSplits {
		amount := asString(splitMap["amount"])
		if _, err := parseBaseUnitAmount(amount); err != nil {
			return nil, err
		}
		recipient, err := normalizeAddress("split recipient", asString(splitMap["recipient"]))
		if err != nil {
			return nil, err
		}
		memo, err := normalizeMemo(asString(splitMap["memo"]))
		if err != nil {
			return nil, err
		}
		splits = append(splits, Split{Amount: amount, Recipient: recipient, Memo: memo})
	}
	return splits, nil
}

func validateCanonicalSplits(totalAmount string, splits []Split) error {
	if len(splits) == 0 {
		return nil
	}
	total, err := parseBaseUnitAmount(totalAmount)
	if err != nil {
		return err
	}
	splitTotal := new(big.Int)
	for _, split := range splits {
		amount, err := parseBaseUnitAmount(split.Amount)
		if err != nil {
			return err
		}
		if amount.Sign() <= 0 {
			return fmt.Errorf("tempo: split amounts must be positive")
		}
		splitTotal.Add(splitTotal, amount)
	}
	if splitTotal.Cmp(total) >= 0 {
		return fmt.Errorf("tempo: split total must be less than the total amount")
	}
	return nil
}

func parseBaseUnitAmount(value string) (*big.Int, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, fmt.Errorf("tempo: amount is required")
	}
	for _, ch := range value {
		if ch < '0' || ch > '9' {
			return nil, fmt.Errorf("tempo: invalid amount %q", value)
		}
	}
	parsed, ok := new(big.Int).SetString(value, 10)
	if !ok {
		return nil, fmt.Errorf("tempo: invalid amount %q", value)
	}
	return parsed, nil
}

func normalizeAddress(field, value string) (string, error) {
	if !common.IsHexAddress(value) {
		return "", fmt.Errorf("tempo: %s must be a 20-byte hex address", field)
	}
	return common.HexToAddress(value).Hex(), nil
}

func normalizeMemo(value string) (string, error) {
	if value == "" {
		return "", nil
	}
	trimmed := strings.TrimPrefix(strings.ToLower(value), "0x")
	if len(trimmed) != 64 {
		return "", fmt.Errorf("tempo: memo must be exactly 32 bytes")
	}
	if _, err := hex.DecodeString(trimmed); err != nil {
		return "", fmt.Errorf("tempo: memo must be hex encoded")
	}
	return "0x" + trimmed, nil
}
