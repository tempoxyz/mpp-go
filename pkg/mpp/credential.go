package mpp

import "encoding/json"

// Credential represents a client-submitted payment credential sent via the
// Authorization header.
type Credential struct {
	Challenge ChallengeEcho  `json:"challenge"`
	Payload   map[string]any `json:"payload,omitempty"`
	Source    string         `json:"source,omitempty"`
}

// CredentialOption configures optional fields when creating a Credential.
type CredentialOption func(*Credential)

// WithCredentialSource sets the optional payer identity on a Credential.
func WithCredentialSource(source string) CredentialOption {
	return func(c *Credential) { c.Source = source }
}

// NewCredential builds a Credential from a Challenge and payment payload.
func (c *Challenge) NewCredential(payload map[string]any, opts ...CredentialOption) *Credential {
	credential := &Credential{
		Challenge: c.ToEcho(),
		Payload:   payload,
	}
	for _, opt := range opts {
		opt(credential)
	}
	return credential
}

// FromAuthorization parses an Authorization header value into a Credential.
func FromAuthorization(header string) (*Credential, error) {
	return ParseCredential(header)
}

// MarshalJSON emits the ergonomic JSON credential shape with a decoded
// challenge request object instead of the internal base64url string.
func (c Credential) MarshalJSON() ([]byte, error) {
	payload := c.Payload
	if payload == nil {
		payload = map[string]any{}
	}

	return json.Marshal(struct {
		Challenge Challenge      `json:"challenge"`
		Payload   map[string]any `json:"payload"`
		Source    string         `json:"source,omitempty"`
	}{
		Challenge: c.Challenge.toChallenge(),
		Payload:   payload,
		Source:    c.Source,
	})
}

// UnmarshalJSON accepts a credential with either an object request or the raw
// base64url echo string and normalizes it into the wire-centric ChallengeEcho.
func (c *Credential) UnmarshalJSON(data []byte) error {
	var decoded struct {
		Challenge Challenge      `json:"challenge"`
		Payload   map[string]any `json:"payload"`
		Source    string         `json:"source,omitempty"`
	}
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}

	payload := decoded.Payload
	if payload == nil {
		payload = map[string]any{}
	}

	*c = Credential{
		Challenge: decoded.Challenge.ToEcho(),
		Payload:   payload,
		Source:    decoded.Source,
	}
	return nil
}

// ToAuthorization formats this Credential as an Authorization header value.
func (c *Credential) ToAuthorization() string {
	return FormatAuthorization(c)
}
