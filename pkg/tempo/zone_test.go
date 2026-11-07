package tempo

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// wireRoundTrip encodes a request map as JSON and decodes it with
// json.Number, as Challenges arrive over the wire.
func wireRoundTrip(t *testing.T, value map[string]any) map[string]any {
	t.Helper()
	encoded, err := json.Marshal(value)
	require.NoError(t, err)
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	var decoded map[string]any
	require.NoError(t, decoder.Decode(&decoded))
	return decoded
}

func TestChargeRequestZones(t *testing.T) {
	t.Parallel()
	for name, zones := range map[string]*Zones{
		"ids": {IDs: []uint32{3, 7}},
		"any": {Any: true},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			request, err := NormalizeChargeRequest(ChargeRequestParams{
				Amount: "1", Currency: PathUSDAddress, Recipient: PathUSDAddress, Zones: zones,
			})
			require.NoError(t, err)
			parsed, err := ParseChargeRequest(wireRoundTrip(t, request.Map()))
			require.NoError(t, err)
			assert.Equal(t, zones, parsed.MethodDetails.Zones)
			assert.Equal(t, request, parsed)
		})
	}

	request, err := NormalizeChargeRequest(ChargeRequestParams{Amount: "1", Currency: PathUSDAddress, Recipient: PathUSDAddress})
	require.NoError(t, err)
	assert.NotContains(t, request.Map(), "methodDetails")
}

func TestChargeRequestRejectsInvalidZones(t *testing.T) {
	t.Parallel()
	for _, zones := range []*Zones{{}, {Any: true, IDs: []uint32{1}}, {IDs: []uint32{0}}} {
		_, err := NormalizeChargeRequest(ChargeRequestParams{Amount: "1", Currency: PathUSDAddress, Recipient: PathUSDAddress, Zones: zones})
		assert.Error(t, err, "zones %+v", zones)
	}
	for _, raw := range []string{"false", "[]", "[0]", "[1.5]", "[4294967296]", "[-1]", "\"3\"", "{}"} {
		input := wireRoundTrip(t, map[string]any{
			"amount": "1", "currency": PathUSDAddress, "recipient": PathUSDAddress,
			"methodDetails": map[string]any{"zones": json.RawMessage(raw)},
		})
		_, err := ParseChargeRequest(input)
		assert.Error(t, err, "zones %s", raw)
	}
}

func TestZoneCredentialPayload(t *testing.T) {
	t.Parallel()
	hash := "0x" + strings.Repeat("AB", 32)
	tag := "0x" + strings.Repeat("A1", 32)
	payload, err := ParseChargeCredentialPayload(wireRoundTrip(t, map[string]any{
		"type": "zone", "hash": hash, "senderTag": tag, "zoneId": 3,
	}))
	require.NoError(t, err)
	assert.Equal(t, ChargeCredentialPayload{
		Type:      CredentialTypeZone,
		Hash:      strings.ToLower(hash),
		SenderTag: strings.ToLower(tag),
		ZoneID:    3,
	}, payload)
	again, err := ParseChargeCredentialPayload(wireRoundTrip(t, payload.Map()))
	require.NoError(t, err)
	assert.Equal(t, payload, again)

	for name, input := range map[string]map[string]any{
		"missing hash":      {"type": "zone", "senderTag": tag, "zoneId": 3},
		"short sender tag":  {"type": "zone", "hash": hash, "senderTag": "0x1234", "zoneId": 3},
		"missing zone":      {"type": "zone", "hash": hash, "senderTag": tag},
		"zero zone":         {"type": "zone", "hash": hash, "senderTag": tag, "zoneId": 0},
		"fractional zone":   {"type": "zone", "hash": hash, "senderTag": tag, "zoneId": 1.5},
		"zone out of range": {"type": "zone", "hash": hash, "senderTag": tag, "zoneId": int64(1 << 32)},
	} {
		_, err := ParseChargeCredentialPayload(wireRoundTrip(t, input))
		assert.Error(t, err, name)
	}
}

func TestZoneCredentialIsPush(t *testing.T) {
	t.Parallel()
	request := ChargeRequest{}
	assert.True(t, request.Allows(CredentialTypeZone))
	request.MethodDetails.SupportedModes = []ChargeMode{ChargeModePush}
	assert.True(t, request.Allows(CredentialTypeZone))
	request.MethodDetails.SupportedModes = []ChargeMode{ChargeModePull}
	assert.False(t, request.Allows(CredentialTypeZone))
}

func TestZoneAccepted(t *testing.T) {
	t.Parallel()
	assert.False(t, ZoneAccepted(nil, 3))
	assert.True(t, ZoneAccepted(&Zones{Any: true}, 3))
	assert.False(t, ZoneAccepted(&Zones{Any: true}, 0))
	assert.True(t, ZoneAccepted(&Zones{IDs: []uint32{3, 7}}, 7))
	assert.False(t, ZoneAccepted(&Zones{IDs: []uint32{3, 7}}, 4))
}

func TestChargeZoneWithdrawalStoreKey(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "mppx:charge:zone:0x5ad0000000000000000000000000000000000003:0x"+strings.Repeat("a1", 32),
		ChargeZoneWithdrawalStoreKey("0x5AD0000000000000000000000000000000000003", "0x"+strings.Repeat("A1", 32)))
}
