package mpp

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// Challenge represents a server-issued payment challenge sent via the
// WWW-Authenticate header.
type Challenge struct {
	ID          string            `json:"id"`
	Method      string            `json:"method"`
	Intent      string            `json:"intent"`
	Request     map[string]any    `json:"request,omitempty"`
	Realm       string            `json:"realm,omitempty"`
	RequestB64  string            `json:"requestB64,omitempty"`
	Digest      string            `json:"digest,omitempty"`
	Expires     string            `json:"expires,omitempty"`
	Description string            `json:"description,omitempty"`
	Opaque      map[string]string `json:"opaque,omitempty"`
}

// ChallengeEcho is the subset of a Challenge echoed back in a Credential.
type ChallengeEcho struct {
	ID      string            `json:"id"`
	Realm   string            `json:"realm,omitempty"`
	Method  string            `json:"method"`
	Intent  string            `json:"intent"`
	Request string            `json:"request,omitempty"`
	Expires string            `json:"expires,omitempty"`
	Digest  string            `json:"digest,omitempty"`
	Opaque  map[string]string `json:"opaque,omitempty"`
}

// ChallengeOption configures optional fields when creating a new Challenge.
type ChallengeOption func(*challengeConfig)

type challengeConfig struct {
	expires     string
	digest      string
	description string
	opaque      map[string]string
}

// WithExpires sets the expiration timestamp on a Challenge.
func WithExpires(expires string) ChallengeOption {
	return func(c *challengeConfig) { c.expires = expires }
}

// WithDigest sets the body digest on a Challenge.
func WithDigest(digest string) ChallengeOption {
	return func(c *challengeConfig) { c.digest = digest }
}

// WithDescription sets the human-readable description on a Challenge.
func WithDescription(desc string) ChallengeOption {
	return func(c *challengeConfig) { c.description = desc }
}

// WithMeta sets opaque metadata key-value pairs on a Challenge.
func WithMeta(meta map[string]string) ChallengeOption {
	return func(c *challengeConfig) { c.opaque = meta }
}

// NewChallenge creates a new Challenge with an HMAC-bound ID. The secretKey
// and realm are used to compute the ID via GenerateChallengeID.
func NewChallenge(secretKey, realm, method, intent string, request map[string]any, opts ...ChallengeOption) *Challenge {
	cfg := &challengeConfig{}
	for _, o := range opts {
		o(cfg)
	}

	requestB64 := b64EncodeRequest(request)

	id := GenerateChallengeID(GenerateChallengeIDInput{
		SecretKey: secretKey,
		Realm:     realm,
		Method:    method,
		Intent:    intent,
		Request:   request,
		Expires:   cfg.expires,
		Digest:    cfg.digest,
		Opaque:    cfg.opaque,
	})

	return &Challenge{
		ID:          id,
		Method:      method,
		Intent:      intent,
		Request:     request,
		Realm:       realm,
		RequestB64:  requestB64,
		Digest:      cfg.digest,
		Expires:     cfg.expires,
		Description: cfg.description,
		Opaque:      cfg.opaque,
	}
}

// MarshalJSON emits the standard JSON challenge shape with a decoded request
// object, while keeping RequestB64 as an internal cache for header formatting.
func (c Challenge) MarshalJSON() ([]byte, error) {
	request, err := requestForJSON(c.Request, c.RequestB64)
	if err != nil {
		return nil, err
	}
	return json.Marshal(struct {
		ID          string            `json:"id"`
		Method      string            `json:"method"`
		Intent      string            `json:"intent"`
		Request     map[string]any    `json:"request"`
		Realm       string            `json:"realm,omitempty"`
		Digest      string            `json:"digest,omitempty"`
		Expires     string            `json:"expires,omitempty"`
		Description string            `json:"description,omitempty"`
		Opaque      map[string]string `json:"opaque,omitempty"`
	}{
		ID:          c.ID,
		Method:      c.Method,
		Intent:      c.Intent,
		Request:     request,
		Realm:       c.Realm,
		Digest:      c.Digest,
		Expires:     c.Expires,
		Description: c.Description,
		Opaque:      c.Opaque,
	})
}

// UnmarshalJSON accepts the standard JSON challenge shape and computes the
// cached RequestB64 field used by header serialization.
func (c *Challenge) UnmarshalJSON(data []byte) error {
	var decoded struct {
		ID          string            `json:"id"`
		Method      string            `json:"method"`
		Intent      string            `json:"intent"`
		Request     json.RawMessage   `json:"request"`
		Realm       string            `json:"realm,omitempty"`
		RequestB64  string            `json:"requestB64,omitempty"`
		Digest      string            `json:"digest,omitempty"`
		Expires     string            `json:"expires,omitempty"`
		Description string            `json:"description,omitempty"`
		Opaque      map[string]string `json:"opaque,omitempty"`
	}
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}

	request, requestB64, err := decodeJSONRequest(decoded.Request, decoded.RequestB64)
	if err != nil {
		return err
	}

	*c = Challenge{
		ID:          decoded.ID,
		Method:      decoded.Method,
		Intent:      decoded.Intent,
		Request:     request,
		Realm:       decoded.Realm,
		RequestB64:  requestB64,
		Digest:      decoded.Digest,
		Expires:     decoded.Expires,
		Description: decoded.Description,
		Opaque:      decoded.Opaque,
	}
	return nil
}

// FromAuthenticate parses an authentication header value into a Challenge.
func FromAuthenticate(header string) (*Challenge, error) {
	return ParseChallenge(header)
}

// FromWWWAuthenticate parses a WWW-Authenticate header value into a Challenge.
func FromWWWAuthenticate(header string) (*Challenge, error) {
	return FromAuthenticate(header)
}

// ToAuthenticate formats this Challenge as an authentication header value.
func (c *Challenge) ToAuthenticate(realm string) string {
	return FormatAuthenticate(c, realm)
}

// ToWWWAuthenticate formats this Challenge as a WWW-Authenticate header value.
func (c *Challenge) ToWWWAuthenticate(realm string) string {
	return c.ToAuthenticate(realm)
}

// Verify checks whether the challenge ID matches the expected HMAC for the
// given secretKey and realm. Uses constant-time comparison.
func (c *Challenge) Verify(secretKey, realm string) bool {
	expected := GenerateChallengeID(GenerateChallengeIDInput{
		SecretKey: secretKey,
		Realm:     realm,
		Method:    c.Method,
		Intent:    c.Intent,
		Request:   c.Request,
		Expires:   c.Expires,
		Digest:    c.Digest,
		Opaque:    c.Opaque,
	})
	return ConstantTimeEqual(c.ID, expected)
}

// ToEcho returns the ChallengeEcho representation suitable for inclusion
// in a Credential.
func (c *Challenge) ToEcho() ChallengeEcho {
	reqB64 := c.RequestB64
	if reqB64 == "" {
		reqB64 = b64EncodeRequest(c.Request)
	}
	return ChallengeEcho{
		ID:      c.ID,
		Realm:   c.Realm,
		Method:  c.Method,
		Intent:  c.Intent,
		Request: reqB64,
		Expires: c.Expires,
		Digest:  c.Digest,
		Opaque:  c.Opaque,
	}
}

func requestForJSON(request map[string]any, requestB64 string) (map[string]any, error) {
	if request != nil {
		return request, nil
	}
	if requestB64 == "" {
		return map[string]any{}, nil
	}
	decoded, err := B64Decode(requestB64)
	if err != nil {
		return nil, fmt.Errorf("mpp: invalid request encoding: %w", err)
	}
	return decoded, nil
}

func decodeJSONRequest(request json.RawMessage, requestB64 string) (map[string]any, string, error) {
	trimmed := bytes.TrimSpace(request)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		if requestB64 == "" {
			requestB64 = b64EncodeRequest(nil)
		}
		decoded, err := B64Decode(requestB64)
		if err != nil {
			return nil, "", fmt.Errorf("mpp: invalid request encoding: %w", err)
		}
		return decoded, requestB64, nil
	}

	decoded, requestFromJSON, err := parseJSONRequestValue(trimmed)
	if err != nil {
		return nil, "", err
	}

	if requestB64 == "" {
		requestB64 = requestFromJSON
		return decoded, requestB64, nil
	}

	decodedB64, err := B64Decode(requestB64)
	if err != nil {
		return nil, "", fmt.Errorf("mpp: invalid request encoding: %w", err)
	}
	if !JSONEqual(decoded, decodedB64) {
		return nil, "", fmt.Errorf("mpp: challenge request and requestB64 do not match")
	}
	return decoded, requestB64, nil
}

func requestB64FromJSON(request json.RawMessage) (string, error) {
	trimmed := bytes.TrimSpace(request)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return b64EncodeRequest(nil), nil
	}

	_, requestB64, err := parseJSONRequestValue(trimmed)
	if err != nil {
		return "", err
	}
	return requestB64, nil
}

func parseJSONRequestValue(trimmed json.RawMessage) (map[string]any, string, error) {
	if len(trimmed) > 0 && trimmed[0] == '"' {
		var requestB64 string
		if err := json.Unmarshal(trimmed, &requestB64); err != nil {
			return nil, "", err
		}
		decoded, err := B64Decode(requestB64)
		if err != nil {
			return nil, "", fmt.Errorf("mpp: invalid request encoding: %w", err)
		}
		return decoded, requestB64, nil
	}

	var request map[string]any
	if err := json.Unmarshal(trimmed, &request); err != nil {
		return nil, "", fmt.Errorf("mpp: request must be an object or base64url string: %w", err)
	}
	if request == nil {
		request = map[string]any{}
	}
	return request, b64EncodeRequest(request), nil
}
