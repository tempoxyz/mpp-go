package mpp

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// Native Go fuzz targets for the wire parsers. Run one with, for example:
//
//	go test -run='^$' -fuzz='^FuzzParseChallenge$' -fuzztime=60s ./pkg/mpp
//
// Plain "go test" runs only the seed corpus below and in testdata/fuzz.

const (
	fuzzRequestB64 = "eyJhbW91bnQiOiIxMDAwMDAwIiwiY3VycmVuY3kiOiIweDIwYzAwMDAwMDAwMDAwMDAwMDAwMDAwMDAwMDAwMDAwMDAwMDAwMDAiLCJyZWNpcGllbnQiOiIweDEyMzQ1Njc4OTBhYmNkZWYxMjM0NTY3ODkwYWJjZGVmMTIzNDU2NzgifQ"
	fuzzChallenge  = `Payment id="XmJ98SdsAdzwP9Oa-8In322Uh6yweMO6rywdomWk_V4", realm="api.example.com", method="tempo", intent="charge", request="` + fuzzRequestB64 + `"`
)

func fuzzChallengeSeeds() []string {
	return []string{
		fuzzChallenge,
		fuzzChallenge + `, expires="2026-01-29T12:00:00Z", digest="sha-256=X48E9qOokqqrvdts8nOJRJN3OWDUoyWxBf7kbu9DBPE="`,
		fuzzChallenge + `, opaque="eyJyb3V0ZSI6InBhaWQiLCJ0cmFjZSI6IjEyMyJ9", header="Payment-Authorization"`,
		fuzzChallenge + `, description="Payment \u2014 coffee \u2615 \ud83d\ude00"`,
		fuzzChallenge + `, description="lone \ude00 here \\u2014 \u12"`,
		fuzzChallenge + ", " + fuzzChallenge,
		fuzzChallenge + `, Bearer realm="x", ` + fuzzChallenge,
		`Payment id="abc", realm="api.example.com", method="tempo", intent="charge", request="e30"`,
		`Payment id="abc", realm="api.example.com", method="tempo", intent="charge", intent="session", request="e30"`,
		`Payment id="abc", realm="api.example.com", method="tempo", intent="charge", request="e30", opaque="unterminated`,
		`Payment id="abc" realm="api.example.com", method="tempo", intent="charge", request="e30"`,
		`payment id=abc, realm=api.example.com, method=tempo, intent=charge, request=e30`,
		`Payment id="ch_special", realm="api.example.com", method="tempo", intent="charge", request="eyJhbW91bnQiOiIxMDAifQ", description="Payment for "Premium" service — 50% off!"`,
		"",
		"Payment ",
	}
}

// FuzzParseChallenge checks that WWW-Authenticate parsing never panics, that
// every challenge it accepts formats back into a header it accepts again with
// the same fields, and that SplitAuthenticate separates merged challenges.
func FuzzParseChallenge(f *testing.F) {
	for _, seed := range fuzzChallengeSeeds() {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, header string) {
		for _, part := range SplitAuthenticate(header) {
			if part == "" || part != strings.TrimSpace(part) {
				t.Fatalf("SplitAuthenticate(%q) returned untrimmed part %q", header, part)
			}
			_, _ = ParseChallenge(part)
		}

		c1, err := ParseChallenge(header)
		if err != nil {
			return
		}
		h1, err := FormatAuthenticateStrict(c1, c1.Realm)
		if err != nil {
			// Strict formatting refuses values a quoted auth-param cannot carry.
			return
		}
		c2, err := ParseChallenge(h1)
		if err != nil {
			t.Fatalf("ParseChallenge(FormatAuthenticateStrict(c)) failed: %v\ninput: %q\nformatted: %q", err, header, h1)
		}
		assertSameChallenge(t, c1, c2, header, h1)
		h2, err := FormatAuthenticateStrict(c2, c2.Realm)
		if err != nil || h1 != h2 {
			t.Fatalf("formatting is not stable: %q != %q (err %v)", h1, h2, err)
		}

		merged := SplitAuthenticate(h1 + ", " + h1)
		if len(merged) != 2 || merged[0] != h1 || merged[1] != h1 {
			t.Fatalf("SplitAuthenticate(two challenges) = %q, want two copies of %q", merged, h1)
		}
	})
}

func assertSameChallenge(t *testing.T, c1, c2 *Challenge, input, formatted string) {
	t.Helper()
	type fields struct {
		ID, Realm, Method, Intent, Digest, Expires, Description, Header string
		Opaque                                                          map[string]string
	}
	f1 := fields{c1.ID, c1.Realm, c1.Method, c1.Intent, c1.Digest, c1.Expires, c1.Description, c1.Header, c1.Opaque}
	f2 := fields{c2.ID, c2.Realm, c2.Method, c2.Intent, c2.Digest, c2.Expires, c2.Description, c2.Header, c2.Opaque}
	if !reflect.DeepEqual(f1, f2) {
		t.Fatalf("challenge changed across a round trip:\n%+v\n%+v\ninput: %q\nformatted: %q", f1, f2, input, formatted)
	}
	if !JSONEqual(c1.Request, c2.Request) {
		t.Fatalf("challenge request changed across a round trip: %v != %v\ninput: %q", c1.Request, c2.Request, input)
	}
}

// FuzzParseCredential checks Authorization parsing and that accepted
// credentials survive FormatAuthorization and a second parse unchanged.
func FuzzParseCredential(f *testing.F) {
	echo := ChallengeEcho{
		ID:      "challenge-id",
		Realm:   "api.example.com",
		Method:  "tempo",
		Intent:  "charge",
		Request: fuzzRequestB64,
		Expires: "2026-01-29T12:00:00Z",
		Opaque:  map[string]string{"trace": "123", "route": "paid"},
	}
	seed := &Credential{Challenge: echo, Payload: map[string]any{"type": "hash", "hash": "0xabc123"}, Source: "did:pkh:eip155:42431:0x1234"}
	f.Add(FormatAuthorization(seed))
	echo.Header = "Payment-Authorization"
	seed.Challenge = echo
	f.Add("Bearer token, " + FormatAuthorization(seed))
	f.Add("Payment " + base64.RawURLEncoding.EncodeToString([]byte(`{"challenge":{"id":"x","method":"tempo","intent":"charge","request":"e30","opaque":{"a":"b"}},"payload":{"n":12345678901234567890123}}`)))
	f.Add("Payment " + base64.RawURLEncoding.EncodeToString([]byte(`{"challenge":{"id":"x","method":"tempo","intent":"charge","request":"e30","opaque":"eyJhIjoiYiJ9"},"payload":{}}`)))
	f.Add("Payment abc, Payment def")
	f.Add("Payment e30")
	f.Add("")

	f.Fuzz(func(t *testing.T, header string) {
		_, _ = FindPaymentAuthorizationStrict(header)
		c1, err := ParseCredential(header)
		if err != nil {
			return
		}
		h1, err := FormatAuthorizationStrict(c1)
		if err != nil {
			return
		}
		c2, err := ParseCredential(h1)
		if err != nil {
			t.Fatalf("ParseCredential(FormatAuthorization(c)) failed: %v\ninput: %q\nformatted: %q", err, header, h1)
		}
		if !reflect.DeepEqual(c1.Challenge, c2.Challenge) || c1.Source != c2.Source || !JSONEqual(c1.Payload, c2.Payload) {
			t.Fatalf("credential changed across a round trip:\n%+v\n%+v\ninput: %q", c1, c2, header)
		}
		if h2 := FormatAuthorization(c2); h1 != h2 {
			t.Fatalf("formatting is not stable: %q != %q", h1, h2)
		}
	})
}

// FuzzParsePaymentReceipt checks Payment-Receipt parsing and that accepted
// receipts survive FormatPaymentReceipt and a second parse.
func FuzzParsePaymentReceipt(f *testing.F) {
	receipt := Success("tempo", "ref-123",
		WithExternalID("ext-123"),
		WithSubscriptionID("sub-123"),
		WithFundingCurrency("0x20c0000000000000000000000000000000000001"),
		WithExtra(map[string]any{"settled": true}),
	)
	f.Add(FormatPaymentReceipt(receipt))
	for _, raw := range []string{
		`{"status":"success","method":"tempo","timestamp":"2026-01-29T12:00:00Z","reference":"0xabc"}`,
		`{"status":"success","method":"tempo","timestamp":"2026-01-29T12:00:00.123456789+02:00","reference":"0xabc","txHash":"0x1","extra":{"n":1e400}}`,
		`{"status":"success","method":"stripe","timestamp":"0000-01-01T00:30:00+01:00","reference":"pi_1"}`,
		`{"status":"success","method":"tempo","timestamp":"2026-01-29T12:00:00Z","reference":"0xabc","extra":{},"channelId":"0x2","settled":{"amount":"1"}}`,
		`{"status":"failed","timestamp":"bad"}`,
	} {
		f.Add(base64.RawURLEncoding.EncodeToString([]byte(raw)))
	}
	f.Add("e30")
	f.Add("")

	f.Fuzz(func(t *testing.T, header string) {
		r1, err := ParsePaymentReceipt(header)
		if err != nil {
			return
		}
		// FormatPaymentReceipt writes four-digit UTC years; a timestamp whose
		// UTC instant falls outside years 0000-9999 cannot be re-emitted.
		if y := r1.Timestamp.UTC().Year(); y < 0 || y > 9999 {
			t.Skip("timestamp year outside RFC 3339 range after UTC conversion")
		}
		h1 := FormatPaymentReceipt(r1)
		r2, err := ParsePaymentReceipt(h1)
		if err != nil {
			t.Fatalf("ParsePaymentReceipt(FormatPaymentReceipt(r)) failed: %v\ninput: %q\nformatted: %q", err, header, h1)
		}
		if r1.Status != r2.Status || r1.Method != r2.Method || r1.Reference != r2.Reference ||
			r1.ExternalID != r2.ExternalID || r1.SubscriptionID != r2.SubscriptionID || r1.FundingCurrency != r2.FundingCurrency {
			t.Fatalf("receipt changed across a round trip:\n%+v\n%+v", r1, r2)
		}
		// FormatPaymentReceipt omits an empty extra object, so an empty map and
		// a missing one are the same receipt.
		if !JSONEqual(nonEmpty(r1.Extra), nonEmpty(r2.Extra)) || !JSONEqual(nonEmpty(r1.Extensions), nonEmpty(r2.Extensions)) {
			t.Fatalf("receipt extra or extensions changed across a round trip:\n%+v\n%+v", r1, r2)
		}
		if want := r1.Timestamp.UTC().Truncate(time.Millisecond); !want.Equal(r2.Timestamp) {
			t.Fatalf("receipt timestamp %v, want %v", r2.Timestamp, want)
		}
		if h2 := FormatPaymentReceipt(r2); h1 != h2 {
			t.Fatalf("formatting is not stable: %q != %q", h1, h2)
		}
	})
}

// nonEmpty returns m, or nil when m has no entries.
func nonEmpty(m map[string]any) map[string]any {
	if len(m) == 0 {
		return nil
	}
	return m
}

// FuzzB64Decode checks base64url JSON decoding and that decoded objects
// re-encode to an equivalent object.
func FuzzB64Decode(f *testing.F) {
	f.Add(fuzzRequestB64)
	f.Add("e30")
	f.Add("eyJhIjoxMjM0NTY3ODkwMTIzNDU2Nzg5MH0")
	f.Add(base64.RawURLEncoding.EncodeToString([]byte(`{"a":[1,2.5,-0,1e400,"\ud800"],"b":{"c":null}} {}`)))
	f.Add("e30=")
	f.Add("!!")

	f.Fuzz(func(t *testing.T, s string) {
		m1, err := B64Decode(s)
		if err != nil {
			return
		}
		encoded := b64EncodeAny(m1)
		m2, err := B64Decode(encoded)
		if err != nil {
			t.Fatalf("B64Decode(b64EncodeAny(m)) failed: %v\ninput: %q", err, s)
		}
		if !JSONEqual(m1, m2) {
			t.Fatalf("decoded value changed across a round trip: %v != %v", m1, m2)
		}
	})
}

// FuzzCanonicalJSON checks the RFC 8785 (JCS) encoding used to bind
// challenge requests: it never panics, emits valid UTF-8 JSON, and is
// idempotent.
func FuzzCanonicalJSON(f *testing.F) {
	for _, seed := range []string{
		`{"amount":"1000000","currency":"0x20c0","recipient":"0x1234"}`,
		`{"b":2,"a":1,"c":{"z":[3,2,1],"y":"\u00e9\u20ac\ud83d\ude00"}}`,
		`{"n":12345678901234567890,"f":1.5e-7,"z":-0,"big":1e21}`,
		`{"\u0000":"\u001f","esc":"<>&\u2028"}`,
		`[1,"two",null,true,{"k":{}}]`,
		`"text"`,
		`1e400`,
	} {
		f.Add([]byte(seed))
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		value, err := decodeJSONValue(data)
		if err != nil {
			return
		}
		c1, err := encodeCanonicalJSON(value)
		if err != nil {
			return
		}
		if !utf8.Valid(c1) || !json.Valid(c1) {
			t.Fatalf("canonical JSON is not valid UTF-8 JSON: %q", c1)
		}
		again, err := decodeJSONValue(c1)
		if err != nil {
			t.Fatalf("canonical JSON does not decode: %v: %q", err, c1)
		}
		c2, err := encodeCanonicalJSON(again)
		if err != nil {
			t.Fatalf("canonical JSON does not re-encode: %v: %q", err, c1)
		}
		if !bytes.Equal(c1, c2) {
			t.Fatalf("JCS is not idempotent:\n%q\n%q", c1, c2)
		}
		if !ChallengeBoundJSONEqual(value, again) {
			t.Fatalf("ChallengeBoundJSONEqual differs for a value and its canonical form: %q", c1)
		}
	})
}

// FuzzParseUnits checks ParseUnits against an exact big.Rat reference.
func FuzzParseUnits(f *testing.F) {
	for _, seed := range []struct {
		value    string
		decimals uint8
	}{
		{"1.5", 6}, {"0", 0}, {"100", 18}, {".5", 1}, {"5.", 2}, {".", 6},
		{"1.000000", 2}, {"1.0000001", 6}, {"-1", 6}, {" 2.5 ", 6},
		{"9223372036854775807", 0}, {"9223372036854775808", 0}, {"1e6", 0}, {"１", 0},
	} {
		f.Add(seed.value, seed.decimals)
	}

	f.Fuzz(func(t *testing.T, value string, decimals uint8) {
		// Real tokens use at most 18 decimals; bound it so the reference
		// arithmetic stays cheap.
		d := int(decimals % 40)
		got, err := ParseUnits(value, d)
		if err != nil {
			return
		}
		if got < 0 {
			t.Fatalf("ParseUnits(%q, %d) = %d, want non-negative", value, d, got)
		}
		want, ok := new(big.Rat).SetString(strings.TrimSpace(value))
		if !ok {
			t.Fatalf("ParseUnits(%q, %d) accepted a non-decimal value", value, d)
		}
		want.Mul(want, new(big.Rat).SetInt(new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(d)), nil)))
		if !want.IsInt() || want.Num().Cmp(big.NewInt(got)) != 0 {
			t.Fatalf("ParseUnits(%q, %d) = %d, want %s", value, d, got, want.RatString())
		}
	})
}
