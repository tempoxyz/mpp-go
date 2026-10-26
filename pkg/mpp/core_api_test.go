package mpp

import (
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestChallenge(t *testing.T, opts ...ChallengeOption) *Challenge {
	t.Helper()
	challenge, err := NewChallengeWithError("secret", "api.example.com", "tempo", IntentCharge,
		map[string]any{"amount": "1000", "currency": "0x20c0"}, opts...)
	require.NoError(t, err)
	return challenge
}

func TestFormatChallengeRoundTripsWithChallengeRealm(t *testing.T) {
	t.Parallel()

	challenge := newTestChallenge(t, WithExpires("2026-01-01T00:00:00.000Z"), WithMeta(map[string]string{"k": "v"}))
	header, err := FormatChallenge(challenge)
	require.NoError(t, err)
	assert.Contains(t, header, `realm="api.example.com"`)

	parsed, err := ParseChallenge(header)
	require.NoError(t, err)
	assert.Equal(t, challenge.ID, parsed.ID)
	assert.Equal(t, challenge.Realm, parsed.Realm)
	assert.Equal(t, challenge.Opaque, parsed.Opaque)
	assert.True(t, parsed.VerifyID("secret"))

	again, err := FormatChallenge(parsed)
	require.NoError(t, err)
	assert.Equal(t, header, again)
}

func TestFormatChallengeErrors(t *testing.T) {
	t.Parallel()

	_, err := FormatChallenge(nil)
	assert.Error(t, err)

	noRealm := *newTestChallenge(t)
	noRealm.Realm = ""
	_, err = FormatChallenge(&noRealm)
	assert.ErrorContains(t, err, "realm")

	crlf := *newTestChallenge(t)
	crlf.Description = "a\r\nb"
	_, err = FormatChallenge(&crlf)
	assert.ErrorContains(t, err, "CR or LF")

	badHeader := *newTestChallenge(t)
	badHeader.Header = "bad header"
	_, err = FormatChallenge(&badHeader)
	assert.Error(t, err)
}

func TestVerifyIDUsesChallengeRealm(t *testing.T) {
	t.Parallel()

	challenge := newTestChallenge(t)
	assert.True(t, challenge.VerifyID("secret"))
	assert.False(t, challenge.VerifyID("other"))

	moved := *challenge
	moved.Realm = "evil.example.com"
	assert.False(t, moved.VerifyID("secret"))
}

func TestFormatCredentialRoundTrips(t *testing.T) {
	t.Parallel()

	credential := newTestChallenge(t).NewCredential(map[string]any{"type": "hash", "hash": "0xabc"},
		WithCredentialSource("did:pkh:eip155:4217:0x1"))
	header, err := FormatCredential(credential)
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(header, SchemePayment+" "))

	parsed, err := ParseCredential(header)
	require.NoError(t, err)
	assert.Equal(t, credential.Challenge.ID, parsed.Challenge.ID)
	assert.Equal(t, credential.Source, parsed.Source)
	assert.Equal(t, "0xabc", parsed.Payload["hash"])

	_, err = FormatCredential(nil)
	assert.Error(t, err)

	big := newTestChallenge(t).NewCredential(map[string]any{"blob": strings.Repeat("a", maxAuthorizationPayload)})
	_, err = FormatCredential(big)
	assert.ErrorContains(t, err, "maximum size")
}

func TestFormatReceiptWithErrorRoundTrips(t *testing.T) {
	t.Parallel()

	receipt := Success("tempo", "0xtx", WithExternalID("ext"),
		WithExtensions(map[string]any{"network": "mainnet", "status": "ignored"}))
	header, err := FormatReceiptWithError(receipt)
	require.NoError(t, err)

	parsed, err := ParseReceipt(header)
	require.NoError(t, err)
	assert.Equal(t, "success", parsed.Status)
	assert.Equal(t, "tempo", parsed.Method)
	assert.Equal(t, "0xtx", parsed.Reference)
	assert.Equal(t, "ext", parsed.ExternalID)
	assert.Equal(t, map[string]any{"network": "mainnet"}, parsed.Extensions)
	assert.True(t, receipt.Timestamp.Truncate(time.Millisecond).Equal(parsed.Timestamp))
}

func TestFormatReceiptWithErrorRejectsUnparseableReceipts(t *testing.T) {
	t.Parallel()

	for name, receipt := range map[string]*Receipt{
		"nil":               nil,
		"missing status":    {Reference: "0x1", Method: "tempo"},
		"missing reference": {Status: "success", Method: "tempo"},
		"missing method":    {Status: "success", Reference: "0x1"},
		"invalid method":    {Status: "success", Reference: "0x1", Method: "Tempo"},
		"unencodable":       {Status: "success", Reference: "0x1", Method: "tempo", Extensions: map[string]any{"f": func() {}}},
	} {
		_, err := FormatReceiptWithError(receipt)
		assert.Error(t, err, name)
	}
}

func TestExpiresIn(t *testing.T) {
	t.Parallel()

	before := time.Now().Add(5 * time.Minute).Truncate(time.Millisecond)
	expires := ExpiresIn(5 * time.Minute)
	after := time.Now().Add(5 * time.Minute)
	assert.Regexp(t, `^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z$`, expires)

	parsed, err := ParseExpires(expires)
	require.NoError(t, err)
	assert.False(t, parsed.Before(before))
	assert.False(t, parsed.After(after))
}

func TestParseExpires(t *testing.T) {
	t.Parallel()

	for value, want := range map[string]time.Time{
		"2026-01-29T12:00:00Z":           time.Date(2026, 1, 29, 12, 0, 0, 0, time.UTC),
		"2026-01-29T12:00:00.123Z":       time.Date(2026, 1, 29, 12, 0, 0, 123e6, time.UTC),
		"2026-01-29T13:00:00.5+01:00":    time.Date(2026, 1, 29, 12, 0, 0, 5e8, time.UTC),
		"2026-01-29T12:00:00.123456789Z": time.Date(2026, 1, 29, 12, 0, 0, 123456789, time.UTC),
	} {
		got, err := ParseExpires(value)
		require.NoError(t, err, value)
		assert.True(t, want.Equal(got), value)
	}
	for _, value := range []string{"", "2026-01-29 12:00:00Z", "1769688000", "2026-02-30T12:00:00Z"} {
		_, err := ParseExpires(value)
		assert.Error(t, err, value)
	}
}

func TestCheckExpires(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 1, 29, 12, 0, 0, 0, time.UTC)
	assert.NoError(t, CheckExpires("2026-01-29T12:00:00.001Z", now))

	err := CheckExpires("2026-01-29T12:00:00Z", now)
	assert.True(t, errors.Is(err, ErrorTypePaymentExpired), "expired at the exact instant")
	err = CheckExpires("2026-01-29T11:00:00Z", now)
	assert.True(t, errors.Is(err, ErrorTypePaymentExpired))

	err = CheckExpires("", now)
	assert.True(t, errors.Is(err, ErrorTypeInvalidChallenge))
	assert.ErrorContains(t, err, "missing")
	err = CheckExpires("tomorrow", now)
	assert.True(t, errors.Is(err, ErrorTypeInvalidChallenge))
	assert.ErrorContains(t, err, "malformed")
}

func TestComputeAndVerifyBodyDigest(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "sha-256=47DEQpj8HBSa+/TImW+5JCeuQeRkm5NMpJWZG3hSuFU=", ComputeBodyDigest(nil))
	body := []byte(`{"amount":"1"}`)
	digest := ComputeBodyDigest(body)
	assert.Equal(t, BodyDigest.Compute(body), digest)
	assert.True(t, VerifyBodyDigest(digest, body))
	assert.False(t, VerifyBodyDigest(digest, []byte(`{"amount":"2"}`)))
	assert.False(t, VerifyBodyDigest("", body))
}

func TestParseAmount(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		value    string
		decimals int
		want     string
	}{
		{"1.5", 6, "1500000"},
		{"0.000001", 6, "1"},
		{".5", 1, "5"},
		{"1.", 2, "100"},
		{"1.500", 1, "15"},
		{"0", 18, "0"},
		{"1000000.5", 18, "1000000500000000000000000"},
		{" 42 ", 0, "42"},
	} {
		got, err := ParseAmount(tc.value, tc.decimals)
		require.NoError(t, err, tc.value)
		want, _ := new(big.Int).SetString(tc.want, 10)
		assert.Equal(t, 0, want.Cmp(got), "%s: got %s", tc.value, got)
	}
	for _, tc := range []struct {
		value    string
		decimals int
	}{
		{"", 6}, {"-1", 6}, {".", 6}, {"1.0000001", 6}, {"1", -1}, {"1e5", 6}, {"1.2.3", 6}, {"+1", 6},
	} {
		_, err := ParseAmount(tc.value, tc.decimals)
		assert.Error(t, err, tc.value)
	}

	_, err := ParseUnits("1000000.5", 18)
	assert.ErrorContains(t, err, "overflows int64")
}

func TestIntentConstants(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "charge", IntentCharge)
	assert.Equal(t, "session", IntentSession)
}

func TestReceiptFromResponse(t *testing.T) {
	t.Parallel()

	header, err := FormatReceiptWithError(Success("tempo", "0xtx"))
	require.NoError(t, err)
	resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{}}
	resp.Header.Set(HeaderPaymentReceipt, header)
	receipt, err := ReceiptFromResponse(resp)
	require.NoError(t, err)
	assert.Equal(t, "0xtx", receipt.Reference)

	_, err = ReceiptFromResponse(&http.Response{StatusCode: http.StatusOK, Header: http.Header{}})
	assert.ErrorContains(t, err, "missing")
	_, err = ReceiptFromResponse(nil)
	assert.Error(t, err)
	resp.Header.Set(HeaderPaymentReceipt, "not-a-receipt")
	_, err = ReceiptFromResponse(resp)
	assert.Error(t, err)
}

func TestChallengesFromResponse(t *testing.T) {
	t.Parallel()

	first, err := FormatChallenge(newTestChallenge(t))
	require.NoError(t, err)
	stripe, err := NewChallengeWithError("secret", "api.example.com", "stripe", IntentCharge, map[string]any{"amount": "1"})
	require.NoError(t, err)
	second, err := FormatChallenge(stripe)
	require.NoError(t, err)
	third, err := FormatChallenge(newTestChallenge(t, WithDescription("a, b")))
	require.NoError(t, err)

	resp := &http.Response{StatusCode: http.StatusPaymentRequired, Header: http.Header{}}
	resp.Header.Add(HeaderWWWAuthenticate, `Bearer realm="api"`)
	resp.Header.Add(HeaderWWWAuthenticate, first+", "+second)
	resp.Header.Add(HeaderWWWAuthenticate, `Basic realm="x", `+third)

	challenges, err := ChallengesFromResponse(resp)
	require.NoError(t, err)
	require.Len(t, challenges, 3)
	assert.Equal(t, "tempo", challenges[0].Method)
	assert.Equal(t, "stripe", challenges[1].Method)
	assert.Equal(t, "a, b", challenges[2].Description)

	resp.StatusCode = http.StatusOK
	_, err = ChallengesFromResponse(resp)
	assert.ErrorContains(t, err, "402")

	_, err = ChallengesFromResponse(nil)
	assert.Error(t, err)

	none := &http.Response{StatusCode: http.StatusPaymentRequired, Header: http.Header{}}
	none.Header.Set(HeaderWWWAuthenticate, `Bearer realm="api"`)
	_, err = ChallengesFromResponse(none)
	assert.ErrorContains(t, err, "missing Payment challenge")

	bad := &http.Response{StatusCode: http.StatusPaymentRequired, Header: http.Header{}}
	bad.Header.Set(HeaderWWWAuthenticate, `Payment id="x"`)
	_, err = ChallengesFromResponse(bad)
	assert.Error(t, err)
}

func TestPaymentErrorIsErrorType(t *testing.T) {
	t.Parallel()

	err := fmt.Errorf("charge: %w", ErrMalformedCredential("bad base64"))
	assert.True(t, errors.Is(err, ErrorTypeMalformedCredential))
	assert.False(t, errors.Is(err, ErrorTypeVerificationFailed))
	assert.Equal(t, string(ErrorTypeMalformedCredential), ErrorTypeMalformedCredential.Error())

	cause := errors.New("rpc down")
	wrapped := fmt.Errorf("wrap: %w", ErrVerificationFailed("x").WithCause(cause))
	assert.True(t, errors.Is(wrapped, ErrorTypeVerificationFailed))
	assert.True(t, errors.Is(wrapped, cause))
	assert.True(t, errors.Is(ErrVerificationFailed("y"), ErrVerification))
	assert.False(t, errors.Is(errors.New("plain"), ErrorTypeVerificationFailed))
}
