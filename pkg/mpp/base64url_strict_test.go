package mpp

import (
	"encoding/base64"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestB64DecodeRejectsCharactersOutsideBase64URL(t *testing.T) {
	valid := base64.RawURLEncoding.EncodeToString([]byte(`{"a":1}`))
	_, err := B64Decode(valid)
	require.NoError(t, err)

	for _, input := range []string{
		valid[:3] + "\n" + valid[3:],
		valid[:3] + "\r\n" + valid[3:],
		valid[:3] + "\r" + valid[3:],
		valid + "\n",
		valid[:3] + " " + valid[3:],
		valid[:3] + "+" + valid[3:],
		valid[:3] + "/" + valid[3:],
	} {
		_, err := B64Decode(input)
		require.Error(t, err, "%q", input)
	}
}

func TestB64DecodeRejectsNonObjectJSON(t *testing.T) {
	for _, value := range []string{"[]", "1", `"s"`, "true"} {
		_, err := B64Decode(base64.RawURLEncoding.EncodeToString([]byte(value)))
		require.Error(t, err, value)
	}
}

func TestParseReceiptRejectsLineBreaksInBase64URL(t *testing.T) {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"method":"tempo","reference":"0xabc","status":"success","timestamp":"2025-01-01T00:00:00Z"}`))
	_, err := ParseReceipt(header)
	require.NoError(t, err)

	_, err = ParseReceipt(header[:4] + "\r\n" + header[4:])
	require.Error(t, err)
}

func TestParseChallengeRejectsNullRequest(t *testing.T) {
	_, err := ParseChallenge(`Payment id="0", realm="r", method="a", intent="a", request="bnVsbA"`)
	require.Error(t, err)
}

func TestNullChallengeRequestIsRejectedEverywhere(t *testing.T) {
	_, err := FormatChallenge(&Challenge{ID: "0", Realm: "r", Method: "a", Intent: "a", RequestB64: "bnVsbA"})
	require.Error(t, err)

	var c Challenge
	require.Error(t, json.Unmarshal([]byte(`{"id":"0","realm":"r","method":"a","intent":"a","request":"bnVsbA"}`), &c))
	require.Error(t, json.Unmarshal([]byte(`{"id":"0","realm":"r","method":"a","intent":"a","requestB64":"bnVsbA"}`), &c))
}

func TestParseChallengeKeepsNullOpaque(t *testing.T) {
	// Only the request must be an object: an opaque of null still parses, as
	// it did before, and as mppx, which does not decode opaque.
	_, err := ParseChallenge(`Payment id="0", realm="r", method="a", intent="a", request="e30", opaque="bnVsbA"`)
	require.NoError(t, err)
}

func TestParseChallengeRejectsLineBreaksInRequest(t *testing.T) {
	// A real LF inside the quoted request: encoding/base64 skipped it, so
	// "e3<LF>0" decoded as "e30" and the Challenge parsed.
	_, err := ParseChallenge("Payment id=\"0\", realm=\"r\", method=\"a\", intent=\"a\", request=\"e3\n0\"")
	require.Error(t, err)
}
