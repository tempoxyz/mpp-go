package mpp

import (
	"encoding/base64"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReceiptExternalIDRoundTrip(t *testing.T) {
	t.Parallel()

	const base = `"method":"stripe","reference":"pi_test","status":"success","timestamp":"2026-01-01T00:00:00.000Z"`
	tests := []struct {
		name     string
		member   string
		want     string
		wantSent bool
	}{
		{name: "absent", member: ""},
		{name: "empty", member: `,"externalId":""`, want: "", wantSent: true},
		{name: "value", member: `,"externalId":"order-1"`, want: "order-1", wantSent: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			raw := "{" + base + tt.member + "}"
			receipt, err := ParseReceipt(base64.RawURLEncoding.EncodeToString([]byte(raw)))
			require.NoError(t, err)
			assert.Equal(t, tt.want, receipt.ExternalID)

			header, err := FormatReceiptWithError(receipt)
			require.NoError(t, err)
			decoded, err := B64Decode(header)
			require.NoError(t, err)
			got, sent := decoded["externalId"]
			assert.Equal(t, tt.wantSent, sent)
			if tt.wantSent {
				assert.Equal(t, tt.want, got)
			}

			// MCP receipts keep the field the same way.
			var mcpReceipt MCPReceipt
			require.NoError(t, json.Unmarshal([]byte("{"+base+tt.member+`,"challengeId":"abc"}`), &mcpReceipt))
			encoded, err := json.Marshal(mcpReceipt)
			require.NoError(t, err)
			var fields map[string]any
			require.NoError(t, json.Unmarshal(encoded, &fields))
			got, sent = fields["externalId"]
			assert.Equal(t, tt.wantSent, sent)
			if tt.wantSent {
				assert.Equal(t, tt.want, got)
			}
		})
	}
}

func TestReceiptWithoutExternalIDOmitsIt(t *testing.T) {
	t.Parallel()

	for _, receipt := range []*Receipt{
		Success("stripe", "pi_test"),
		Success("stripe", "pi_test", WithExternalID("")),
	} {
		header, err := FormatReceiptWithError(receipt)
		require.NoError(t, err)
		decoded, err := B64Decode(header)
		require.NoError(t, err)
		assert.NotContains(t, decoded, "externalId")
	}
}
