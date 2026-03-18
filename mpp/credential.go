package mpp

// Credential represents a client-submitted payment credential sent via the
// Authorization header.
type Credential struct {
	Challenge ChallengeEcho  `json:"challenge"`
	Payload   map[string]any `json:"payload,omitempty"`
	Source    string         `json:"source,omitempty"`
}

// FromAuthorization parses an Authorization header value into a Credential.
func FromAuthorization(header string) (*Credential, error) {
	return ParseAuthorization(header)
}

// ToAuthorization formats this Credential as an Authorization header value.
func (c *Credential) ToAuthorization() string {
	return FormatAuthorization(c)
}
