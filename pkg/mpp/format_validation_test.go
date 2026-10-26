package mpp

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFormatReceiptWithErrorRejectsWhatParseReceiptRejects(t *testing.T) {
	valid := func() *Receipt { return Success("tempo", "0xreceipt") }

	header, err := FormatReceiptWithError(valid())
	require.NoError(t, err)
	_, err = ParseReceipt(header)
	require.NoError(t, err)

	for _, status := range []string{"failed", "pending", "Success"} {
		r := valid()
		r.Status = status
		_, err := FormatReceiptWithError(r)
		assert.ErrorContains(t, err, "invalid receipt status", status)
		// ParseReceipt rejects the same receipt.
		_, err = ParseReceipt(b64EncodeAny(receiptFields(r)))
		assert.Error(t, err, status)
	}

	big := valid()
	big.Extensions = map[string]any{"blob": strings.Repeat("x", maxHeaderPayload)}
	_, err = FormatReceiptWithError(big)
	assert.ErrorContains(t, err, "exceeds maximum size")
	_, err = ParseReceipt(b64EncodeAny(receiptFields(big)))
	assert.Error(t, err)
}

func TestFormatChallengeRejectsWhatParseChallengeRejects(t *testing.T) {
	valid := func() *Challenge {
		return &Challenge{
			ID:      "id",
			Realm:   "api.example.com",
			Method:  "tempo",
			Intent:  IntentCharge,
			Request: map[string]any{"amount": "1"},
			Expires: ExpiresIn(time.Minute),
		}
	}
	header, err := FormatChallenge(valid())
	require.NoError(t, err)
	_, err = ParseChallenge(header)
	require.NoError(t, err)

	for _, method := range []string{"Tempo", "tempo-v2", "tempo charge", "t3mpo"} {
		c := valid()
		c.Method = method
		_, err := FormatChallenge(c)
		assert.ErrorContains(t, err, "invalid challenge method", method)
		// The lenient deprecated formatter still emits it; ParseChallenge rejects it.
		_, err = ParseChallenge(formatAuthenticateUnchecked(c, c.Realm))
		assert.Error(t, err, method)
	}

	c := valid()
	c.RequestB64 = "not base64!"
	_, err = FormatChallenge(c)
	assert.ErrorContains(t, err, "invalid challenge request")
	_, err = ParseChallenge(formatAuthenticateUnchecked(c, c.Realm))
	assert.Error(t, err)
}

func TestFormatCredentialRejectsWhatParseCredentialRejects(t *testing.T) {
	valid := func() *Credential {
		return &Credential{
			Challenge: ChallengeEcho{ID: "id", Realm: "api.example.com", Method: "tempo", Intent: IntentCharge, Request: "e30"},
			Payload:   map[string]any{"type": "hash", "hash": "0xabc"},
		}
	}
	header, err := FormatCredential(valid())
	require.NoError(t, err)
	_, err = ParseCredential(header)
	require.NoError(t, err)

	empty := valid()
	empty.Payload = map[string]any{}
	header, err = FormatCredential(empty)
	require.NoError(t, err, "an empty payload object is valid")
	_, err = ParseCredential(header)
	require.NoError(t, err)

	cases := map[string]func(*Credential){
		"missing required field: id":          func(c *Credential) { c.Challenge.ID = "" },
		"missing required field: method":      func(c *Credential) { c.Challenge.Method = "" },
		"invalid credential challenge method": func(c *Credential) { c.Challenge.Method = "Tempo" },
		"missing required field: intent":      func(c *Credential) { c.Challenge.Intent = "" },
		"missing required field: request":     func(c *Credential) { c.Challenge.Request = "" },
		"invalid HTTP header name":            func(c *Credential) { c.Challenge.Header = "Bad Header" },
		"payload must be an object":           func(c *Credential) { c.Payload = nil },
	}
	for want, mutate := range cases {
		c := valid()
		mutate(c)
		_, err := FormatCredential(c)
		assert.ErrorContains(t, err, want)
		// The lenient deprecated formatter still emits it; ParseCredential rejects it.
		_, err = ParseCredential(formatAuthorizationUnchecked(c))
		assert.Error(t, err, want)
	}
}
