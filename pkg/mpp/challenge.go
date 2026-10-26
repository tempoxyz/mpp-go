package mpp

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// Challenge represents a server-issued payment challenge sent via the
// WWW-Authenticate header.
type Challenge struct {
	ID          string         `json:"id"`
	Method      string         `json:"method"`
	Intent      string         `json:"intent"`
	Request     map[string]any `json:"request,omitempty"`
	Realm       string         `json:"realm,omitempty"`
	RequestB64  string         `json:"requestB64,omitempty"`
	Digest      string         `json:"digest,omitempty"`
	Expires     string         `json:"expires,omitempty"`
	Description string         `json:"description,omitempty"`
	// Opaque is the decoded opaque auth-param: server-defined metadata that
	// clients echo unchanged. mppx calls it meta; set it with WithMeta.
	Opaque map[string]string `json:"opaque,omitempty"`
	// opaqueB64 keeps the issuer's encoded opaque parameter when it differs
	// from the canonical encoding of Opaque, so that formatting echoes it
	// unchanged.
	opaqueB64 string
	// Header is the HTTP field for the Payment credential. Empty means the
	// implicit default, Authorization. When set, it is advertised as the
	// challenge header parameter and bound into the challenge ID.
	Header string `json:"header,omitempty"`
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
	Header  string            `json:"header,omitempty"`
	// opaqueB64 keeps the issuer's encoded opaque parameter when it differs
	// from the canonical encoding of Opaque.
	opaqueB64 string
}

// ChallengeOption configures optional fields when creating a new Challenge.
type ChallengeOption func(*challengeConfig)

type challengeConfig struct {
	expires     string
	digest      string
	description string
	opaque      map[string]string
	header      string
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

// WithHeader sets the HTTP field that must carry the Payment credential.
// Authorization is the implicit default and is not advertised on the wire.
func WithHeader(header string) ChallengeOption {
	return func(c *challengeConfig) { c.header = AdvertisedCredentialHeader(header) }
}

// NewChallenge creates a new Challenge with an HMAC-bound ID. The secretKey
// and realm are used to compute the ID via GenerateChallengeIDWithError. It
// panics where NewChallengeWithError returns an error.
//
// Deprecated: Use NewChallengeWithError instead.
func NewChallenge(secretKey, realm, method, intent string, request map[string]any, opts ...ChallengeOption) *Challenge {
	challenge, err := NewChallengeWithError(secretKey, realm, method, intent, request, opts...)
	if err != nil {
		panic(fmt.Sprintf("mpp: invalid challenge request: %v", err))
	}
	return challenge
}

// NewChallengeWithError creates a new Challenge with an HMAC-bound ID
// computed from secretKey over realm and the other challenge parameters (see
// GenerateChallengeIDWithError). It rejects invalid advertised credential
// headers and request values that JCS cannot represent without changing their
// numeric value. Check the ID of an echoed challenge with Challenge.VerifyID,
// and its Realm against the realm you expect.
func NewChallengeWithError(secretKey, realm, method, intent string, request map[string]any, opts ...ChallengeOption) (*Challenge, error) {
	cfg := &challengeConfig{}
	for _, o := range opts {
		o(cfg)
	}

	requestB64, err := b64EncodeRequestWithError(request)
	if err != nil {
		return nil, err
	}

	id, err := GenerateChallengeIDWithError(GenerateChallengeIDInput{
		SecretKey: secretKey,
		Realm:     realm,
		Method:    method,
		Intent:    intent,
		Request:   request,
		Expires:   cfg.expires,
		Digest:    cfg.digest,
		Opaque:    cfg.opaque,
		Header:    cfg.header,
	})
	if err != nil {
		return nil, err
	}

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
		Header:      cfg.header,
	}, nil
}

// MarshalJSON emits the standard JSON challenge shape with a decoded request
// object, while keeping RequestB64 as an internal cache for header formatting.
func (c Challenge) MarshalJSON() ([]byte, error) {
	request, err := requestForJSON(c.Request, c.RequestB64)
	if err != nil {
		return nil, err
	}
	return json.Marshal(struct {
		ID          string         `json:"id"`
		Method      string         `json:"method"`
		Intent      string         `json:"intent"`
		Request     map[string]any `json:"request"`
		Realm       string         `json:"realm,omitempty"`
		Digest      string         `json:"digest,omitempty"`
		Expires     string         `json:"expires,omitempty"`
		Description string         `json:"description,omitempty"`
		Opaque      any            `json:"opaque,omitempty"`
		Header      string         `json:"header,omitempty"`
	}{
		ID:          c.ID,
		Method:      c.Method,
		Intent:      c.Intent,
		Request:     request,
		Realm:       c.Realm,
		Digest:      c.Digest,
		Expires:     c.Expires,
		Description: c.Description,
		Opaque:      c.opaqueJSON(),
		Header:      AdvertisedCredentialHeader(c.Header),
	})
}

// UnmarshalJSON accepts the standard JSON challenge shape and computes the
// cached RequestB64 field used by header serialization.
func (c *Challenge) UnmarshalJSON(data []byte) error {
	var decoded struct {
		ID          string          `json:"id"`
		Method      string          `json:"method"`
		Intent      string          `json:"intent"`
		Request     json.RawMessage `json:"request"`
		Realm       string          `json:"realm,omitempty"`
		RequestB64  string          `json:"requestB64,omitempty"`
		Digest      string          `json:"digest,omitempty"`
		Expires     string          `json:"expires,omitempty"`
		Description string          `json:"description,omitempty"`
		Opaque      json.RawMessage `json:"opaque,omitempty"`
		Header      string          `json:"header,omitempty"`
	}
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}

	request, requestB64, err := decodeJSONRequest(decoded.Request, decoded.RequestB64)
	if err != nil {
		return err
	}
	opaque, preservedOpaque, err := decodeJSONOpaque(decoded.Opaque)
	if err != nil {
		return err
	}
	header, err := parseAdvertisedCredentialHeader(decoded.Header)
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
		Opaque:      opaque,
		Header:      header,

		opaqueB64: preservedOpaque,
	}
	return nil
}

// FromAuthenticate parses an authentication header value into a Challenge.
//
// Deprecated: Use ParseChallenge instead.
func FromAuthenticate(header string) (*Challenge, error) {
	return ParseChallenge(header)
}

// ToAuthenticate formats this Challenge as an authentication header value
// with the given realm, which the challenge ID does not bind unless it equals
// c.Realm. It returns an empty string when c cannot be formatted.
//
// Deprecated: Use FormatChallenge instead, which writes c.Realm and returns
// formatting errors.
func (c *Challenge) ToAuthenticate(realm string) string {
	header, _ := formatAuthenticate(c, realm, false)
	return header
}

// ToAuthenticateStrict formats this Challenge as an authentication header value
// and rejects values that cannot be safely represented in quoted auth-params.
//
// Deprecated: Use FormatChallenge instead, which writes c.Realm.
func (c *Challenge) ToAuthenticateStrict(realm string) (string, error) {
	return formatAuthenticateStrict(c, realm)
}

// VerifyID reports whether the challenge ID is the HMAC that secretKey binds
// to the challenge's parameters, including c.Realm. It uses constant-time
// comparison. A parsed challenge whose issuer encoded opaque non-canonically
// is verified against that encoding, as it is echoed.
//
// VerifyID only proves that the holder of secretKey issued the ID for c.Realm;
// it does not check that c.Realm is a realm you serve. A server that shares
// secretKey across realms must also compare c.Realm with its own expected
// realm, or a challenge issued for another realm verifies here.
func (c *Challenge) VerifyID(secretKey string) bool {
	return c.verify(secretKey, c.Realm)
}

// Verify checks whether the challenge ID matches the expected HMAC for the
// given secretKey and realm. Uses constant-time comparison.
//
// Deprecated: Use VerifyID together with a c.Realm == realm check. VerifyID
// binds the ID to the challenge's own Realm and does not compare it with the
// realm you expect, so the check is still the caller's.
func (c *Challenge) Verify(secretKey, realm string) bool {
	return c.verify(secretKey, realm)
}

func (c *Challenge) verify(secretKey, realm string) bool {
	expected, err := generateChallengeID(GenerateChallengeIDInput{
		SecretKey: secretKey,
		Realm:     realm,
		Method:    c.Method,
		Intent:    c.Intent,
		Request:   c.Request,
		Expires:   c.Expires,
		Digest:    c.Digest,
		Opaque:    c.Opaque,
		Header:    c.Header,
	}, c.preservedOpaque())
	if err != nil {
		return false
	}
	return ConstantTimeEqual(c.ID, expected)
}

// CredentialHeader returns the HTTP field a client must use for the payment
// credential. Challenges that omit header default to Authorization.
func (c *Challenge) CredentialHeader() string {
	if AdvertisedCredentialHeader(c.Header) == "" {
		return HeaderAuthorization
	}
	return c.Header
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
		Header:  AdvertisedCredentialHeader(c.Header),

		opaqueB64: c.opaqueB64,
	}
}

func (e ChallengeEcho) toChallenge() Challenge {
	return Challenge{
		ID:         e.ID,
		Method:     e.Method,
		Intent:     e.Intent,
		Realm:      e.Realm,
		RequestB64: e.Request,
		Digest:     e.Digest,
		Expires:    e.Expires,
		Opaque:     e.Opaque,
		Header:     AdvertisedCredentialHeader(e.Header),

		opaqueB64: e.opaqueB64,
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
	if !ChallengeBoundJSONEqual(decoded, decodedB64) {
		return nil, "", fmt.Errorf("mpp: challenge request and requestB64 do not match")
	}
	return decoded, requestB64, nil
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

	request, err := decodeJSONMap(trimmed)
	if err != nil {
		return nil, "", fmt.Errorf("mpp: request must be an object or base64url string: %w", err)
	}
	if request == nil {
		request = map[string]any{}
	}
	requestB64, err := b64EncodeRequestWithError(request)
	if err != nil {
		return nil, "", fmt.Errorf("mpp: invalid request encoding: %w", err)
	}
	return request, requestB64, nil
}

// decodeJSONOpaque decodes a JSON opaque value. For a base64url string it also
// returns the encoding to preserve, as preservedOpaqueB64 does.
func decodeJSONOpaque(raw json.RawMessage) (map[string]string, string, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil, "", nil
	}

	if trimmed[0] == '"' {
		var encoded string
		if err := json.Unmarshal(trimmed, &encoded); err != nil {
			return nil, "", err
		}
		if encoded == "" {
			return map[string]string{}, "", nil
		}
		decoded, err := B64Decode(encoded)
		if err != nil {
			return map[string]string{"_raw": encoded}, "", nil
		}
		opaque := make(map[string]string, len(decoded))
		for key, value := range decoded {
			opaque[key] = anyStr(value)
		}
		return opaque, preservedOpaqueB64(opaque, encoded), nil
	}

	var opaque map[string]string
	if err := json.Unmarshal(trimmed, &opaque); err == nil {
		return opaque, "", nil
	}

	var opaqueAny map[string]any
	if err := json.Unmarshal(trimmed, &opaqueAny); err != nil {
		return nil, "", fmt.Errorf("mpp: opaque must be an object or base64url string: %w", err)
	}
	opaque = make(map[string]string, len(opaqueAny))
	for key, value := range opaqueAny {
		opaque[key] = anyStr(value)
	}
	return opaque, "", nil
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

// preservedOpaqueB64 returns encoded when the canonical encoding of opaque
// does not reproduce it, or when opaque has a "_raw" key, which would
// otherwise be taken for the sentinel of an undecodable opaque. The canonical
// encoding is used in all other cases.
func preservedOpaqueB64(opaque map[string]string, encoded string) string {
	if encoded == "" {
		return ""
	}
	if _, hasRaw := opaque["_raw"]; !hasRaw {
		if canonical, err := b64EncodeSortedStringMap(opaque); err == nil && canonical == encoded {
			return ""
		}
	}
	return encoded
}

// preservedOpaque returns the issuer's opaque encoding while it still decodes
// to c.Opaque, and empty otherwise.
func (c *Challenge) preservedOpaque() string {
	if c.Opaque != nil && c.opaqueB64 != "" && opaqueEncodes(c.Opaque, c.opaqueB64) {
		return c.opaqueB64
	}
	return ""
}

// opaqueJSON returns the JSON opaque value: the preserved issuer encoding as a
// base64url string, so that it survives a JSON round trip, or the decoded map.
func (c *Challenge) opaqueJSON() any {
	if encoded := c.preservedOpaque(); encoded != "" {
		return encoded
	}
	return opaqueForJSON(c.Opaque)
}

// wireOpaque returns the opaque parameter to put on the wire. It returns the
// issuer's encoding when that encoding still decodes to opaque. Otherwise it
// returns the canonical encoding of opaque. Clients must not modify opaque, and
// servers can bind the challenge ID to the encoded value.
func wireOpaque(opaque map[string]string, encoded string) (string, error) {
	if encoded != "" && opaqueEncodes(opaque, encoded) {
		return encoded, nil
	}
	return b64EncodeSortedStringMap(opaque)
}

func opaqueEncodes(opaque map[string]string, encoded string) bool {
	decoded, err := B64Decode(encoded)
	if err != nil || len(decoded) != len(opaque) {
		return false
	}
	for key, value := range decoded {
		want, ok := opaque[key]
		if !ok || anyStr(value) != want {
			return false
		}
	}
	return true
}
