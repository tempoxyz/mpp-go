package tempo

import (
	"encoding/base64"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tempoxyz/tempo-go/pkg/keychain"
	"github.com/tempoxyz/tempo-go/pkg/signer"
)

const (
	subscriptionTestRoot      = "0x1111111111111111111111111111111111111111111111111111111111111111"
	subscriptionTestKey       = "0x2222222222222222222222222222222222222222"
	subscriptionTestRecipient = "0x3333333333333333333333333333333333333333"
)

func subscriptionTestChallengeID(fill byte) string {
	raw := make([]byte, 32)
	for i := range raw {
		raw[i] = fill
	}
	return base64.RawURLEncoding.EncodeToString(raw)
}

func subscriptionTestRequest(t *testing.T) SubscriptionRequest {
	t.Helper()
	request, err := NormalizeSubscriptionRequest(SubscriptionRequestParams{
		Amount:              "10",
		Currency:            PathUSDAddress,
		Recipient:           subscriptionTestRecipient,
		PeriodCount:         "30",
		PeriodUnit:          SubscriptionPeriodDay,
		SubscriptionExpires: time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC),
		ChainID:             ChainIDModerato,
		AccessKey:           &SubscriptionAccessKey{AccessKeyAddress: subscriptionTestKey, KeyType: SubscriptionKeyTypeSecp256k1},
	})
	require.NoError(t, err)
	return request
}

func subscriptionTestSign(t *testing.T, request SubscriptionRequest, challengeID string) SubscriptionCredentialPayload {
	t.Helper()
	root, err := signer.NewSigner(subscriptionTestRoot)
	require.NoError(t, err)
	signature, err := SignSubscriptionKeyAuthorization(root, *request.AccessKey, challengeID, ChainIDModerato, request)
	require.NoError(t, err)
	return SubscriptionCredentialPayload{Type: CredentialTypeKeyAuthorization, Signature: signature}
}

// TestSubscriptionKeyAuthorizationMatchesOx checks the signed key
// authorization against ox's KeyAuthorization.serialize for the same inputs,
// so mppx servers verify Go credentials and Go servers verify mppx ones.
func TestSubscriptionKeyAuthorizationMatchesOx(t *testing.T) {
	request := subscriptionTestRequest(t)
	challengeID := subscriptionTestChallengeID(7)
	payload := subscriptionTestSign(t, request, challengeID)
	assert.Equal(t, "0xf8d7f89282a5bf80942222222222222222222222222222222222222222846b36ec80dedd9420c00000000000000000000000000000000000008398968083278d00f3f29420c0000000000000000000000000000000000000dcdb8495777d59d5943333333333333333333333333333333333333333a00707070707070707070707070707070707070707070707070707070707070707b841650f393dda656609bbaf56f60399d04a8885f22af0dfd126685a9c20e75abde939bf2792ab2f1522441fb16d2455c6da6ed699825cc5a2c273debd234871b4e51c", payload.Signature)

	verified, err := VerifySubscriptionKeyAuthorization(request.AccessKey, challengeID, ChainIDModerato, payload, request)
	require.NoError(t, err)
	assert.Equal(t, common.HexToAddress("0x19e7e376e7c213b7e7e7e46cc70a5dd086daff2a"), verified.Source)
}

func TestSubscriptionRequestRoundTrip(t *testing.T) {
	request := subscriptionTestRequest(t)
	assert.Equal(t, "10000000", request.Amount)
	assert.Equal(t, "2027-01-01T00:00:00.000Z", request.SubscriptionExpires)
	assert.Equal(t, map[string]any{
		"amount":              "10000000",
		"currency":            "0x20c0000000000000000000000000000000000000",
		"periodCount":         "30",
		"periodUnit":          "day",
		"recipient":           subscriptionTestRecipient,
		"subscriptionExpires": "2027-01-01T00:00:00.000Z",
		"methodDetails": map[string]any{
			"chainId":   ChainIDModerato,
			"accessKey": map[string]any{"accessKeyAddress": subscriptionTestKey, "keyType": "secp256k1"},
		},
	}, request.Map())

	parsed, err := ParseSubscriptionRequest(request.Map())
	require.NoError(t, err)
	assert.Equal(t, request, parsed)

	mixed := request.Map()
	mixed["recipient"] = strings.ToUpper(subscriptionTestRecipient[:2]) + "AbCd" + subscriptionTestRecipient[6:]
	mixed["subscriptionExpires"] = "2027-01-01T00:00:00Z"
	parsed, err = ParseSubscriptionRequest(mixed)
	require.NoError(t, err)
	assert.Equal(t, "0xabcd333333333333333333333333333333333333", parsed.Recipient)
	assert.Equal(t, "2027-01-01T00:00:00.000Z", parsed.SubscriptionExpires)
}

func TestSubscriptionRequestValidation(t *testing.T) {
	valid := subscriptionTestRequest(t).Map()
	cases := map[string]func(map[string]any){
		"zero amount":       func(m map[string]any) { m["amount"] = "0" },
		"bad currency":      func(m map[string]any) { m["currency"] = "pathUSD" },
		"bad recipient":     func(m map[string]any) { m["recipient"] = "0x1234" },
		"zero period count": func(m map[string]any) { m["periodCount"] = "0" },
		"padded count":      func(m map[string]any) { m["periodCount"] = "01" },
		"unknown unit":      func(m map[string]any) { m["periodUnit"] = "month" },
		"fractional expiry": func(m map[string]any) { m["subscriptionExpires"] = "2027-01-01T00:00:00.500Z" },
		"invalid expiry":    func(m map[string]any) { m["subscriptionExpires"] = "next year" },
		"period overflows":  func(m map[string]any) { m["periodCount"] = "18446744073709551615" },
		"unsafe period":     func(m map[string]any) { m["periodCount"] = "9007199254740992"; m["periodUnit"] = "dev_second" },
		"bad access key": func(m map[string]any) {
			m["methodDetails"].(map[string]any)["accessKey"] = map[string]any{"accessKeyAddress": "0x12", "keyType": "secp256k1"}
		},
		"bad key type": func(m map[string]any) {
			m["methodDetails"].(map[string]any)["accessKey"] = map[string]any{"accessKeyAddress": subscriptionTestKey, "keyType": "rsa"}
		},
		"bad key token": func(m map[string]any) {
			m["methodDetails"].(map[string]any)["accessKey"] = map[string]any{"accessKeyAddress": subscriptionTestKey, "keyType": "secp256k1", "keyToken": "0x12"}
		},
		"methodDetails type": func(m map[string]any) { m["methodDetails"] = "x" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			input := subscriptionTestRequest(t).Map()
			mutate(input)
			_, err := ParseSubscriptionRequest(input)
			assert.Error(t, err)
		})
	}
	_, err := ParseSubscriptionRequest(valid)
	assert.NoError(t, err)

	_, err = NormalizeSubscriptionRequest(SubscriptionRequestParams{
		Amount: "1", Currency: PathUSDAddress, Recipient: subscriptionTestRecipient, PeriodCount: "1",
		PeriodUnit: SubscriptionPeriodDay, SubscriptionExpires: time.Date(2027, 1, 1, 0, 0, 0, 1, time.UTC),
	})
	assert.ErrorContains(t, err, "whole seconds")
}

func TestSubscriptionPeriodSeconds(t *testing.T) {
	seconds, err := SubscriptionPeriodSeconds("2", SubscriptionPeriodWeek)
	require.NoError(t, err)
	assert.Equal(t, uint64(1_209_600), seconds)
	seconds, err = SubscriptionPeriodSeconds("5", SubscriptionPeriodDevSecond)
	require.NoError(t, err)
	assert.Equal(t, uint64(5), seconds)
	_, err = SubscriptionPeriodSeconds("106751991167301", SubscriptionPeriodDay)
	assert.ErrorContains(t, err, "cannot be represented exactly")
}

func TestValidateSubscriptionTiming(t *testing.T) {
	request := subscriptionTestRequest(t)
	assert.NoError(t, ValidateSubscriptionTiming(request, "2026-12-31T23:59:59Z"))
	assert.ErrorContains(t, ValidateSubscriptionTiming(request, "2027-01-01T00:00:00Z"), "strictly later than challenge expires")
	assert.ErrorContains(t, ValidateSubscriptionTiming(request, "garbage"), "strictly later than challenge expires")
}

func TestVerifySubscriptionKeyAuthorizationRejectsMismatches(t *testing.T) {
	request := subscriptionTestRequest(t)
	challengeID := subscriptionTestChallengeID(9)
	payload := subscriptionTestSign(t, request, challengeID)

	with := func(mutate func(*SubscriptionRequest)) SubscriptionRequest {
		changed := request
		mutate(&changed)
		return changed
	}
	cases := []struct {
		name      string
		accessKey *SubscriptionAccessKey
		challenge string
		chainID   int64
		request   SubscriptionRequest
		want      string
	}{
		{"amount", request.AccessKey, challengeID, ChainIDModerato, with(func(r *SubscriptionRequest) { r.Amount = "20000000" }), "amount mismatch"},
		{"currency", request.AccessKey, challengeID, ChainIDModerato, with(func(r *SubscriptionRequest) { r.Currency = strings.ToLower(OUSDAddress) }), "currency mismatch"},
		{"recipient", request.AccessKey, challengeID, ChainIDModerato, with(func(r *SubscriptionRequest) { r.Recipient = subscriptionTestKey }), "recipient mismatch"},
		{"period", request.AccessKey, challengeID, ChainIDModerato, with(func(r *SubscriptionRequest) { r.PeriodCount = "31" }), "period mismatch"},
		{"expiry", request.AccessKey, challengeID, ChainIDModerato, with(func(r *SubscriptionRequest) { r.SubscriptionExpires = "2028-01-01T00:00:00.000Z" }), "expiry mismatch"},
		{"chain", request.AccessKey, challengeID, ChainIDMainnet, request, "chainId mismatch"},
		{"access key", &SubscriptionAccessKey{AccessKeyAddress: subscriptionTestRecipient, KeyType: SubscriptionKeyTypeSecp256k1}, challengeID, ChainIDModerato, request, "access key mismatch"},
		{"key type", &SubscriptionAccessKey{AccessKeyAddress: subscriptionTestKey, KeyType: SubscriptionKeyTypeP256}, challengeID, ChainIDModerato, request, "key type mismatch"},
		{"replayed against another challenge", request.AccessKey, subscriptionTestChallengeID(8), ChainIDModerato, request, "challenge mismatch"},
		{"non 32-byte challenge", request.AccessKey, "short", ChainIDModerato, request, "32 bytes"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := VerifySubscriptionKeyAuthorization(tc.accessKey, tc.challenge, tc.chainID, payload, tc.request)
			assert.ErrorContains(t, err, tc.want)
		})
	}

	_, err := VerifySubscriptionKeyAuthorization(request.AccessKey, challengeID, ChainIDModerato, SubscriptionCredentialPayload{Type: CredentialTypeKeyAuthorization, Signature: "0x1234"}, request)
	assert.ErrorContains(t, err, "invalid keyAuthorization payload")
}

func TestVerifySubscriptionKeyAuthorizationRequiresTransferWithMemo(t *testing.T) {
	request := subscriptionTestRequest(t)
	challengeID := subscriptionTestChallengeID(3)
	root, err := signer.NewSigner(subscriptionTestRoot)
	require.NoError(t, err)
	sign := func(scopes []keychain.CallScope) SubscriptionCredentialPayload {
		authorization, err := NewSubscriptionKeyAuthorization(*request.AccessKey, challengeID, ChainIDModerato, request)
		require.NoError(t, err)
		authorization.WithAllowedCalls(scopes)
		signed, err := authorization.Sign(root)
		require.NoError(t, err)
		serialized, err := SerializeKeyAuthorization(signed)
		require.NoError(t, err)
		return SubscriptionCredentialPayload{Type: CredentialTypeKeyAuthorization, Signature: serialized}
	}
	currency := common.HexToAddress(request.Currency)
	recipient := common.HexToAddress(request.Recipient)

	_, err = VerifySubscriptionKeyAuthorization(request.AccessKey, challengeID, ChainIDModerato, sign([]keychain.CallScope{{
		Target: currency, SelectorRules: []keychain.SelectorRule{{Selector: keychain.SelectorTransfer, Recipients: []common.Address{recipient}}},
	}}), request)
	assert.ErrorContains(t, err, "must allow transferWithMemo")

	_, err = VerifySubscriptionKeyAuthorization(request.AccessKey, challengeID, ChainIDModerato, sign([]keychain.CallScope{{
		Target: currency, SelectorRules: []keychain.SelectorRule{{Selector: keychain.SelectorTransferWithMemo}},
	}}), request)
	assert.ErrorContains(t, err, "recipient mismatch")

	_, err = VerifySubscriptionKeyAuthorization(request.AccessKey, challengeID, ChainIDModerato, sign([]keychain.CallScope{{
		Target: currency, SelectorRules: []keychain.SelectorRule{{Selector: keychain.SelectorApprove, Recipients: []common.Address{recipient}}},
	}}), request)
	assert.ErrorContains(t, err, "selector not allowed")

	_, err = VerifySubscriptionKeyAuthorization(request.AccessKey, challengeID, ChainIDModerato, sign([]keychain.CallScope{{
		Target: recipient, SelectorRules: []keychain.SelectorRule{{Selector: keychain.SelectorTransferWithMemo, Recipients: []common.Address{recipient}}},
	}}), request)
	assert.ErrorContains(t, err, "call target mismatch")

	verified, err := VerifySubscriptionKeyAuthorization(request.AccessKey, challengeID, ChainIDModerato, sign([]keychain.CallScope{{
		Target: currency, SelectorRules: []keychain.SelectorRule{
			{Selector: keychain.SelectorTransfer, Recipients: []common.Address{recipient}},
			{Selector: keychain.SelectorTransferWithMemo, Recipients: []common.Address{recipient}},
		},
	}}), request)
	require.NoError(t, err)
	assert.Equal(t, root.Address(), verified.Source)
}

func TestIsKeychainSignatureDecidesBySize(t *testing.T) {
	primitive := make([]byte, 65)
	primitive[0] = 0x04 // r may start with the keychain type byte
	assert.False(t, isKeychainSignature(primitive))
	keychainSig := append([]byte{0x04}, make([]byte, 20+65)...)
	assert.True(t, isKeychainSignature(keychainSig))
	assert.False(t, isKeychainSignature(nil))
}
