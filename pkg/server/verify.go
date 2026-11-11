package server

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"time"

	"github.com/tempoxyz/mpp-go/pkg/mpp"
)

// VerifyParams contains the parameters for VerifyOrChallenge.
type VerifyParams struct {
	// DisableChallengeNonce issues Challenges without the random, HMAC-bound
	// _mpp_nonce that by default makes every issued Challenge unique.
	// Verification accepts Challenges with and without a nonce either way.
	// Mpp.Charge sets it for a handler created WithChallengeNonce(false).
	DisableChallengeNonce bool
	// Authorization is the incoming Authorization header value.
	Authorization string
	// Intent verifies Credentials for this request.
	Intent Intent
	// Request is the canonical request shape the Challenge binds to.
	Request map[string]any
	// Body is the actual request body bytes/string/object the digest binds to.
	// When set, issued challenges include a body digest and submitted
	// credentials must echo a digest matching this value (see
	// mpp.ComputeBodyDigest). A []byte or string, or a named type of either,
	// is digested as is; any other value as its json.Marshal output. An
	// io.Reader or a value json.Marshal cannot encode is an error.
	Body any
	// Realm is the expected server realm.
	Realm string
	// SecretKey signs and verifies Challenge IDs. It must be at least 32
	// bytes, as the core specification requires.
	SecretKey string
	// Method is the payment method token, for example "tempo".
	Method string
	// Description is copied into a newly issued Challenge.
	Description string
	// Meta stores opaque Challenge metadata.
	Meta map[string]string
	// Expires overrides the default Challenge expiry.
	Expires string
	// Header selects the HTTP field for the Payment credential. Empty defaults
	// to Authorization. Payment-Authorization is advertised when set.
	Header string
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

// IsChallenge reports whether the result is a Challenge the client must
// satisfy, rather than a verified payment. A nil result is not a challenge.
func (r *VerifyResult) IsChallenge() bool {
	return r != nil && r.Challenge != nil
}

// VerifyOrChallenge checks for a valid payment credential or generates a new challenge.
// Returns a Challenge if payment is required, or (Credential, Receipt) if verified.
//
// Logic:
//  1. If no authorization header, create new challenge
//  2. Extract "Payment" scheme from Authorization header
//  3. Parse credential (malformed-credential)
//  4. Recompute expected challenge ID from echoed parameters via HMAC
//  5. Constant-time compare IDs
//  6. Verify echoed fields match (realm, method, intent name, request,
//     opaque metadata, body digest): invalid-challenge
//  7. Check expiry (payment-expired)
//  8. Validate then broadcast split intents, or call legacy intent.Verify;
//     the method reports replays (invalid-challenge) before verifying the
//     payment (verification-failed or invalid-payload)
//  9. Return result
//
// This is the check order the core specification sets. Every rejected
// credential gets a fresh Challenge, except a method's verification-failed
// error, which keeps the echoed one.
func VerifyOrChallenge(ctx context.Context, params VerifyParams) (*VerifyResult, error) {
	if len(params.SecretKey) < minimumSecretKeyBytes {
		return nil, fmt.Errorf("server: secret key must be at least %d bytes", minimumSecretKeyBytes)
	}
	expires := params.Expires
	if expires == "" {
		expires = mpp.ExpiresIn(5 * time.Minute)
	}

	// Build the challenge from server-side parameters. Used both for
	// issuing a fresh 402 and for verifying echoed credentials.
	opts := []mpp.ChallengeOption{mpp.WithExpires(expires)}
	if params.Description != "" {
		opts = append(opts, mpp.WithDescription(params.Description))
	}
	for _, key := range challengeNonceKeys {
		if _, reserved := params.Meta[key]; reserved {
			// Meta is server configuration: a reserved key is a server bug.
			return nil, fmt.Errorf("server: %s is reserved for challenge issuance", key)
		}
	}
	meta := maps.Clone(params.Meta)
	if !params.DisableChallengeNonce {
		if meta == nil {
			meta = make(map[string]string)
		}
		nonce := make([]byte, challengeNonceBytes)
		if _, err := rand.Read(nonce); err != nil {
			return nil, fmt.Errorf("generating challenge nonce: %w", err)
		}
		meta[challengeNonceKey] = base64.RawURLEncoding.EncodeToString(nonce)
	}
	if meta != nil {
		opts = append(opts, mpp.WithMeta(meta))
	}

	if params.Header != "" {
		opts = append(opts, mpp.WithHeader(params.Header))
	}
	if params.Body != nil {
		body, err := bodyBytes(params.Body)
		if err != nil {
			return nil, err
		}
		opts = append(opts, mpp.WithDigest(mpp.ComputeBodyDigest(body)))
	}

	challenge, err := mpp.NewChallengeWithError(
		params.SecretKey,
		params.Realm,
		params.Method,
		params.Intent.Name(),
		params.Request,
		opts...,
	)
	if err != nil {
		return nil, fmt.Errorf("server: invalid challenge request: %w", err)
	}

	// 1. No authorization header — issue challenge.
	if params.Authorization == "" {
		return &VerifyResult{Challenge: challenge}, nil
	}

	// 2. Extract the Payment credential from Authorization. A credential
	// over the size servers must accept is malformed.
	if len(params.Authorization) > maxCredentialBytes {
		return challengeResultError(challenge, mpp.ErrMalformedCredential("Payment credential exceeds maximum size"))
	}
	authHeader, err := mpp.FindPaymentAuthorizationStrict(params.Authorization)
	if err != nil {
		return nil, mpp.ErrBadRequest(err.Error())
	}
	if authHeader == "" {
		return &VerifyResult{Challenge: challenge}, nil
	}

	// 3. Parse credential.
	credential, err := mpp.ParseCredential(authHeader)
	if err != nil {
		return challengeResultError(challenge, mpp.ErrMalformedCredential(err.Error()))
	}

	// 4-5. Recompute expected challenge ID and constant-time compare.
	// Reconstruct the echoed challenge request from base64.
	echoedRequest, err := echoedRequestMap(credential)
	if err != nil {
		return challengeResultError(
			challenge,
			mpp.ErrMalformedCredential(fmt.Sprintf("invalid echoed request: %v", err)),
		)
	}

	echoedChallenge, err := mpp.NewChallengeWithError(
		params.SecretKey,
		credential.Challenge.Realm,
		credential.Challenge.Method,
		credential.Challenge.Intent,
		echoedRequest,
		echoedChallengeOpts(credential)...,
	)
	if err != nil {
		return challengeResultError(
			challenge,
			mpp.ErrMalformedCredential(fmt.Sprintf("invalid echoed request: %v", err)),
		)
	}

	if !mpp.ConstantTimeEqual(credential.Challenge.ID, echoedChallenge.ID) {
		return challengeResultError(challenge, mpp.ErrInvalidChallenge(
			credential.Challenge.ID,
			"challenge was not issued by this server",
		))
	}

	// 6. Verify echoed fields match.
	echoed := &credential.Challenge
	if echoed.Realm != params.Realm {
		return challengeResultError(challenge, mpp.ErrInvalidChallenge(echoed.ID, "realm mismatch"))
	}
	if echoed.Method != params.Method {
		return challengeResultError(challenge, mpp.ErrInvalidChallenge(echoed.ID, "method mismatch"))
	}
	if echoed.Intent != params.Intent.Name() {
		return challengeResultError(challenge, mpp.ErrInvalidChallenge(echoed.ID, "intent mismatch"))
	}

	if !mpp.ChallengeBoundJSONEqual(echoedRequest, params.Request) {
		return challengeResultError(challenge, mpp.ErrInvalidChallenge(
			echoed.ID,
			"credential request does not match this route's requirements",
		))
	}
	if echoed.Expires == "" {
		return challengeResultError(
			challenge,
			mpp.ErrInvalidChallenge(echoed.ID, "missing required expires"),
		)
	}
	expiresTime, err := mpp.ParseExpires(echoed.Expires)
	if err != nil {
		return challengeResultError(
			challenge,
			mpp.ErrInvalidChallenge(echoed.ID, "invalid expires format"),
		)
	}
	// The nonce is authenticated by the echoed HMAC, not compared with the
	// fresh challenge. Application metadata must still match exactly.
	if !maps.Equal(applicationMeta(echoed.Opaque), params.Meta) {
		return challengeResultError(challenge, mpp.ErrInvalidChallenge(
			echoed.ID,
			"credential opaque metadata does not match this route's requirements",
		))
	}
	if err := verifyBodyDigest(echoed.ID, echoed.Digest, params.Body); err != nil {
		return challengeResultError(challenge, err)
	}
	// 7. Check expiry once the credential is known to be bound to this
	// route, as the core specification orders the checks.
	if challengeExpired(expiresTime, time.Now()) {
		return challengeResultError(challenge, mpp.ErrPaymentExpired(echoed.Expires))
	}

	// 9. Dispatch split intents through Broadcast and legacy intents through Verify.
	receipt, err := broadcastCredential(ctx, params.Intent, credential, params.Request)
	if err != nil {
		var pe *mpp.PaymentError
		if errors.As(err, &pe) {
			if pe.Status != http.StatusPaymentRequired {
				return nil, pe
			}
			// A replayed credential (invalid-challenge) and an invalid
			// payload get a fresh Challenge.
			if pe.Type == mpp.ErrorTypeInvalidPayload || pe.Type == mpp.ErrorTypeInvalidChallenge {
				return challengeResultError(challenge, pe)
			}
			return challengeResultError(echoedChallenge, pe)
		}
		// An intent may reject a credential with the bare ErrVerification
		// sentinel: keep that a 402 verification failure with a fixed detail.
		if errors.Is(err, mpp.ErrVerification) {
			return challengeResultError(echoedChallenge, mpp.ErrVerificationFailed(mpp.ErrVerification.Error()).WithCause(err))
		}
		// Errors that are not payment errors are unexpected processing failures:
		// report them without their internal details instead of as a rejected
		// credential. The cause stays reachable through errors.Unwrap.
		return nil, mpp.ErrInternalPayment().WithCause(err)
	}

	// 9. Return result.
	return &VerifyResult{
		Credential: credential,
		Receipt:    receipt,
	}, nil
}

func challengeResultError(challenge *mpp.Challenge, err error) (*VerifyResult, error) {
	return &VerifyResult{Challenge: challenge}, err
}

func verifyBodyDigest(challengeID, digest string, body any) error {
	if body == nil {
		if digest != "" {
			return mpp.ErrInvalidChallenge(
				challengeID,
				"body digest present but request body was not provided",
			)
		}
		return nil
	}
	if digest == "" {
		return mpp.ErrInvalidChallenge(challengeID, "missing body digest")
	}
	data, err := bodyBytes(body)
	if err != nil {
		return err
	}
	if !mpp.VerifyBodyDigest(digest, data) {
		return mpp.ErrInvalidChallenge(challengeID, "body digest mismatch")
	}
	return nil
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
	if cred.Challenge.Header != "" {
		opts = append(opts, mpp.WithHeader(cred.Challenge.Header))
	}
	return opts
}

// challengeNonceKey is the opaque metadata key of the issuance nonce, the
// key every MPP SDK uses.
const challengeNonceKey = "_mpp_nonce"

// challengeNonceBytes is the size of the random issuance nonce, which is
// encoded as base64url without padding.
const challengeNonceBytes = 16

// challengeNonceKeys are the reserved issuance nonce keys: _mpp_nonce and
// the legacy _mppx_nonce that mppx and earlier mpp-go releases issued, still
// accepted on verification. Neither takes part in matching a Credential's
// opaque metadata against the route's.
var challengeNonceKeys = [...]string{challengeNonceKey, "_mppx_nonce"}

// maxCredentialBytes is the largest Payment credential field value servers
// accept (65536 octets): the core specification lets servers reject larger
// ones as malformed.
const maxCredentialBytes = 64 * 1024

func applicationMeta(meta map[string]string) map[string]string {
	result := maps.Clone(meta)
	for _, key := range challengeNonceKeys {
		delete(result, key)
	}
	return result
}
