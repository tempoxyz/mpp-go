package mpp_test

import (
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/tempoxyz/mpp-go/pkg/mpp"
)

func ExampleFormatChallenge() {
	challenge, err := mpp.NewChallengeWithError(
		"server-secret", "api.example.com", "tempo", mpp.IntentCharge,
		map[string]any{"amount": "1000000", "currency": "0x20c0000000000000000000006a37DA5C996874BE"},
	)
	if err != nil {
		panic(err)
	}
	header, err := mpp.FormatChallenge(challenge)
	if err != nil {
		panic(err)
	}
	fmt.Println(header)
	// Output:
	// Payment id="qEJM3P0jKiqImybGyCeUMeHy2beC0JTnqS_TTde14lY", realm="api.example.com", method="tempo", intent="charge", request="eyJhbW91bnQiOiIxMDAwMDAwIiwiY3VycmVuY3kiOiIweDIwYzAwMDAwMDAwMDAwMDAwMDAwMDAwMDZhMzdEQTVDOTk2ODc0QkUifQ"
}

func ExampleParseChallenge() {
	header := "Payment id=\"abc\", realm=\"api.example.com\", method=\"tempo\", intent=\"charge\", request=\"eyJhbW91bnQiOiIxMDAwMDAwIn0\""
	challenge, err := mpp.ParseChallenge(header)
	if err != nil {
		panic(err)
	}
	fmt.Println(challenge.Realm, challenge.Method, challenge.Intent, challenge.Request["amount"])
	// Output:
	// api.example.com tempo charge 1000000
}

func ExampleNewChallengeWithError() {
	challenge, err := mpp.NewChallengeWithError(
		"server-secret", "api.example.com", "tempo", mpp.IntentCharge,
		map[string]any{"amount": "1000000"},
		mpp.WithExpires("2030-01-01T00:00:00.000Z"),
	)
	if err != nil {
		panic(err)
	}
	fmt.Println(challenge.VerifyID("server-secret"))

	// The ID binds every parameter, including the realm.
	moved := *challenge
	moved.Realm = "other.example.com"
	fmt.Println(moved.VerifyID("server-secret"))
	// Output:
	// true
	// false
}

func ExampleParseReceipt() {
	header, err := mpp.FormatReceiptWithError(&mpp.Receipt{
		Status:    "success",
		Method:    "tempo",
		Reference: "0x9f3c",
		Timestamp: time.Date(2026, 1, 29, 12, 0, 0, 0, time.UTC),
	})
	if err != nil {
		panic(err)
	}
	receipt, err := mpp.ParseReceipt(header)
	if err != nil {
		panic(err)
	}
	fmt.Println(receipt.Method, receipt.Reference, receipt.Timestamp.Format(time.RFC3339))
	// Output:
	// tempo 0x9f3c 2026-01-29T12:00:00Z
}

func ExampleReceiptFromResponse() {
	header, err := mpp.FormatReceiptWithError(mpp.Success("tempo", "0x9f3c"))
	if err != nil {
		panic(err)
	}
	resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{}}
	resp.Header.Set(mpp.HeaderPaymentReceipt, header)

	receipt, err := mpp.ReceiptFromResponse(resp)
	if err != nil {
		panic(err)
	}
	fmt.Println(receipt.Status, receipt.Reference)
	// Output:
	// success 0x9f3c
}

func ExampleExpiresIn() {
	expires := mpp.ExpiresIn(5 * time.Minute)

	// Issue a challenge with mpp.WithExpires(expires); verify it later.
	fmt.Println(mpp.CheckExpires(expires, time.Now()))
	fmt.Println(mpp.CheckExpires(expires, time.Now().Add(10*time.Minute)) != nil)
	// Output:
	// <nil>
	// true
}

func ExampleErrorType() {
	err := fmt.Errorf("charge: %w", mpp.ErrMalformedCredential("invalid base64url"))

	fmt.Println(errors.Is(err, mpp.ErrorTypeMalformedCredential))
	fmt.Println(errors.Is(err, mpp.ErrorTypePaymentExpired))

	var pe *mpp.PaymentError
	if errors.As(err, &pe) {
		fmt.Println(pe.Status, pe.Title)
	}
	// Output:
	// true
	// false
	// 402 Malformed Credential
}
