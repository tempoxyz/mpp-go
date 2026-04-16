package mpp

import (
	"errors"
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
			status: http.StatusBadRequest,
			detail: "bad credential",
		},
		{
			name:   "invalid challenge",
			err:    ErrInvalidChallenge("challenge-1", "tampered"),
			want:   ErrorTypeInvalidChallenge,
			status: http.StatusBadRequest,
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

			if tt.err.Type != tt.want {
				t.Fatalf("err.Type = %q, want %q", tt.err.Type, tt.want)
			}
			if tt.err.Status != tt.status {
				t.Fatalf("err.Status = %d, want %d", tt.err.Status, tt.status)
			}
			if tt.err.Detail != tt.detail {
				t.Fatalf("err.Detail = %q, want %q", tt.err.Detail, tt.detail)
			}
		})
	}
}

func TestPaymentErrorHelpers(t *testing.T) {
	t.Parallel()

	err := ErrVerificationFailed("signature mismatch")
	if got := err.Error(); got != "Verification Failed: signature mismatch" {
		t.Fatalf("err.Error() = %q, want %q", got, "Verification Failed: signature mismatch")
	}
	if !errors.Is(err, ErrVerification) {
		t.Fatal("errors.Is(err, ErrVerification) = false, want true")
	}

	problem := err.ProblemDetails("challenge-1")
	if problem["type"] != err.Type {
		t.Fatalf("problem[type] = %#v, want %q", problem["type"], err.Type)
	}
	if problem["challengeId"] != "challenge-1" {
		t.Fatalf("problem[challengeId] = %#v, want %q", problem["challengeId"], "challenge-1")
	}

	nonVerification := ErrBadRequest("invalid")
	if errors.Is(nonVerification, ErrVerification) {
		t.Fatal("errors.Is(nonVerification, ErrVerification) = true, want false")
	}
	if got := nonVerification.ProblemDetails(""); got["challengeId"] != nil {
		t.Fatalf("problem[challengeId] = %#v, want nil", got["challengeId"])
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
	if receipt.Status != "success" {
		t.Fatalf("receipt.Status = %q, want %q", receipt.Status, "success")
	}
	if receipt.Timestamp.Location() != time.UTC {
		t.Fatalf("receipt.Timestamp.Location() = %v, want UTC", receipt.Timestamp.Location())
	}

	header := receipt.ToPaymentReceipt()
	parsed, err := FromPaymentReceipt(header)
	if err != nil {
		t.Fatalf("FromPaymentReceipt() error = %v", err)
	}
	if parsed.Reference != receipt.Reference {
		t.Fatalf("parsed.Reference = %q, want %q", parsed.Reference, receipt.Reference)
	}
	if parsed.Method != receipt.Method {
		t.Fatalf("parsed.Method = %q, want %q", parsed.Method, receipt.Method)
	}
	if parsed.ExternalID != receipt.ExternalID {
		t.Fatalf("parsed.ExternalID = %q, want %q", parsed.ExternalID, receipt.ExternalID)
	}
	if parsed.Extra["settled"] != true {
		t.Fatalf("parsed.Extra[settled] = %#v, want true", parsed.Extra["settled"])
	}
	if got, want := parsed.Timestamp.Format("2006-01-02T15:04:05.000Z"), receipt.Timestamp.Format("2006-01-02T15:04:05.000Z"); got != want {
		t.Fatalf("parsed.Timestamp = %q, want %q", got, want)
	}
}

func TestParsePaymentReceiptValidation(t *testing.T) {
	t.Parallel()

	_, err := ParsePaymentReceipt(b64EncodeAny(map[string]any{
		"status":    "pending",
		"timestamp": "2026-01-01T00:00:00Z",
		"reference": "ref-123",
	}))
	if err == nil || !strings.Contains(err.Error(), "invalid receipt status") {
		t.Fatalf("ParsePaymentReceipt() error = %v, want invalid status error", err)
	}

	_, err = ParsePaymentReceipt(b64EncodeAny(map[string]any{
		"status": "success",
	}))
	if err == nil || !strings.Contains(err.Error(), "receipt missing reference") {
		t.Fatalf("ParsePaymentReceipt() error = %v, want missing reference error", err)
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
	if !challenge.Verify("secret-key", "api.example.com") {
		t.Fatal("challenge.Verify(secret-key, api.example.com) = false, want true")
	}
	if challenge.Verify("secret-key", "other.example.com") {
		t.Fatal("challenge.Verify(secret-key, other.example.com) = true, want false")
	}

	challenge.RequestB64 = ""
	echo := challenge.ToEcho()
	if echo.Request == "" {
		t.Fatal("echo.Request = empty, want base64-encoded request")
	}
	if !ConstantTimeEqual(challenge.ID, echo.ID) {
		t.Fatal("ConstantTimeEqual(challenge.ID, echo.ID) = false, want true")
	}
	if ConstantTimeEqual(challenge.ID, "different") {
		t.Fatal("ConstantTimeEqual(challenge.ID, different) = true, want false")
	}
	if echo.Expires != challenge.Expires {
		t.Fatalf("echo.Expires = %q, want %q", echo.Expires, challenge.Expires)
	}
}
