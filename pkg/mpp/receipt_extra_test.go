package mpp

import (
	"encoding/base64"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestReceiptExtraIsAnOrdinaryExtension round-trips the mpp-tools
// receipt/success_receipt vectors with a top-level "extra" field added: it is
// kept in Extensions["extra"] and formatted back at the top level, so the wire
// is unchanged. An empty "extra" object is dropped, as before.
func TestReceiptExtraIsAnOrdinaryExtension(t *testing.T) {
	t.Parallel()

	base := func() map[string]any {
		return map[string]any{
			"method":    "tempo",
			"reference": "0xabcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890ab",
			"status":    "success",
			"timestamp": "2026-01-29T12:00:30.000Z",
		}
	}
	extensionFields := func() map[string]any {
		object := base()
		object["fundingCurrency"] = "0x20c0000000000000000000006a37DA5C996874BE"
		object["originTxHash"] = "0x9bcff372aee89b648c922b850573b22387c31d693079f5e37cd255814e2d615a"
		object["subscriptionId"] = "sub_123"
		return object
	}

	for _, tt := range []struct {
		name   string
		object map[string]any
		extra  any
	}{
		{name: "success_receipt", object: base(), extra: map[string]any{"settled": true, "nested": map[string]any{"n": "1"}}},
		{name: "success_receipt_extension_fields", object: extensionFields(), extra: map[string]any{"settled": true}},
		{name: "non-object extra", object: base(), extra: "note"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			tt.object["extra"] = tt.extra
			wire := b64EncodeAny(tt.object)

			parsed, err := ParseReceipt(wire)
			require.NoError(t, err)
			assert.Equal(t, tt.extra, parsed.Extensions["extra"])

			formatted, err := FormatReceiptWithError(parsed)
			require.NoError(t, err)
			assert.Equal(t, wire, formatted)

			again, err := ParseReceipt(formatted)
			require.NoError(t, err)
			assert.Equal(t, parsed.Extensions, again.Extensions)
		})
	}

	t.Run("empty extra is dropped", func(t *testing.T) {
		t.Parallel()
		object := base()
		object["extra"] = map[string]any{}
		parsed, err := ParseReceipt(b64EncodeAny(object))
		require.NoError(t, err)
		assert.NotContains(t, parsed.Extensions, "extra")

		formatted, err := FormatReceiptWithError(parsed)
		require.NoError(t, err)
		assert.Equal(t, b64EncodeAny(base()), formatted)

		parsed.Extensions = map[string]any{"extra": map[string]any{}}
		formatted, err = FormatReceiptWithError(parsed)
		require.NoError(t, err)
		decoded, err := base64.RawURLEncoding.DecodeString(formatted)
		require.NoError(t, err)
		assert.NotContains(t, string(decoded), "extra")
	})

	type namedMap map[string]int
	for _, tt := range []struct {
		name  string
		extra any
		empty bool
	}{
		{name: "map[string]string{}", extra: map[string]string{}, empty: true},
		{name: "named map type", extra: namedMap{}, empty: true},
		{name: "json.RawMessage {}", extra: json.RawMessage("{}"), empty: true},
		{name: "json.RawMessage spaced", extra: json.RawMessage(" { } "), empty: true},
		{name: "non-empty map[string]string", extra: map[string]string{"settled": "true"}},
		{name: "non-empty json.RawMessage", extra: json.RawMessage(`{"settled":true}`)},
	} {
		t.Run("format "+tt.name, func(t *testing.T) {
			t.Parallel()
			parsed, err := ParseReceipt(b64EncodeAny(base()))
			require.NoError(t, err)
			parsed.Extensions = map[string]any{"extra": tt.extra}

			formatted, err := FormatReceiptWithError(parsed)
			require.NoError(t, err)
			reparsed, err := ParseReceipt(formatted)
			require.NoError(t, err)
			if tt.empty {
				assert.Equal(t, b64EncodeAny(base()), formatted)
				assert.NotContains(t, reparsed.Extensions, "extra")
			} else {
				assert.Contains(t, reparsed.Extensions, "extra")
			}

			// Parse/format is stable from here on.
			again, err := FormatReceiptWithError(reparsed)
			require.NoError(t, err)
			assert.Equal(t, formatted, again)
		})
	}
}
