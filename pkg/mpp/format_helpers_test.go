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

// withExtra sets the nested "extra" receipt object, which Receipt.Extra keeps
// for the conformance adapter.
func withExtra(extra map[string]any) ReceiptOption {
	return func(r *Receipt) { r.Extra = extra }
}
