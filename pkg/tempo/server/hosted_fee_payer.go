package chargeserver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
	temposigner "github.com/tempoxyz/tempo-go/pkg/signer"
	tempotx "github.com/tempoxyz/tempo-go/pkg/transaction"

	"github.com/tempoxyz/mpp-go/pkg/mpp"
)

// maxHostedFeePayerResponseSize bounds a hosted fee payer's JSON-RPC reply.
const maxHostedFeePayerResponseSize = 1 << 20

// HostedFeePayer is a hosted (remote) fee-payer service that sponsors charge
// transactions, as mppx's feePayer: {url, headers} configures one.
//
// The server enforces its sponsor policy and simulates the sender's calls,
// then asks the service to co-sign with JSON-RPC eth_fillTransaction. Of the
// service's answer it keeps only the feePayerSignature and feeToken (checked
// against AllowedFeeTokens when that list is set), recovers the sponsor from
// the signature over the sender-committed fields, simulates the final
// transaction with that sponsor and broadcasts it itself.
type HostedFeePayer struct {
	// URL is the service's JSON-RPC endpoint.
	URL string
	// Headers are sent with every request, such as an Authorization header.
	// Content-Type is always application/json.
	Headers map[string]string
	// HTTPClient sends the requests; nil uses a client with a 30 second
	// timeout.
	HTTPClient *http.Client
}

func (h *HostedFeePayer) client() *http.Client {
	if h.HTTPClient != nil {
		return h.HTTPClient
	}
	return &http.Client{Timeout: defaultRelayTimeout}
}

type hostedFillReply struct {
	Result *struct {
		Tx *struct {
			FeePayerSignature map[string]any `json:"feePayerSignature"`
			FeeToken          any            `json:"feeToken"`
		} `json:"tx"`
	} `json:"result"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// fill asks the hosted fee payer to sponsor the sender-signed tx and returns
// its fee payer signature and fee token.
func (h *HostedFeePayer) fill(ctx context.Context, tx *tempotx.Tx) (*temposigner.Signature, common.Address, error) {
	params, err := hostedFeePayerRequest(tx)
	if err != nil {
		return nil, common.Address{}, mpp.ErrVerificationFailed("hosted fee payer request could not be built: " + err.Error())
	}
	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "eth_fillTransaction",
		"params":  []any{params},
	})
	if err != nil {
		return nil, common.Address{}, err
	}
	failed := mpp.ErrVerificationFailed("hosted fee payer failed to sponsor transaction")
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, h.URL, bytes.NewReader(body))
	if err != nil {
		return nil, common.Address{}, failed
	}
	for name, value := range h.Headers {
		if strings.EqualFold(name, "Content-Type") {
			continue
		}
		request.Header.Set(name, value)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := h.client().Do(request)
	if err != nil {
		return nil, common.Address{}, failed
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxHostedFeePayerResponseSize))
	if err != nil {
		return nil, common.Address{}, failed
	}
	var reply hostedFillReply
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&reply); err != nil || reply.Error != nil || reply.Result == nil ||
		reply.Result.Tx == nil || len(reply.Result.Tx.FeePayerSignature) == 0 {
		return nil, common.Address{}, failed
	}
	feeTokenValue, ok := reply.Result.Tx.FeeToken.(string)
	if !ok {
		return nil, common.Address{}, mpp.ErrVerificationFailed("hosted fee payer did not return a feeToken")
	}
	if !strings.HasPrefix(strings.ToLower(feeTokenValue), "0x") || !common.IsHexAddress(feeTokenValue) {
		return nil, common.Address{}, mpp.ErrInvalidPayload("fee-sponsored transaction feeToken is invalid")
	}
	signature, err := parseRPCSignature(reply.Result.Tx.FeePayerSignature)
	if err != nil {
		return nil, common.Address{}, mpp.ErrVerificationFailed("hosted fee payer returned an invalid feePayerSignature")
	}
	return signature, common.HexToAddress(feeTokenValue), nil
}

// parseRPCSignature reads a JSON-RPC {r, s, yParity} secp256k1 signature
// whose values are hex or decimal strings or numbers.
func parseRPCSignature(value map[string]any) (*temposigner.Signature, error) {
	r, err := rpcInteger(value["r"])
	if err != nil {
		return nil, err
	}
	s, err := rpcInteger(value["s"])
	if err != nil {
		return nil, err
	}
	yParity, err := rpcInteger(value["yParity"])
	if err != nil {
		return nil, err
	}
	n := crypto.S256().Params().N
	if r.Sign() <= 0 || r.Cmp(n) >= 0 || s.Sign() <= 0 || s.Cmp(n) >= 0 || !yParity.IsUint64() || yParity.Uint64() > 1 {
		return nil, fmt.Errorf("signature out of range")
	}
	var parity uint8
	if yParity.Sign() != 0 {
		parity = 1
	}
	return temposigner.NewSignature(r, s, parity), nil
}

func rpcInteger(value any) (*big.Int, error) {
	switch typed := value.(type) {
	case json.Number:
		parsed, ok := new(big.Int).SetString(typed.String(), 10)
		if !ok {
			return nil, fmt.Errorf("invalid integer %q", typed)
		}
		return parsed, nil
	case string:
		if strings.HasPrefix(typed, "0x") || strings.HasPrefix(typed, "0X") {
			parsed, ok := new(big.Int).SetString(typed[2:], 16)
			if !ok {
				return nil, fmt.Errorf("invalid hex integer %q", typed)
			}
			return parsed, nil
		}
		parsed, ok := new(big.Int).SetString(typed, 10)
		if !ok {
			return nil, fmt.Errorf("invalid integer %q", typed)
		}
		return parsed, nil
	default:
		return nil, fmt.Errorf("invalid integer %v", value)
	}
}

// hostedFeePayerRequest returns the eth_fillTransaction request asking a
// hosted fee payer to sponsor tx, in mppx's hostedFeePayerRequest shape
// (src/tempo/internal/fee-payer.ts): fields left at zero are omitted, a zero
// call value is "0x", and a key authorization is its JSON-RPC object.
func hostedFeePayerRequest(tx *tempotx.Tx) (map[string]any, error) {
	calls := make([]map[string]any, 0, len(tx.Calls))
	for _, call := range tx.Calls {
		entry := map[string]any{}
		if call.To != nil {
			entry["to"] = strings.ToLower(call.To.Hex())
		}
		if len(call.Data) > 0 {
			entry["data"] = hexutil.Encode(call.Data)
		}
		if call.Value == nil || call.Value.Sign() == 0 {
			entry["value"] = "0x"
		} else {
			entry["value"] = hexutil.EncodeBig(call.Value)
		}
		calls = append(calls, entry)
	}
	request := map[string]any{
		"calls":    calls,
		"feePayer": true,
		"from":     strings.ToLower(tx.From.Hex()),
		"nonce":    hexutil.EncodeUint64(tx.Nonce),
		"nonceKey": hexBig(tx.NonceKey),
		"type":     "0x76",
	}
	if len(tx.AccessList) > 0 {
		request["accessList"] = accessListCallObject(tx.AccessList)
	}
	if tx.Gas != 0 {
		request["gas"] = hexutil.EncodeUint64(tx.Gas)
	}
	if tx.KeyAuthorization != nil {
		keyAuthorization, err := keyAuthorizationRPC(tx.KeyAuthorization)
		if err != nil {
			return nil, err
		}
		request["keyAuthorization"] = keyAuthorization
	}
	if tx.MaxFeePerGas != nil && tx.MaxFeePerGas.Sign() != 0 {
		request["maxFeePerGas"] = hexutil.EncodeBig(tx.MaxFeePerGas)
	}
	if tx.MaxPriorityFeePerGas != nil && tx.MaxPriorityFeePerGas.Sign() != 0 {
		request["maxPriorityFeePerGas"] = hexutil.EncodeBig(tx.MaxPriorityFeePerGas)
	}
	if tx.ValidAfter != 0 {
		request["validAfter"] = hexutil.EncodeUint64(tx.ValidAfter)
	}
	if tx.ValidBefore != 0 {
		request["validBefore"] = hexutil.EncodeUint64(tx.ValidBefore)
	}
	return request, nil
}

var keyAuthorizationKeyTypes = map[uint64]string{0: "secp256k1", 1: "p256", 2: "webAuthn", 3: "multisig"}

// keyAuthorizationRPC renders a signed keyAuthorization RLP value
// ([authorization fields, signature]) as its JSON-RPC object, as ox's
// KeyAuthorization.toRpc does, so a sponsor and an eth_call see the key
// authorization the broadcast transaction carries.
func keyAuthorizationRPC(payload []interface{}) (map[string]any, error) {
	if len(payload) != 2 {
		return nil, fmt.Errorf("key authorization must be [authorization, signature]")
	}
	fields, ok := payload[0].([]interface{})
	signature, sigOK := payload[1].([]byte)
	if !ok || !sigOK || len(fields) < 3 || len(fields) > 9 || len(signature) == 0 {
		return nil, fmt.Errorf("malformed key authorization")
	}
	padded := make([]interface{}, 9)
	copy(padded, fields)
	for i := len(fields); i < 9; i++ {
		padded[i] = []byte{}
	}
	chainID, ok1 := padded[0].([]byte)
	keyType, ok2 := padded[1].([]byte)
	keyID, ok3 := padded[2].([]byte)
	expiry, ok4 := padded[3].([]byte)
	isAdmin, ok5 := padded[7].([]byte)
	if !ok1 || !ok2 || !ok3 || !ok4 || !ok5 || len(keyID) != common.AddressLength || len(keyType) > 1 {
		return nil, fmt.Errorf("malformed key authorization")
	}
	kind, ok := keyAuthorizationKeyTypes[new(big.Int).SetBytes(keyType).Uint64()]
	if !ok {
		return nil, fmt.Errorf("unsupported key authorization key type")
	}
	signatureRPC, err := primitiveSignatureRPC(signature)
	if err != nil {
		return nil, err
	}
	rpc := map[string]any{
		"chainId":   "0x",
		"expiry":    nil,
		"keyId":     strings.ToLower(common.BytesToAddress(keyID).Hex()),
		"keyType":   kind,
		"signature": signatureRPC,
	}
	if isNonZero(chainID) {
		rpc["chainId"] = hexutil.EncodeBig(new(big.Int).SetBytes(chainID))
	}
	if isNonZero(expiry) {
		rpc["expiry"] = hexutil.EncodeBig(new(big.Int).SetBytes(expiry))
	}
	// ox omits an empty limits list (no spending allowed), but the broadcast
	// transaction carries it, so it is kept here.
	if !isEmptyString(padded[4]) {
		limits, err := limitsRPC(padded[4])
		if err != nil {
			return nil, err
		}
		rpc["limits"] = limits
	}
	if !isEmptyString(padded[5]) {
		calls, err := allowedCallsRPC(padded[5])
		if err != nil {
			return nil, err
		}
		rpc["allowedCalls"] = calls
	}
	if !isEmptyString(padded[6]) {
		witness, ok := padded[6].([]byte)
		if !ok || len(witness) != common.HashLength {
			return nil, fmt.Errorf("malformed key authorization witness")
		}
		rpc["witness"] = hexutil.Encode(witness)
	}
	if len(isAdmin) != 0 {
		if !bytes.Equal(isAdmin, []byte{1}) {
			return nil, fmt.Errorf("invalid key authorization admin marker")
		}
		rpc["isAdmin"] = true
	}
	if !isEmptyString(padded[8]) {
		account, ok := padded[8].([]byte)
		if !ok || len(account) != common.AddressLength {
			return nil, fmt.Errorf("malformed key authorization account")
		}
		rpc["account"] = strings.ToLower(common.BytesToAddress(account).Hex())
	} else if kind == "multisig" {
		return nil, fmt.Errorf("multisig key authorization must bind an account")
	}
	return rpc, nil
}

func isNonZero(value []byte) bool {
	for _, b := range value {
		if b != 0 {
			return true
		}
	}
	return false
}

func isEmptyString(value interface{}) bool {
	raw, ok := value.([]byte)
	return ok && len(raw) == 0
}

func hex32(value []byte) string {
	return hexutil.Encode(common.LeftPadBytes(value, 32))
}

// primitiveSignatureRPC renders a key authorization's signature as JSON-RPC
// (ox SignatureEnvelope.toRpc).
func primitiveSignatureRPC(signature []byte) (any, error) {
	if len(signature) == 65 {
		v := signature[64]
		if v != 0 && v != 1 && v != 27 && v != 28 {
			return nil, fmt.Errorf("invalid secp256k1 recovery id")
		}
		return map[string]any{
			"r":       hex32(signature[:32]),
			"s":       hex32(signature[32:64]),
			"yParity": hexutil.EncodeUint64(uint64(v % 27)),
			"type":    "secp256k1",
		}, nil
	}
	data := signature[1:]
	switch signature[0] {
	case envelopeTypeP256:
		if len(data) == p256EnvelopeDataLength {
			return map[string]any{
				"preHash": data[128] != 0,
				"pubKeyX": hex32(data[64:96]),
				"pubKeyY": hex32(data[96:128]),
				"r":       hex32(data[:32]),
				"s":       hex32(data[32:64]),
				"type":    "p256",
			}, nil
		}
	case envelopeTypeWebAuthn:
		if len(data) >= webAuthnMinDataLength && len(data) <= webAuthnMaxDataLength {
			tail := data[len(data)-128:]
			return map[string]any{
				"pubKeyX":      hex32(tail[64:96]),
				"pubKeyY":      hex32(tail[96:]),
				"r":            hex32(tail[:32]),
				"s":            hex32(tail[32:64]),
				"type":         "webAuthn",
				"webauthnData": hexutil.Encode(data[:len(data)-128]),
			}, nil
		}
	case envelopeTypeMultisig:
		if _, err := envelopeSigner(signature); err == nil {
			return hexutil.Encode(data), nil
		}
	}
	return nil, fmt.Errorf("unsupported key authorization signature")
}

func limitsRPC(value interface{}) ([]map[string]any, error) {
	limits, ok := value.([]interface{})
	if !ok {
		return nil, fmt.Errorf("malformed key authorization limits")
	}
	rendered := make([]map[string]any, 0, len(limits))
	for _, rawLimit := range limits {
		limit, ok := rawLimit.([]interface{})
		if !ok || (len(limit) != 2 && len(limit) != 3) {
			return nil, fmt.Errorf("malformed key authorization limit")
		}
		token, ok1 := limit[0].([]byte)
		amount, ok2 := limit[1].([]byte)
		if !ok1 || !ok2 || len(token) != common.AddressLength {
			return nil, fmt.Errorf("malformed key authorization limit")
		}
		entry := map[string]any{
			"token": strings.ToLower(common.BytesToAddress(token).Hex()),
			"limit": hexutil.EncodeBig(new(big.Int).SetBytes(amount)),
		}
		if len(limit) == 3 {
			period, ok := limit[2].([]byte)
			if !ok {
				return nil, fmt.Errorf("malformed key authorization limit")
			}
			if isNonZero(period) {
				entry["period"] = hexutil.EncodeBig(new(big.Int).SetBytes(period))
			}
		}
		rendered = append(rendered, entry)
	}
	return rendered, nil
}

func allowedCallsRPC(value interface{}) ([]map[string]any, error) {
	scopes, ok := value.([]interface{})
	if !ok {
		return nil, fmt.Errorf("malformed key authorization allowed calls")
	}
	rendered := make([]map[string]any, 0, len(scopes))
	for _, rawScope := range scopes {
		scope, ok := rawScope.([]interface{})
		if !ok || len(scope) != 2 {
			return nil, fmt.Errorf("malformed key authorization call scope")
		}
		target, ok1 := scope[0].([]byte)
		rules, ok2 := scope[1].([]interface{})
		if !ok1 || !ok2 || len(target) != common.AddressLength {
			return nil, fmt.Errorf("malformed key authorization call scope")
		}
		entry := map[string]any{"target": strings.ToLower(common.BytesToAddress(target).Hex())}
		renderedRules := make([]map[string]any, 0, len(rules))
		for _, rawRule := range rules {
			rule, ok := rawRule.([]interface{})
			if !ok || len(rule) != 2 {
				return nil, fmt.Errorf("malformed key authorization selector rule")
			}
			selector, ok1 := rule[0].([]byte)
			recipients, ok2 := rule[1].([]interface{})
			if !ok1 || !ok2 || len(selector) != 4 {
				return nil, fmt.Errorf("malformed key authorization selector rule")
			}
			renderedRule := map[string]any{"selector": hexutil.Encode(selector)}
			if len(recipients) > 0 {
				addresses := make([]string, 0, len(recipients))
				for _, rawRecipient := range recipients {
					recipient, ok := rawRecipient.([]byte)
					if !ok || len(recipient) != common.AddressLength {
						return nil, fmt.Errorf("malformed key authorization recipient")
					}
					addresses = append(addresses, strings.ToLower(common.BytesToAddress(recipient).Hex()))
				}
				renderedRule["recipients"] = addresses
			}
			renderedRules = append(renderedRules, renderedRule)
		}
		if len(renderedRules) > 0 {
			entry["selectorRules"] = renderedRules
		}
		rendered = append(rendered, entry)
	}
	return rendered, nil
}
