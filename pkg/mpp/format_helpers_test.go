package mpp

// formatReceiptUnchecked encodes r without the validation
// FormatReceiptWithError applies, so tests can build receipts ParseReceipt
// rejects.
func formatReceiptUnchecked(r *Receipt) string {
	return b64EncodeAny(receiptFields(r))
}

// formatAuthenticateUnchecked formats c with realm, returning an empty string
// where FormatChallenge would return an error.
func formatAuthenticateUnchecked(c *Challenge, realm string) string {
	header, _ := formatAuthenticate(c, realm, false)
	return header
}

// formatAuthorizationUnchecked formats c without the wire size limit,
// returning an empty string where FormatCredential would return an error.
func formatAuthorizationUnchecked(c *Credential) string {
	header, _ := formatAuthorization(c)
	return header
}

// withExtra sets the nested "extra" receipt object, an ordinary extension.
func withExtra(extra map[string]any) ReceiptOption {
	return func(r *Receipt) {
		if r.Extensions == nil {
			r.Extensions = make(map[string]any)
		}
		r.Extensions["extra"] = extra
	}
}

// newChallenge is NewChallengeWithError for fixed test inputs; it panics on
// an error.
func newChallenge(secretKey, realm, method, intent string, request map[string]any, opts ...ChallengeOption) *Challenge {
	challenge, err := NewChallengeWithError(secretKey, realm, method, intent, request, opts...)
	if err != nil {
		panic(err)
	}
	return challenge
}

// mustChallengeID is GenerateChallengeIDWithError for fixed test inputs;
// it panics on an error.
func mustChallengeID(in GenerateChallengeIDInput) string {
	id, err := GenerateChallengeIDWithError(in)
	if err != nil {
		panic(err)
	}
	return id
}

// toAuthenticate formats c with realm, returning an empty string where
// FormatChallenge would return an error.
func (c *Challenge) toAuthenticate(realm string) string {
	return formatAuthenticateUnchecked(c, realm)
}

// toAuthenticateStrict formats c with realm, rejecting values that cannot be
// represented in quoted auth-params.
func (c *Challenge) toAuthenticateStrict(realm string) (string, error) {
	return formatAuthenticateStrict(c, realm)
}

// toAuthorization formats c, returning an empty string where FormatCredential
// would return an error.
func (c *Credential) toAuthorization() string {
	return formatAuthorizationUnchecked(c)
}
