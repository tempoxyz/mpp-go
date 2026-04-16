package mpp

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

// FromAuthenticate parses an authentication header value into a Challenge.
func FromAuthenticate(header string) (*Challenge, error) {
	return ParseChallenge(header)
}

// ToAuthenticate formats this Challenge as an authentication header value.
func (c *Challenge) ToAuthenticate(realm string) string {
	return FormatAuthenticate(c, realm)
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
