package mpp

import (
	"errors"
	"github.com/stretchr/testify/assert"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestPaymentErrorConstructors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		err    *PaymentError
		want   ErrorType
		status int
		detail string
	}{
		{
			name:   "payment required default detail",
			err:    ErrPaymentRequired("api.example.com", ""),
			want:   ErrorTypePaymentRequired,
			status: http.StatusPaymentRequired,
			detail: "Payment is required to access this resource",
		},
		{
			name:   "payment required custom detail",
			err:    ErrPaymentRequired("api.example.com", "custom detail"),
			want:   ErrorTypePaymentRequired,
			status: http.StatusPaymentRequired,
			detail: "custom detail",
		},
		{
			name:   "malformed credential",
			err:    ErrMalformedCredential("bad credential"),
			want:   ErrorTypeMalformedCredential,
			status: http.StatusPaymentRequired,
			detail: "bad credential",
		},
		{
			name:   "invalid challenge",
			err:    ErrInvalidChallenge("challenge-1", "tampered"),
			want:   ErrorTypeInvalidChallenge,
			status: http.StatusPaymentRequired,
			detail: "challenge challenge-1: tampered",
		},
		{
			name:   "verification failed",
			err:    ErrVerificationFailed("signature mismatch"),
			want:   ErrorTypeVerificationFailed,
			status: http.StatusPaymentRequired,
			detail: "signature mismatch",
		},
		{
			name:   "payment expired",
			err:    ErrPaymentExpired("2026-01-01T00:00:00Z"),
			want:   ErrorTypePaymentExpired,
			status: http.StatusPaymentRequired,
			detail: "payment expired at 2026-01-01T00:00:00Z",
		},
		{
			name:   "invalid payload",
			err:    ErrInvalidPayload("invalid payload"),
			want:   ErrorTypeInvalidPayload,
			status: http.StatusBadRequest,
			detail: "invalid payload",
		},
		{
			name:   "bad request",
			err:    ErrBadRequest("bad request"),
			want:   ErrorTypeBadRequest,
			status: http.StatusBadRequest,
			detail: "bad request",
		},
		{
			name:   "payment insufficient",
			err:    ErrPaymentInsufficient("not enough"),
			want:   ErrorTypePaymentInsufficient,
			status: http.StatusPaymentRequired,
			detail: "not enough",
		},
		{
			name:   "method unsupported",
			err:    ErrMethodUnsupported("stripe"),
			want:   ErrorTypeMethodUnsupported,
			status: http.StatusBadRequest,
			detail: `payment method "stripe" is not supported`,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if !assert.Equalf(t, tt.want, tt.err.Type,
				"err.Type = %q, want %q", tt.err.Type, tt.want) {
				return
			}
			if !assert.Equalf(t, tt.status, tt.err.Status,
				"err.Status = %d, want %d", tt.err.Status, tt.status) {
				return
			}
			if !assert.Equalf(t, tt.detail, tt.err.Detail,
				"err.Detail = %q, want %q", tt.err.Detail, tt.detail) {
				return
			}

		})
	}
}

func TestProblemTypeURIsUseCanonicalBase(t *testing.T) {
	t.Parallel()

	tests := []struct {
		got  ErrorType
		want string
	}{
		{got: ErrorTypePaymentRequired, want: "https://paymentauth.org/problems/payment-required"},
		{got: ErrorTypeMalformedCredential, want: "https://paymentauth.org/problems/malformed-credential"},
		{got: ErrorTypeInvalidChallenge, want: "https://paymentauth.org/problems/invalid-challenge"},
		{got: ErrorTypeVerificationFailed, want: "https://paymentauth.org/problems/verification-failed"},
		{got: ErrorTypePaymentExpired, want: "https://paymentauth.org/problems/payment-expired"},
		{got: ErrorTypePaymentInsufficient, want: "https://paymentauth.org/problems/payment-insufficient"},
		{got: ErrorTypeMethodUnsupported, want: "https://paymentauth.org/problems/method-unsupported"},
	}

	for _, tt := range tests {
		if !assert.Equalf(t, tt.want, string(tt.got),
			"problem type URI = %q, want %q", tt.got, tt.want) {
			return
		}
	}
}

func TestPaymentErrorHelpers(t *testing.T) {
	t.Parallel()

	err := ErrVerificationFailed("signature mismatch")
	if got := err.Error(); got != "Verification Failed: signature mismatch" {
		assert.Failf(t, "", "err.Error() = %q, want %q", got, "Verification Failed: signature mismatch")
		return
	}
	if !assert.True(t, errors.Is(err, ErrVerification),
		"errors.Is(err, ErrVerification) = false, want true") {
		return
	}

	problem := err.ProblemDetails("challenge-1")
	if !assert.Equalf(t, err.Type, problem["type"],
		"problem[type] = %#v, want %q", problem["type"], err.Type) {
		return
	}
	if !assert.Equalf(t, "challenge-1", problem["challengeId"],
		"problem[challengeId] = %#v, want %q", problem["challengeId"], "challenge-1") {
		return
	}

	nonVerification := ErrBadRequest("invalid")
	if !assert.False(t, errors.Is(nonVerification, ErrVerification),
		"errors.Is(nonVerification, ErrVerification) = true, want false") {
		return
	}

	if got := nonVerification.ProblemDetails(""); got["challengeId"] != nil {
		assert.Failf(t, "", "problem[challengeId] = %#v, want nil", got["challengeId"])
		return
	}
}

func TestPaymentErrorHints(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  *PaymentError
		hint string
	}{
		{
			name: "payment required has hint",
			err:  ErrPaymentRequired("api.example.com", ""),
			hint: HintPaymentRequired,
		},
		{
			name: "malformed credential has hint",
			err:  ErrMalformedCredential("bad"),
			hint: HintMalformedCredential,
		},
		{
			name: "method unsupported has hint",
			err:  ErrMethodUnsupported("stripe"),
			hint: HintMethodUnsupported,
		},
		{
			name: "verification failed has no hint",
			err:  ErrVerificationFailed("sig mismatch"),
			hint: "",
		},
		{
			name: "bad request has no hint",
			err:  ErrBadRequest("invalid"),
			hint: "",
		},
		{
			name: "payment expired has no hint",
			err:  ErrPaymentExpired("2026-01-01T00:00:00Z"),
			hint: "",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equalf(t, tt.hint, tt.err.Hint,
				"err.Hint = %q, want %q", tt.err.Hint, tt.hint)

			problem := tt.err.ProblemDetails("")
			if tt.hint != "" {
				assert.Equalf(t, tt.hint, problem["hint"],
					"problem[hint] = %q, want %q", problem["hint"], tt.hint)
			} else {
				assert.Nilf(t, problem["hint"],
					"problem[hint] = %v, want nil", problem["hint"])
			}
		})
	}
}

func TestReceiptRoundTrip(t *testing.T) {
	t.Parallel()

	receipt := Success(
		"ref-123",
		WithReceiptMethod("tempo"),
		WithExternalID("ext-123"),
		WithExtra(map[string]any{"settled": true}),
	)
	if !assert.Equalf(t, "success", receipt.Status,
		"receipt.Status = %q, want %q", receipt.Status, "success") {
		return
	}
	if !assert.Equalf(t, time.UTC, receipt.Timestamp.Location(),
		"receipt.Timestamp.Location() = %v, want UTC", receipt.Timestamp.Location()) {
		return
	}

	header := FormatReceipt(receipt)
	parsed, err := ParseReceipt(header)
	if !assert.NoErrorf(t, err,
		"ParseReceipt() error = %v", err) {
		return
	}
	if !assert.Equalf(t, receipt.Reference, parsed.Reference,
		"parsed.Reference = %q, want %q", parsed.Reference, receipt.Reference) {
		return
	}
	if !assert.Equalf(t, receipt.Method, parsed.Method,
		"parsed.Method = %q, want %q", parsed.Method, receipt.Method) {
		return
	}
	if !assert.Equalf(t, receipt.ExternalID, parsed.ExternalID,
		"parsed.ExternalID = %q, want %q", parsed.ExternalID, receipt.ExternalID) {
		return
	}
	if !assert.Equalf(t, true, parsed.Extra["settled"],
		"parsed.Extra[settled] = %#v, want true", parsed.Extra["settled"]) {
		return
	}

	if got, want := parsed.Timestamp.Format("2006-01-02T15:04:05.000Z"), receipt.Timestamp.Format("2006-01-02T15:04:05.000Z"); got != want {
		assert.Failf(t, "", "parsed.Timestamp = %q, want %q", got, want)
		return
	}
}

func TestParsePaymentReceiptValidation(t *testing.T) {
	t.Parallel()

	_, err := ParseReceipt(b64EncodeAny(map[string]any{
		"status":    "pending",
		"timestamp": "2026-01-01T00:00:00Z",
		"reference": "ref-123",
	}))
	if !assert.Falsef(t, err == nil || !strings.Contains(err.Error(), "invalid receipt status"),
		"ParseReceipt() error = %v, want invalid status error", err) {
		return
	}

	_, err = ParseReceipt(b64EncodeAny(map[string]any{
		"status": "success",
	}))
	if !assert.Falsef(t, err == nil || !strings.Contains(err.Error(), "receipt missing reference"),
		"ParseReceipt() error = %v, want missing reference error", err) {
		return
	}

	_, err = ParseReceipt(b64EncodeAny(map[string]any{
		"status":    "success",
		"method":    "Tempo",
		"timestamp": "2026-01-01T00:00:00Z",
		"reference": "ref-123",
	}))
	if !assert.Falsef(t, err == nil || !strings.Contains(err.Error(), "invalid receipt method"),
		"ParseReceipt() error = %v, want invalid method error", err) {
		return
	}

	_, err = ParseReceipt(b64EncodeAny(map[string]any{
		"status":    "success",
		"method":    "tempo2",
		"timestamp": "2026-01-01T00:00:00Z",
		"reference": "ref-123",
	}))
	if !assert.Falsef(t, err == nil || !strings.Contains(err.Error(), "invalid receipt method"),
		"ParseReceipt() error = %v, want invalid method error", err) {
		return
	}

	_, err = ParseReceipt(b64EncodeAny(map[string]any{
		"status":    "success",
		"method":    "tempo.pay",
		"timestamp": "2026-01-01T00:00:00Z",
		"reference": "ref-123",
	}))
	if !assert.Falsef(t, err == nil || !strings.Contains(err.Error(), "invalid receipt method"),
		"ParseReceipt() error = %v, want invalid method error", err) {
		return
	}

}

func TestChallengeVerifyAndToEcho(t *testing.T) {
	t.Parallel()

	challenge := NewChallenge(
		"secret-key",
		"api.example.com",
		"tempo",
		"charge",
		map[string]any{"amount": "100"},
		WithExpires("2026-01-01T00:00:00.000Z"),
	)
	if !assert.True(t, challenge.Verify("secret-key", "api.example.com"),
		"challenge.Verify(secret-key, api.example.com) = false, want true") {
		return
	}
	if !assert.False(t, challenge.Verify("secret-key", "other.example.com"),
		"challenge.Verify(secret-key, other.example.com) = true, want false") {
		return
	}

	challenge.RequestB64 = ""
	echo := challenge.ToEcho()
	if !assert.NotEqual(t, "", echo.Request,
		"echo.Request = empty, want base64-encoded request") {
		return
	}
	if !assert.True(t, ConstantTimeEqual(challenge.ID, echo.ID),
		"ConstantTimeEqual(challenge.ID, echo.ID) = false, want true") {
		return
	}
	if !assert.False(t, ConstantTimeEqual(challenge.ID, "different"),
		"ConstantTimeEqual(challenge.ID, different) = true, want false") {
		return
	}
	if !assert.Equalf(t, challenge.Expires, echo.Expires,
		"echo.Expires = %q, want %q", echo.Expires, challenge.Expires) {
		return
	}

}

func TestChallengeNewCredential(t *testing.T) {
	t.Parallel()

	challenge := NewChallenge(
		"secret-key",
		"api.example.com",
		"tempo",
		"charge",
		map[string]any{"amount": "100"},
	)

	credential := challenge.NewCredential(
		map[string]any{"type": "hash", "hash": "0xabc123"},
		WithCredentialSource("did:pkh:eip155:42431:0x1234"),
	)
	if !assert.Equalf(t, challenge.ID, credential.Challenge.ID,
		"credential.Challenge.ID = %q, want %q", credential.Challenge.ID, challenge.ID) {
		return
	}
	if !assert.Equalf(t, challenge.ToEcho().Request, credential.Challenge.Request,
		"credential.Challenge.Request = %q, want %q", credential.Challenge.Request, challenge.ToEcho().Request) {
		return
	}
	if !assert.Equalf(t, "did:pkh:eip155:42431:0x1234", credential.Source,
		"credential.Source = %q, want source", credential.Source) {
		return
	}
	if !assert.Equalf(t, "0xabc123", credential.Payload["hash"],
		"credential.Payload[hash] = %#v, want %q", credential.Payload["hash"], "0xabc123") {
		return
	}

}
