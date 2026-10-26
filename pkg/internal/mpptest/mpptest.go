// Package mpptest builds and formats MPP Challenges and Credentials for
// tests, panicking where the mpp functions return an error.
package mpptest

import "github.com/tempoxyz/mpp-go/pkg/mpp"

// NewChallenge is mpp.NewChallengeWithError, panicking on error.
func NewChallenge(secretKey, realm, method, intent string, request map[string]any, opts ...mpp.ChallengeOption) *mpp.Challenge {
	challenge, err := mpp.NewChallengeWithError(secretKey, realm, method, intent, request, opts...)
	if err != nil {
		panic(err)
	}
	return challenge
}

// FormatChallenge is mpp.FormatChallenge, panicking on error.
func FormatChallenge(challenge *mpp.Challenge) string {
	header, err := mpp.FormatChallenge(challenge)
	if err != nil {
		panic(err)
	}
	return header
}

// FormatCredential is mpp.FormatCredential, panicking on error.
func FormatCredential(credential *mpp.Credential) string {
	header, err := mpp.FormatCredential(credential)
	if err != nil {
		panic(err)
	}
	return header
}
