package server

import (
	"context"
	"fmt"
	"reflect"
	"time"

	"github.com/tempoxyz/mpp-go/pkg/mpp"
)

// VerifyParams contains the parameters for VerifyOrChallenge.
type VerifyParams struct {
	// Authorization is the incoming Authorization header value.
	Authorization string
	// Intent verifies Credentials for this request.
	Intent Intent
	// Request is the canonical request shape the Challenge binds to.
	Request map[string]any
	// Realm is the expected server realm.
	Realm string
	// SecretKey signs and verifies Challenge IDs.
	SecretKey string
	// Method is the payment method token, for example "tempo".
	Method string
	// Description is copied into a newly issued Challenge.
	Description string
	// Meta stores opaque Challenge metadata.
	Meta map[string]string
	// Expires overrides the default Challenge expiry.
	Expires string
}

// VerifyResult is either a Challenge or a verified (Credential, Receipt) pair.
type VerifyResult struct {
	// Challenge is returned when the server needs the client to pay first.
	Challenge *mpp.Challenge
	// Credential is the parsed client credential on success.
	Credential *mpp.Credential
	// Receipt acknowledges successful verification.
	Receipt *mpp.Receipt
}

// VerifyOrChallenge checks for a valid payment credential or generates a new challenge.
// Returns a Challenge if payment is required, or (Credential, Receipt) if verified.
//
// Logic:
//  1. If no authorization header, create new challenge
//  2. Extract "Payment" scheme from Authorization header
//  3. Parse credential
//  4. Recompute expected challenge ID from echoed parameters via HMAC
//  5. Constant-time compare IDs
//  6. Verify echoed fields match (realm, method, intent name, request)
//  7. Check expiry
//  8. Call intent.Verify
//  9. Return result
func VerifyOrChallenge(ctx context.Context, params VerifyParams) (*VerifyResult, error) {
	expires := params.Expires
	if expires == "" {
		expires = mpp.Expires.Minutes(5)
	}

	// Build the challenge from server-side parameters. Used both for
	// issuing a fresh 402 and for verifying echoed credentials.
	var opts []mpp.ChallengeOption
	if expires != "" {
		opts = append(opts, mpp.WithExpires(expires))
	}
	if params.Description != "" {
		opts = append(opts, mpp.WithDescription(params.Description))
	}
	if params.Meta != nil {
		opts = append(opts, mpp.WithMeta(params.Meta))
	}

	challenge := mpp.NewChallenge(
		params.SecretKey,
		params.Realm,
		params.Method,
		params.Intent.Name(),
		params.Request,
		opts...,
	)

	// 1. No authorization header — issue challenge.
	if params.Authorization == "" {
		return &VerifyResult{Challenge: challenge}, nil
	}

	// 2. Extract the Payment credential from Authorization.
	authHeader := mpp.FindPaymentAuthorization(params.Authorization)
	if authHeader == "" {
		return &VerifyResult{Challenge: challenge}, nil
	}

	// 3. Parse credential.
	credential, err := mpp.ParseCredential(authHeader)
	if err != nil {
		return nil, mpp.ErrMalformedCredential(err.Error())
	}

	// 4-5. Recompute expected challenge ID and constant-time compare.
	// Reconstruct the echoed challenge request from base64.
	echoedRequest, err := echoedRequestMap(credential)
	if err != nil {
		return nil, mpp.ErrMalformedCredential(fmt.Sprintf("invalid echoed request: %v", err))
	}

	echoedChallenge := mpp.NewChallenge(
		params.SecretKey,
		credential.Challenge.Realm,
		credential.Challenge.Method,
		credential.Challenge.Intent,
		echoedRequest,
		echoedChallengeOpts(credential)...,
	)

	if !mpp.ConstantTimeEqual(credential.Challenge.ID, echoedChallenge.ID) {
		return nil, mpp.ErrInvalidChallenge(
			credential.Challenge.ID,
			"challenge was not issued by this server",
		)
	}

	// 6. Verify echoed fields match.
	echoed := &credential.Challenge
	if echoed.Realm != params.Realm {
		return nil, mpp.ErrInvalidChallenge(echoed.ID, "realm mismatch")
	}
	if echoed.Method != params.Method {
		return nil, mpp.ErrInvalidChallenge(echoed.ID, "method mismatch")
	}
	if echoed.Intent != params.Intent.Name() {
		return nil, mpp.ErrInvalidChallenge(echoed.ID, "intent mismatch")
	}

	if !mpp.JSONEqual(echoedRequest, params.Request) {
		return nil, mpp.ErrInvalidChallenge(
			echoed.ID,
			"credential request does not match this route's requirements",
		)
	}
	if echoed.Expires == "" {
		return nil, mpp.ErrInvalidChallenge(echoed.ID, "missing required expires")
	}
	if params.Expires != "" && echoed.Expires != params.Expires {
		return nil, mpp.ErrInvalidChallenge(
			echoed.ID,
			"credential expires does not match this route's requirements",
		)
	}
	if !reflect.DeepEqual(echoed.Opaque, challenge.Opaque) {
		return nil, mpp.ErrInvalidChallenge(
			echoed.ID,
			"credential opaque metadata does not match this route's requirements",
		)
	}

	// 7. Check expiry.
	if echoed.Expires != "" {
		expiresTime, err := time.Parse(time.RFC3339, echoed.Expires)
		if err != nil {
			// Try the millisecond format used by mpp.Expires helpers.
			expiresTime, err = time.Parse("2006-01-02T15:04:05.000Z", echoed.Expires)
			if err != nil {
				return nil, mpp.ErrInvalidChallenge(echoed.ID, "invalid expires format")
			}
		}
		if time.Now().UTC().After(expiresTime) {
			return nil, mpp.ErrPaymentExpired(echoed.Expires)
		}
	}

	// 8. Call intent.Verify.
	receipt, err := params.Intent.Verify(ctx, credential, params.Request)
	if err != nil {
		if pe, ok := err.(*mpp.PaymentError); ok {
			return nil, pe
		}
		return nil, mpp.ErrVerificationFailed(err.Error())
	}

	// 9. Return result.
	return &VerifyResult{
		Credential: credential,
		Receipt:    receipt,
	}, nil
}

// echoedRequestMap decodes the echoed request from a credential's challenge echo.
func echoedRequestMap(cred *mpp.Credential) (map[string]any, error) {
	if cred.Challenge.Request == "" {
		return nil, nil
	}
	m, err := mpp.B64Decode(cred.Challenge.Request)
	if err != nil {
		return nil, err
	}
	return m, nil
}

// echoedChallengeOpts reconstructs ChallengeOptions from a credential's echoed challenge.
func echoedChallengeOpts(cred *mpp.Credential) []mpp.ChallengeOption {
	var opts []mpp.ChallengeOption
	if cred.Challenge.Expires != "" {
		opts = append(opts, mpp.WithExpires(cred.Challenge.Expires))
	}
	if cred.Challenge.Digest != "" {
		opts = append(opts, mpp.WithDigest(cred.Challenge.Digest))
	}
	if cred.Challenge.Opaque != nil {
		opts = append(opts, mpp.WithMeta(cred.Challenge.Opaque))
	}
	return opts
}
