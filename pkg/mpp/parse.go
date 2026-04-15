package mpp

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// maxHeaderPayload is the maximum accepted header payload size (16 KB).
const maxHeaderPayload = 16 * 1024

// parseAuthParams parses a comma-separated list of key=value or key="value"
// pairs from an auth-param list (RFC 9110 §11.2).
func parseAuthParams(s string) map[string]string {
	params := make(map[string]string)
	s = strings.TrimSpace(s)
	for s != "" {
		// Find key.
		eq := strings.IndexByte(s, '=')
		if eq < 0 {
			break
		}
		key := strings.TrimSpace(s[:eq])
		s = strings.TrimSpace(s[eq+1:])

		var val string
		if len(s) > 0 && s[0] == '"' {
			// Quoted value.
			end := 1
			for end < len(s) {
				if s[end] == '\\' && end+1 < len(s) {
					end += 2
					continue
				}
				if s[end] == '"' {
					break
				}
				end++
			}
			val = s[1:end]
			if end < len(s) {
				end++ // skip closing quote
			}
			s = s[end:]
		} else {
			// Token value — ends at comma or end of string.
			end := strings.IndexByte(s, ',')
			if end < 0 {
				val = strings.TrimSpace(s)
				s = ""
			} else {
				val = strings.TrimSpace(s[:end])
				s = s[end:]
			}
		}

		// Trim leading comma/whitespace for next iteration.
		s = strings.TrimLeft(s, ", \t")
		params[key] = val
	}
	return params
}

// SplitChallenges splits a potentially merged authentication header value into
// individual challenges while preserving commas inside quoted auth-param values.
func SplitChallenges(header string) []string {
	header = strings.TrimSpace(header)
	if header == "" {
		return nil
	}
	var parts []string
	inQuote := false
	escaped := false
	start := 0
	for i := 0; i < len(header); i++ {
		switch ch := header[i]; {
		case escaped:
			escaped = false
		case inQuote && ch == '\\':
			escaped = true
		case ch == '"':
			inQuote = !inQuote
		case ch == ',' && !inQuote:
			if next := nextChallengeStart(header, i+1); next >= 0 {
				part := strings.TrimSpace(header[start:i])
				if part != "" {
					parts = append(parts, part)
				}
				start = next
			}
		}
	}
	if tail := strings.TrimSpace(header[start:]); tail != "" {
		parts = append(parts, tail)
	}
	return parts
}

// Deprecated: use SplitChallenges.
func SplitWWWAuthenticate(header string) []string {
	return SplitChallenges(header)
}

func nextChallengeStart(header string, start int) int {
	for start < len(header) && (header[start] == ' ' || header[start] == '\t') {
		start++
	}
	end := start
	for end < len(header) && isTokenChar(header[end]) {
		end++
	}
	if end == start || end >= len(header) {
		return -1
	}
	if header[end] == '=' {
		return -1
	}
	if header[end] == ' ' || header[end] == '\t' {
		return start
	}
	return -1
}

func isTokenChar(ch byte) bool {
	switch {
	case ch >= 'a' && ch <= 'z':
		return true
	case ch >= 'A' && ch <= 'Z':
		return true
	case ch >= '0' && ch <= '9':
		return true
	}
	switch ch {
	case '!', '#', '$', '%', '&', '\'', '*', '+', '-', '.', '^', '_', '`', '|', '~':
		return true
	default:
		return false
	}
}

// ParseWWWAuthenticate parses a WWW-Authenticate header value with scheme
// "Payment" into a Challenge.
func ParseWWWAuthenticate(header string) (*Challenge, error) {
	if len(header) > maxHeaderPayload {
		return nil, fmt.Errorf("mpp: WWW-Authenticate header exceeds maximum size")
	}

	scheme, rest, ok := strings.Cut(header, " ")
	if !ok || !strings.EqualFold(scheme, "Payment") {
		return nil, fmt.Errorf("mpp: expected Payment scheme, got %q", scheme)
	}

	params := parseAuthParams(rest)

	id := params["id"]
	realm := params["realm"]
	method := params["method"]
	intent := params["intent"]
	requestB64 := params["request"]

	if id == "" || realm == "" || method == "" || intent == "" || requestB64 == "" {
		return nil, fmt.Errorf("mpp: missing required challenge fields (id, realm, method, intent, request)")
	}

	var request map[string]any
	var err error
	request, err = B64Decode(requestB64)
	if err != nil {
		return nil, fmt.Errorf("mpp: invalid request field: %w", err)
	}

	expires := params["expires"]
	digest := params["digest"]
	description := params["description"]

	var opaque map[string]string
	if opaqueB64, ok := params["opaque"]; ok && opaqueB64 != "" {
		opaqueMap, err := B64Decode(opaqueB64)
		if err != nil {
			return nil, fmt.Errorf("mpp: invalid opaque field: %w", err)
		}
		opaque = make(map[string]string, len(opaqueMap))
		for k, v := range opaqueMap {
			opaque[k] = anyStr(v)
		}
	}

	return &Challenge{
		ID:          id,
		Method:      method,
		Intent:      intent,
		Request:     request,
		Realm:       realm,
		RequestB64:  requestB64,
		Digest:      digest,
		Expires:     expires,
		Description: description,
		Opaque:      opaque,
	}, nil
}

// FormatWWWAuthenticate formats a Challenge as a WWW-Authenticate header value.
//
// Output format: Payment id="...", realm="...", method="...", intent="...", request="..."
func FormatWWWAuthenticate(c *Challenge, realm string) string {
	var parts []string
	add := func(k, v string) {
		if v != "" {
			parts = append(parts, fmt.Sprintf(`%s="%s"`, k, escapeQuoted(v)))
		}
	}

	add("id", c.ID)
	add("realm", realm)
	add("method", c.Method)
	add("intent", c.Intent)

	reqB64 := c.RequestB64
	if reqB64 == "" {
		reqB64 = b64EncodeRequest(c.Request)
	}
	add("request", reqB64)
	add("digest", c.Digest)
	add("expires", c.Expires)
	add("description", c.Description)

	if len(c.Opaque) > 0 {
		add("opaque", b64EncodeSortedStringMap(c.Opaque))
	}

	return "Payment " + strings.Join(parts, ", ")
}

func b64EncodeRequest(request map[string]any) string {
	if request == nil {
		request = map[string]any{}
	}
	return b64EncodeAny(request)
}

func escapeQuoted(value string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(value)
}

// ParseAuthorization parses an Authorization header value with scheme "Payment"
// into a Credential.
//
// Expected format: Payment <base64url-json>
// The JSON payload contains: challenge (echo), payload, and optional source.
func ParseAuthorization(header string) (*Credential, error) {
	if len(header) > maxHeaderPayload {
		return nil, fmt.Errorf("mpp: Authorization header exceeds maximum size")
	}

	header = ExtractPaymentAuthorization(header)
	if header == "" {
		return nil, fmt.Errorf("mpp: expected Payment scheme")
	}

	scheme, rest, ok := strings.Cut(header, " ")
	if !ok || !strings.EqualFold(scheme, "Payment") {
		return nil, fmt.Errorf("mpp: expected Payment scheme, got %q", scheme)
	}

	b64 := strings.TrimSpace(rest)
	data, err := B64Decode(b64)
	if err != nil {
		return nil, fmt.Errorf("mpp: invalid credential encoding: %w", err)
	}

	challengeRaw, ok := data["challenge"]
	if !ok {
		return nil, fmt.Errorf("mpp: credential missing required field: challenge")
	}
	challengeMap, ok := challengeRaw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("mpp: credential challenge must be an object")
	}

	payloadRaw, ok := data["payload"]
	if !ok {
		return nil, fmt.Errorf("mpp: credential missing required field: payload")
	}
	payload, ok := payloadRaw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("mpp: credential payload must be an object")
	}

	echo := ChallengeEcho{
		ID:      anyStr(challengeMap["id"]),
		Realm:   anyStr(challengeMap["realm"]),
		Method:  anyStr(challengeMap["method"]),
		Intent:  anyStr(challengeMap["intent"]),
		Request: anyStr(challengeMap["request"]),
		Expires: anyStr(challengeMap["expires"]),
		Digest:  anyStr(challengeMap["digest"]),
	}

	if opaqueRaw, ok := challengeMap["opaque"]; ok {
		switch opaque := opaqueRaw.(type) {
		case string:
			decoded, err := B64Decode(opaque)
			if err != nil {
				return nil, fmt.Errorf("mpp: invalid credential opaque field: %w", err)
			}
			echo.Opaque = make(map[string]string, len(decoded))
			for key, value := range decoded {
				echo.Opaque[key] = anyStr(value)
			}
		case map[string]any:
			echo.Opaque = make(map[string]string, len(opaque))
			for key, value := range opaque {
				echo.Opaque[key] = anyStr(value)
			}
		}
	}

	source := anyStr(data["source"])

	return &Credential{
		Challenge: echo,
		Payload:   payload,
		Source:    source,
	}, nil
}

// FormatAuthorization formats a Credential as an Authorization header value.
//
// Output format: Payment <base64url-json>
func FormatAuthorization(c *Credential) string {
	challengeDict := map[string]any{
		"id":      c.Challenge.ID,
		"realm":   c.Challenge.Realm,
		"method":  c.Challenge.Method,
		"intent":  c.Challenge.Intent,
		"request": c.Challenge.Request,
	}
	if c.Challenge.Expires != "" {
		challengeDict["expires"] = c.Challenge.Expires
	}
	if c.Challenge.Digest != "" {
		challengeDict["digest"] = c.Challenge.Digest
	}
	if len(c.Challenge.Opaque) > 0 {
		if raw, ok := c.Challenge.Opaque["_raw"]; ok {
			challengeDict["opaque"] = raw
		} else {
			challengeDict["opaque"] = b64EncodeSortedStringMap(c.Challenge.Opaque)
		}
	}

	payload := map[string]any{
		"challenge": challengeDict,
		"payload":   c.Payload,
	}
	if c.Source != "" {
		payload["source"] = c.Source
	}

	return "Payment " + b64EncodeAny(payload)
}

// ParsePaymentReceipt parses a Payment-Receipt header value into a Receipt.
//
// Expected format: <base64url-json>
func ParsePaymentReceipt(header string) (*Receipt, error) {
	header = strings.TrimSpace(header)
	if len(header) > maxHeaderPayload {
		return nil, fmt.Errorf("mpp: Payment-Receipt header exceeds maximum size")
	}

	data, err := B64Decode(header)
	if err != nil {
		return nil, fmt.Errorf("mpp: invalid receipt encoding: %w", err)
	}

	status := anyStr(data["status"])
	if status == "" {
		return nil, fmt.Errorf("mpp: receipt missing status")
	}
	if status != "success" {
		return nil, fmt.Errorf("mpp: invalid receipt status: %q", status)
	}

	var ts time.Time
	if tsRaw := anyStr(data["timestamp"]); tsRaw != "" {
		ts, err = time.Parse(time.RFC3339Nano, strings.Replace(tsRaw, "Z", "+00:00", 1))
		if err != nil {
			ts, err = time.Parse(time.RFC3339, tsRaw)
			if err != nil {
				return nil, fmt.Errorf("mpp: invalid receipt timestamp: %w", err)
			}
		}
	}

	reference := anyStr(data["reference"])
	if reference == "" {
		return nil, fmt.Errorf("mpp: receipt missing reference")
	}

	method := anyStr(data["method"])

	var externalID string
	if v, ok := data["externalId"]; ok {
		externalID = anyStr(v)
	}

	var extra map[string]any
	if v, ok := data["extra"]; ok {
		if m, ok := v.(map[string]any); ok {
			extra = m
		}
	}

	return &Receipt{
		Status:     status,
		Timestamp:  ts,
		Reference:  reference,
		Method:     method,
		ExternalID: externalID,
		Extra:      extra,
	}, nil
}

// FormatPaymentReceipt formats a Receipt as a Payment-Receipt header value.
//
// Output format: <base64url-json>
func FormatPaymentReceipt(r *Receipt) string {
	data := map[string]any{
		"status":    r.Status,
		"timestamp": r.Timestamp.UTC().Format("2006-01-02T15:04:05.000Z"),
		"reference": r.Reference,
	}
	if r.Method != "" {
		data["method"] = r.Method
	}
	if r.ExternalID != "" {
		data["externalId"] = r.ExternalID
	}
	if len(r.Extra) > 0 {
		data["extra"] = r.Extra
	}
	return b64EncodeAny(data)
}

// b64EncodeAny encodes a value as compact JSON then base64url without padding.
func b64EncodeAny(data map[string]any) string {
	// json.Marshal sorts map keys by default in Go.
	b, _ := json.Marshal(data)
	return base64.RawURLEncoding.EncodeToString(b)
}

// B64Decode decodes a base64url (no padding) string into a map.
func B64Decode(s string) (map[string]any, error) {
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("base64 decode: %w", err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("json decode: %w", err)
	}
	return m, nil
}

// anyStr safely converts an any value to string.
func anyStr(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprintf("%v", v)
}
