package mpp

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// ReceiptFromResponse parses the Payment-Receipt header of resp, as mppx's
// Receipt.fromResponse does. It returns an error when resp is nil, the header
// is missing, or ParseReceipt rejects it.
func ReceiptFromResponse(resp *http.Response) (*Receipt, error) {
	if resp == nil {
		return nil, errors.New("mpp: response is nil")
	}
	header := resp.Header.Get(HeaderPaymentReceipt)
	if header == "" {
		return nil, fmt.Errorf("mpp: missing %s header", HeaderPaymentReceipt)
	}
	return ParseReceipt(header)
}

// ChallengesFromResponse parses every Payment challenge of a 402 response, in
// order, as mppx's Challenge.fromResponseList does. It reads every
// WWW-Authenticate field, splits values that merge several challenges, and
// ignores other authentication schemes. It returns an error when resp is nil
// or not a 402, when it carries no Payment challenge, or when one fails to
// parse.
func ChallengesFromResponse(resp *http.Response) ([]*Challenge, error) {
	if resp == nil {
		return nil, errors.New("mpp: response is nil")
	}
	if resp.StatusCode != http.StatusPaymentRequired {
		return nil, fmt.Errorf("mpp: response status is %d, not 402", resp.StatusCode)
	}
	var challenges []*Challenge
	for _, field := range resp.Header.Values(HeaderWWWAuthenticate) {
		for _, value := range SplitAuthenticate(field) {
			scheme, _, _ := strings.Cut(value, " ")
			if !strings.EqualFold(scheme, SchemePayment) {
				continue
			}
			challenge, err := ParseChallenge(value)
			if err != nil {
				return nil, err
			}
			challenges = append(challenges, challenge)
		}
	}
	if len(challenges) == 0 {
		return nil, fmt.Errorf("mpp: missing Payment challenge in %s header", HeaderWWWAuthenticate)
	}
	return challenges, nil
}
