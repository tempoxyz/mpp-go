package client

import (
	"time"

	"github.com/tempoxyz/mpp-go/pkg/mpp"
)

// ChallengeCandidate pairs an offered Challenge with a configured Method that
// can pay it.
type ChallengeCandidate struct {
	Challenge *mpp.Challenge
	Method    Method
	// Quality is the payment preference q-value that ranked the Challenge.
	Quality float64
}

// SelectChallengeCandidates returns the offered Challenges that methods can
// pay, ranked like the HTTP Transport ranks them: by payment preference, then
// in offer order. Expired Challenges, Challenges no method handles and
// Challenges with q=0 are left out. Transports other than HTTP, such as MCP,
// use it to pick the Challenge to pay. Unknown preference keys and invalid
// q-values return an error.
func SelectChallengeCandidates(
	challenges []mpp.Challenge,
	methods []Method,
	preferences PaymentPreferences,
) ([]ChallengeCandidate, error) {
	t := newTransport(methods, nil, &transportConfig{paymentPreferences: clonePaymentPreferences(preferences)})
	if t.configErr != nil {
		return nil, t.configErr
	}
	ranked := t.challengeCandidates(challenges, t.paymentPreferences, time.Now().UTC())
	candidates := make([]ChallengeCandidate, len(ranked))
	for i, candidate := range ranked {
		candidates[i] = ChallengeCandidate{
			Challenge: candidate.challenge,
			Method:    candidate.method,
			Quality:   candidate.quality,
		}
	}
	return candidates, nil
}
