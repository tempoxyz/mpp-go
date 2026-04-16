package mpp

import (
	"bytes"
	"encoding/json"
	"fmt"
)

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
	request, err := requestForJSON(nil, c.Challenge.Request)
	if err != nil {
		return nil, err
	}

	payload := c.Payload
	if payload == nil {
		payload = map[string]any{}
	}

	return json.Marshal(struct {
		Challenge struct {
			ID      string `json:"id"`
			Realm   string `json:"realm,omitempty"`
			Method  string `json:"method"`
			Intent  string `json:"intent"`
			Request any    `json:"request"`
			Expires string `json:"expires,omitempty"`
			Digest  string `json:"digest,omitempty"`
			Opaque  any    `json:"opaque,omitempty"`
		} `json:"challenge"`
		Payload map[string]any `json:"payload"`
		Source  string         `json:"source,omitempty"`
	}{
		Challenge: struct {
			ID      string `json:"id"`
			Realm   string `json:"realm,omitempty"`
			Method  string `json:"method"`
			Intent  string `json:"intent"`
			Request any    `json:"request"`
			Expires string `json:"expires,omitempty"`
			Digest  string `json:"digest,omitempty"`
			Opaque  any    `json:"opaque,omitempty"`
		}{
			ID:      c.Challenge.ID,
			Realm:   c.Challenge.Realm,
			Method:  c.Challenge.Method,
			Intent:  c.Challenge.Intent,
			Request: request,
			Expires: c.Challenge.Expires,
			Digest:  c.Challenge.Digest,
			Opaque:  opaqueForJSON(c.Challenge.Opaque),
		},
		Payload: payload,
		Source:  c.Source,
	})
}

// UnmarshalJSON accepts a credential with either an object request or the raw
// base64url echo string and normalizes it into the wire-centric ChallengeEcho.
func (c *Credential) UnmarshalJSON(data []byte) error {
	var decoded struct {
		Challenge struct {
			ID      string          `json:"id"`
			Realm   string          `json:"realm,omitempty"`
			Method  string          `json:"method"`
			Intent  string          `json:"intent"`
			Request json.RawMessage `json:"request"`
			Expires string          `json:"expires,omitempty"`
			Digest  string          `json:"digest,omitempty"`
			Opaque  json.RawMessage `json:"opaque,omitempty"`
		} `json:"challenge"`
		Payload map[string]any `json:"payload"`
		Source  string         `json:"source,omitempty"`
	}
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}

	request, err := requestB64FromJSON(decoded.Challenge.Request)
	if err != nil {
		return err
	}
	opaque, err := decodeJSONOpaque(decoded.Challenge.Opaque)
	if err != nil {
		return err
	}

	payload := decoded.Payload
	if payload == nil {
		payload = map[string]any{}
	}

	*c = Credential{
		Challenge: ChallengeEcho{
			ID:      decoded.Challenge.ID,
			Realm:   decoded.Challenge.Realm,
			Method:  decoded.Challenge.Method,
			Intent:  decoded.Challenge.Intent,
			Request: request,
			Expires: decoded.Challenge.Expires,
			Digest:  decoded.Challenge.Digest,
			Opaque:  opaque,
		},
		Payload: payload,
		Source:  decoded.Source,
	}
	return nil
}

// ToAuthorization formats this Credential as an Authorization header value.
func (c *Credential) ToAuthorization() string {
	return FormatAuthorization(c)
}

func decodeJSONOpaque(raw json.RawMessage) (map[string]string, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil, nil
	}

	if trimmed[0] == '"' {
		var encoded string
		if err := json.Unmarshal(trimmed, &encoded); err != nil {
			return nil, err
		}
		if encoded == "" {
			return map[string]string{}, nil
		}
		decoded, err := B64Decode(encoded)
		if err != nil {
			return map[string]string{"_raw": encoded}, nil
		}
		opaque := make(map[string]string, len(decoded))
		for key, value := range decoded {
			opaque[key] = anyStr(value)
		}
		return opaque, nil
	}

	var opaque map[string]string
	if err := json.Unmarshal(trimmed, &opaque); err == nil {
		return opaque, nil
	}

	var opaqueAny map[string]any
	if err := json.Unmarshal(trimmed, &opaqueAny); err != nil {
		return nil, fmt.Errorf("mpp: opaque must be an object or base64url string: %w", err)
	}
	opaque = make(map[string]string, len(opaqueAny))
	for key, value := range opaqueAny {
		opaque[key] = anyStr(value)
	}
	return opaque, nil
}

func opaqueForJSON(opaque map[string]string) any {
	if opaque == nil {
		return nil
	}
	if raw, ok := opaque["_raw"]; ok && len(opaque) == 1 {
		decoded, err := B64Decode(raw)
		if err != nil {
			return raw
		}
		result := make(map[string]string, len(decoded))
		for key, value := range decoded {
			result[key] = anyStr(value)
		}
		return result
	}
	return opaque
}
