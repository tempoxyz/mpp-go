package tempo

import (
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/ethereum/go-ethereum/common"
)

// ChargeMode declares which Tempo credential flow a challenge accepts.
type ChargeMode string

const (
	ChargeModePull ChargeMode = "pull"
	ChargeModePush ChargeMode = "push"
)

// CredentialType identifies the Tempo credential payload shape.
type CredentialType string

const (
	CredentialTypeTransaction CredentialType = "transaction"
	CredentialTypeHash        CredentialType = "hash"
)

// MethodDetails holds Tempo-specific request fields nested under methodDetails.
type MethodDetails struct {
	ChainID        *int64
	FeePayer       bool
	FeePayerURL    string
	Memo           string
	SupportedModes []ChargeMode
}

// ChargeRequest is the canonical Tempo request shape embedded in a Challenge.
type ChargeRequest struct {
	Amount        string
	Currency      string
	Recipient     string
	Description   string
	ExternalID    string
	MethodDetails MethodDetails
}

// ChargeRequestParams are the human-friendly inputs used to build a ChargeRequest.
type ChargeRequestParams struct {
	Amount         string
	Currency       string
	Recipient      string
	Decimals       int
	Description    string
	ExternalID     string
	ChainID        int64
	FeePayer       bool
	FeePayerURL    string
	Memo           string
	SupportedModes []ChargeMode
}

// ChargeCredentialPayload is the Tempo-specific payload carried by a Credential.
type ChargeCredentialPayload struct {
	Type      CredentialType
	Hash      string
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
		request.MethodDetails.SupportedModes = parseModes(raw["supportedModes"])
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
	default:
		return ChargeCredentialPayload{}, fmt.Errorf("tempo: unsupported credential payload type %q", typeValue)
	}
}

// Map converts a Tempo payload back into the generic Credential payload shape.
func (p ChargeCredentialPayload) Map() map[string]any {
	switch p.Type {
	case CredentialTypeHash:
		return map[string]any{"type": string(p.Type), "hash": p.Hash}
	default:
		return map[string]any{"type": string(CredentialTypeTransaction), "signature": p.Signature}
	}
}

// Allows reports whether the request accepts the supplied credential type.
func (r ChargeRequest) Allows(credentialType CredentialType) bool {
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
