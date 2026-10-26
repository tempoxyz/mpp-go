package mpp

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"
	"unicode/utf8"
)

// maxHeaderPayload is the maximum accepted header payload size (16 KB).
const maxHeaderPayload = 16 * 1024

// maxAuthorizationPayload is the maximum accepted Authorization value size
// (64 KB). A credential echoes its challenge, whose request alone may reach
// maxHeaderPayload, so it needs room to expand during JSON and base64url
// encoding. This matches mppx.
const maxAuthorizationPayload = 64 * 1024

// parseAuthParams parses a comma-separated list of key=value or key="value"
// pairs from an auth-param list (RFC 9110 §11.2).
func parseAuthParams(s string) (map[string]string, error) {
	params := make(map[string]string)
	for i := 0; i < len(s); {
		for i < len(s) && (s[i] == ',' || s[i] == ' ' || s[i] == '\t') {
			i++
		}
		if i >= len(s) {
			break
		}

		start := i
		for i < len(s) && isAuthParamKeyChar(s[i]) {
			i++
		}
		key := s[start:i]
		if key == "" {
			return nil, fmt.Errorf("mpp: malformed auth-param")
		}
		// Auth-param names are case-insensitive (RFC 9110 §11.2).
		name := strings.ToLower(key)

		for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
			i++
		}
		if i >= len(s) || s[i] != '=' {
			return nil, fmt.Errorf("mpp: malformed auth-param")
		}
		i++

		for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
			i++
		}

		value, next, err := readAuthParamValue(s, i)
		if err != nil {
			return nil, err
		}
		if _, exists := params[name]; exists {
			return nil, fmt.Errorf("mpp: duplicate auth-param %q", key)
		}
		params[name] = value
		i = next

		for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
			i++
		}
		if i < len(s) && s[i] != ',' {
			// Legacy challenges may contain unescaped quotes in the optional
			// description. Keep the value up to the quote and ignore the
			// malformed, non-semantic suffix.
			if name == "description" {
				break
			}
			return nil, fmt.Errorf("mpp: malformed auth-param separator")
		}
	}
	return params, nil
}

// SplitAuthenticate splits a potentially merged authentication header value into
// individual scheme values while preserving commas inside quoted auth-param values.
func SplitAuthenticate(header string) []string {
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

func isAuthParamKeyChar(ch byte) bool {
	switch {
	case ch >= 'a' && ch <= 'z':
		return true
	case ch >= 'A' && ch <= 'Z':
		return true
	case ch >= '0' && ch <= '9':
		return true
	}
	return ch == '_' || ch == '-'
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

func readAuthParamValue(input string, start int) (string, int, error) {
	if start < len(input) && input[start] == '"' {
		return readQuotedAuthParamValue(input, start+1)
	}
	end := start
	for end < len(input) && input[end] != ',' {
		end++
	}
	return strings.TrimSpace(input[start:end]), end, nil
}

func readQuotedAuthParamValue(input string, start int) (string, int, error) {
	var builder strings.Builder
	for i := start; i < len(input); {
		switch ch := input[i]; ch {
		case '\\':
			i++
			if i >= len(input) {
				return "", 0, fmt.Errorf("mpp: unterminated quoted auth-param")
			}
			if r, next, ok := readUnicodeEscape(input, i); ok {
				builder.WriteRune(r)
				i = next
				continue
			}
			builder.WriteByte(input[i])
			i++
		case '"':
			return builder.String(), i + 1, nil
		default:
			builder.WriteByte(ch)
			i++
		}
	}
	return "", 0, fmt.Errorf("mpp: unterminated quoted auth-param")
}

func readUnicodeEscape(input string, i int) (rune, int, bool) {
	unit, next, ok := readEscapedCodeUnit(input, i)
	if !ok {
		return 0, 0, false
	}
	if !utf16.IsSurrogate(rune(unit)) {
		return rune(unit), next, true
	}
	if next < len(input) && input[next] == '\\' {
		if low, after, ok := readEscapedCodeUnit(input, next+1); ok {
			if r := utf16.DecodeRune(rune(unit), rune(low)); r != utf8.RuneError {
				return r, after, true
			}
		}
	}
	return utf8.RuneError, next, true
}

func readEscapedCodeUnit(input string, i int) (uint16, int, bool) {
	if i >= len(input) || input[i] != 'u' || i+5 > len(input) {
		return 0, 0, false
	}
	value, err := strconv.ParseUint(input[i+1:i+5], 16, 16)
	if err != nil {
		return 0, 0, false
	}
	return uint16(value), i + 5, true
}

// ParseChallenge parses a Payment challenge from a WWW-Authenticate header value.
func ParseChallenge(header string) (*Challenge, error) {
	header = strings.TrimSpace(header)
	if len(header) > maxHeaderPayload {
		return nil, fmt.Errorf("mpp: WWW-Authenticate header exceeds maximum size")
	}

	scheme, rest, ok := strings.Cut(header, " ")
	if !ok || !strings.EqualFold(scheme, SchemePayment) {
		return nil, fmt.Errorf("mpp: expected Payment scheme, got %q", scheme)
	}

	params, err := parseAuthParams(rest)
	if err != nil {
		return nil, err
	}

	id := params["id"]
	realm := params["realm"]
	method := params["method"]
	intent := params["intent"]
	requestB64 := params["request"]

	if id == "" || realm == "" || method == "" || intent == "" || requestB64 == "" {
		return nil, fmt.Errorf("mpp: missing required challenge fields (id, realm, method, intent, request)")
	}
	if !isMethodName(method) {
		return nil, fmt.Errorf("mpp: invalid challenge method %q", method)
	}

	request, err := B64Decode(requestB64)
	if err != nil {
		return nil, fmt.Errorf("mpp: invalid request field: %w", err)
	}

	expires := params["expires"]
	digest := params["digest"]
	description := params["description"]
	credentialHeader, err := parseAdvertisedCredentialHeader(params["header"])
	if err != nil {
		return nil, err
	}

	var opaque map[string]string
	var preservedOpaque string
	if opaqueB64, ok := params["opaque"]; ok && opaqueB64 != "" {
		opaqueMap, err := B64Decode(opaqueB64)
		if err != nil {
			return nil, fmt.Errorf("mpp: invalid opaque field: %w", err)
		}
		opaque = make(map[string]string, len(opaqueMap))
		for k, v := range opaqueMap {
			opaque[k] = anyStr(v)
		}
		preservedOpaque = preservedOpaqueB64(opaque, opaqueB64)
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
		Header:      credentialHeader,

		opaqueB64: preservedOpaque,
	}, nil
}

// FormatChallenge formats c as a WWW-Authenticate header value, the inverse
// of ParseChallenge:
//
//	Payment id="...", realm="...", method="...", intent="...", request="..."
//
// It writes c.Realm, the realm the challenge ID binds. It rejects a nil
// challenge, values that cannot be safely represented in quoted auth-params,
// an invalid advertised credential header, empty required auth-params (id,
// realm, method, intent, request), which would otherwise be omitted and yield
// a challenge ParseChallenge rejects, a method that is not a valid method name
// (lowercase ASCII letters), a RequestB64 that is not base64url-encoded JSON,
// and a header over the size limit.
func FormatChallenge(c *Challenge) (string, error) {
	if c == nil {
		return "", fmt.Errorf("mpp: challenge is nil")
	}
	return formatAuthenticateStrict(c, c.Realm)
}

func formatAuthenticateStrict(c *Challenge, realm string) (string, error) {
	if _, err := parseAdvertisedCredentialHeader(c.Header); err != nil {
		return "", err
	}
	return formatAuthenticate(c, realm, true)
}

func formatAuthenticate(c *Challenge, realm string, strict bool) (string, error) {
	var parts []string
	add := func(k, v string, required bool) error {
		if v == "" {
			if strict && required {
				return fmt.Errorf("mpp: missing required %s auth-param", k)
			}
			return nil
		}
		if strict && strings.ContainsAny(v, "\r\n") {
			return fmt.Errorf("mpp: invalid %s auth-param: contains CR or LF", k)
		}
		parts = append(parts, fmt.Sprintf(`%s="%s"`, k, escapeQuoted(v)))
		return nil
	}

	for _, param := range []struct {
		key      string
		value    string
		required bool
	}{
		{key: "id", value: c.ID, required: true},
		{key: "realm", value: realm, required: true},
		{key: "method", value: c.Method, required: true},
		{key: "intent", value: c.Intent, required: true},
	} {
		if err := add(param.key, param.value, param.required); err != nil {
			return "", err
		}
	}

	if strict && c.Method != "" && !isMethodName(c.Method) {
		return "", fmt.Errorf("mpp: invalid challenge method %q", c.Method)
	}

	reqB64 := c.RequestB64
	if strict && reqB64 != "" {
		if _, err := B64Decode(reqB64); err != nil {
			return "", fmt.Errorf("mpp: invalid challenge request: %w", err)
		}
	}
	if reqB64 == "" {
		var err error
		reqB64, err = b64EncodeRequestWithError(c.Request)
		if err != nil {
			return "", fmt.Errorf("mpp: invalid challenge request: %w", err)
		}
	}
	for _, param := range []struct {
		key      string
		value    string
		required bool
	}{
		{key: "request", value: reqB64, required: true},
		{key: "digest", value: c.Digest},
		{key: "expires", value: c.Expires},
		{key: "description", value: c.Description},
		{key: "header", value: AdvertisedCredentialHeader(c.Header)},
	} {
		if err := add(param.key, param.value, param.required); err != nil {
			return "", err
		}
	}

	if c.Opaque != nil {
		opaque, err := wireOpaque(c.Opaque, c.opaqueB64)
		if err != nil {
			return "", fmt.Errorf("mpp: invalid challenge opaque: %w", err)
		}
		if err := add("opaque", opaque, false); err != nil {
			return "", err
		}
	}

	header := SchemePayment + " " + strings.Join(parts, ", ")
	if len(header) > maxHeaderPayload {
		return "", fmt.Errorf("mpp: WWW-Authenticate header exceeds maximum size")
	}
	return header, nil
}

func b64EncodeRequest(request map[string]any) string {
	encoded, _ := b64EncodeRequestWithError(request)
	return encoded
}

func b64EncodeRequestWithError(request map[string]any) (string, error) {
	if request == nil {
		request = map[string]any{}
	}
	encoded, err := encodeCanonicalJSON(request)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(encoded), nil
}

func escapeQuoted(value string) string {
	// Strip CR and LF unconditionally so the non-strict formatAuthenticate
	// can never emit a header value that splits the HTTP response. FormatChallenge
	// rejects such values earlier with an error; this keeps the default path safe too.
	var builder strings.Builder
	for len(value) > 0 {
		r, size := utf8.DecodeRuneInString(value)
		if r == utf8.RuneError && size == 1 {
			builder.WriteByte(value[0])
			value = value[1:]
			continue
		}
		value = value[size:]
		switch r {
		case '\r', '\n':
			continue
		case '\\':
			builder.WriteString(`\\`)
		case '"':
			builder.WriteString(`\"`)
		default:
			if r <= 0xff {
				builder.WriteRune(r)
				continue
			}
			for _, unit := range utf16.Encode([]rune{r}) {
				fmt.Fprintf(&builder, `\u%04x`, unit)
			}
		}
	}
	return builder.String()
}

// ParseCredential parses a Payment credential from an Authorization header value.
//
// Expected format: Payment <base64url-json>
// The JSON payload contains: challenge (echo), payload, and optional source.
func ParseCredential(header string) (*Credential, error) {
	if len(header) > maxAuthorizationPayload {
		return nil, fmt.Errorf("mpp: Authorization header exceeds maximum size")
	}

	header, err := FindPaymentAuthorizationStrict(header)
	if err != nil {
		return nil, err
	}
	if header == "" {
		return nil, fmt.Errorf("mpp: expected Payment scheme")
	}

	scheme, rest, ok := strings.Cut(header, " ")
	if !ok || !strings.EqualFold(scheme, SchemePayment) {
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
	credentialHeader, err := parseAdvertisedCredentialHeader(anyStr(challengeMap["header"]))
	if err != nil {
		return nil, err
	}
	echo.Header = credentialHeader
	if echo.ID == "" {
		return nil, fmt.Errorf("mpp: credential challenge missing required field: id")
	}
	if echo.Method == "" {
		return nil, fmt.Errorf("mpp: credential challenge missing required field: method")
	}
	if !isMethodName(echo.Method) {
		return nil, fmt.Errorf("mpp: invalid credential challenge method %q", echo.Method)
	}
	if echo.Intent == "" {
		return nil, fmt.Errorf("mpp: credential challenge missing required field: intent")
	}
	if echo.Request == "" {
		return nil, fmt.Errorf("mpp: credential challenge missing required field: request")
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
			echo.opaqueB64 = preservedOpaqueB64(echo.Opaque, opaque)
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

// FormatCredential formats c as an Authorization (or Payment-Authorization)
// header value, the inverse of ParseCredential:
//
//	Payment <base64url-json>
//
// It returns an error for a nil credential, for a credential ParseCredential
// would reject because its challenge echo lacks id, method, intent or request,
// its method is not a valid method name, its advertised credential header is
// not a valid header name or its payload is nil (payload must be a JSON
// object), for values JSON cannot encode, and for a header over the size
// limit.
func FormatCredential(c *Credential) (string, error) {
	if err := validateCredential(c); err != nil {
		return "", err
	}
	header, err := formatAuthorization(c)
	if err != nil {
		return "", err
	}
	if len(header) > maxAuthorizationPayload {
		return "", fmt.Errorf("mpp: Authorization header exceeds maximum size")
	}
	return header, nil
}

// validateCredential rejects a credential ParseCredential would reject.
func validateCredential(c *Credential) error {
	if c == nil {
		return fmt.Errorf("mpp: credential is nil")
	}
	switch echo := c.Challenge; {
	case echo.ID == "":
		return fmt.Errorf("mpp: credential challenge missing required field: id")
	case echo.Method == "":
		return fmt.Errorf("mpp: credential challenge missing required field: method")
	case !isMethodName(echo.Method):
		return fmt.Errorf("mpp: invalid credential challenge method %q", echo.Method)
	case echo.Intent == "":
		return fmt.Errorf("mpp: credential challenge missing required field: intent")
	case echo.Request == "":
		return fmt.Errorf("mpp: credential challenge missing required field: request")
	}
	if _, err := parseAdvertisedCredentialHeader(c.Challenge.Header); err != nil {
		return err
	}
	if c.Payload == nil {
		return fmt.Errorf("mpp: credential payload must be an object")
	}
	return nil
}

func formatAuthorization(c *Credential) (string, error) {
	if c == nil {
		return "", fmt.Errorf("mpp: credential is nil")
	}
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
	if header := AdvertisedCredentialHeader(c.Challenge.Header); header != "" {
		challengeDict["header"] = header
	}
	if c.Challenge.Opaque != nil {
		if c.Challenge.opaqueB64 != "" && opaqueEncodes(c.Challenge.Opaque, c.Challenge.opaqueB64) {
			challengeDict["opaque"] = c.Challenge.opaqueB64
		} else if raw, ok := c.Challenge.Opaque["_raw"]; ok {
			challengeDict["opaque"] = raw
		} else {
			encoded, err := b64EncodeSortedStringMap(c.Challenge.Opaque)
			if err != nil {
				return "", err
			}
			challengeDict["opaque"] = encoded
		}
	}

	payload := map[string]any{
		"challenge": challengeDict,
		"payload":   c.Payload,
	}
	if c.Source != "" {
		payload["source"] = c.Source
	}

	encoded, err := encodeStableJSON(payload)
	if err != nil {
		return "", err
	}
	return SchemePayment + " " + base64.RawURLEncoding.EncodeToString(encoded), nil
}

// ParseReceipt parses a Payment-Receipt header value into a Receipt. It
// requires status "success", reference, method and an RFC 3339 timestamp, and
// keeps method-defined top-level fields in Receipt.Extensions.
//
// Expected format: <base64url-json>
func ParseReceipt(header string) (*Receipt, error) {
	header = strings.TrimSpace(header)
	if len(header) > maxHeaderPayload {
		return nil, fmt.Errorf("mpp: Payment-Receipt header exceeds maximum size")
	}

	data, err := B64Decode(header)
	if err != nil {
		return nil, fmt.Errorf("mpp: invalid receipt encoding: %w", err)
	}
	return receiptFromFields(data)
}

// receiptFromFields validates decoded Payment-Receipt JSON fields and maps
// them onto a Receipt.
func receiptFromFields(data map[string]any) (*Receipt, error) {
	status := anyStr(data["status"])
	if status == "" {
		return nil, fmt.Errorf("mpp: receipt missing status")
	}
	if status != "success" {
		return nil, fmt.Errorf("mpp: invalid receipt status: %q", status)
	}

	reference := anyStr(data["reference"])
	if reference == "" {
		return nil, fmt.Errorf("mpp: receipt missing reference")
	}

	// method and timestamp are required base receipt fields per
	// draft-ietf-httpauth-payment §5.3 and the canonical mppx Receipt schema.
	method := anyStr(data["method"])
	if method == "" {
		return nil, fmt.Errorf("mpp: receipt missing method")
	}
	if !isMethodName(method) {
		return nil, fmt.Errorf("mpp: invalid receipt method %q", method)
	}

	tsRaw := anyStr(data["timestamp"])
	if tsRaw == "" {
		return nil, fmt.Errorf("mpp: receipt missing timestamp")
	}
	ts, err := time.Parse(time.RFC3339Nano, strings.Replace(tsRaw, "Z", "+00:00", 1))
	if err != nil {
		ts, err = time.Parse(time.RFC3339, tsRaw)
		if err != nil {
			return nil, fmt.Errorf("mpp: invalid receipt timestamp: %w", err)
		}
	}

	var externalID string
	if v, ok := data["externalId"]; ok {
		externalID = anyStr(v)
	}

	var subscriptionID string
	if v, ok := data["subscriptionId"]; ok {
		subscriptionID = anyStr(v)
	}

	var fundingCurrency string
	if v, ok := data["fundingCurrency"]; ok {
		fundingCurrency = anyStr(v)
	}

	// Preserve method-defined top-level extension fields. Payment method
	// specifications MAY define additional receipt fields (draft-ietf-httpauth-payment
	// §5.3); the canonical mppx schema keeps them across a parse/format round trip
	// rather than dropping them. Everything outside the known base keys is retained.
	var extensions map[string]any
	for k, v := range data {
		if _, known := receiptBaseFields[k]; known || isEmptyReceiptExtra(k, v) {
			continue
		}
		if extensions == nil {
			extensions = make(map[string]any)
		}
		extensions[k] = v
	}

	return &Receipt{
		Status:          status,
		Timestamp:       ts,
		Reference:       reference,
		Method:          method,
		ExternalID:      externalID,
		SubscriptionID:  subscriptionID,
		FundingCurrency: fundingCurrency,
		Extensions:      extensions,
	}, nil
}

// receiptBaseFields is the set of top-level Payment-Receipt keys the parser
// maps onto named Receipt fields. Any other top-level key is a method-defined
// extension and is preserved via Receipt.Extensions.
//
// fundingCurrency is a method-defined (Tempo) field: the parser fills
// Receipt.FundingCurrency and also keeps it in Extensions, so code that reads
// only Extensions round-trips it. When formatting, the named field wins.
var receiptBaseFields = map[string]struct{}{
	"status":         {},
	"timestamp":      {},
	"reference":      {},
	"method":         {},
	"externalId":     {},
	"subscriptionId": {},
}

// isEmptyReceiptExtra reports whether a top-level receipt field is an empty
// "extra" object, which receipts drop rather than keep as an extension.
// Emptiness is decided by JSON shape, so any value that encodes as {} (such as
// map[string]string{}, a named map type or json.RawMessage("{}")) is dropped
// when formatting, as ParseReceipt drops it, keeping parse/format stable.
func isEmptyReceiptExtra(key string, value any) bool {
	if key != "extra" {
		return false
	}
	if m, ok := value.(map[string]any); ok {
		return len(m) == 0
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		// Left in place so encoding reports the error.
		return false
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, encoded); err != nil {
		return false
	}
	return compact.String() == "{}"
}

// FormatReceiptWithError formats r as a Payment-Receipt header value, the
// inverse of ParseReceipt:
//
//	<base64url-json>
//
// It returns an error for a nil receipt, for a receipt ParseReceipt would
// reject because its status is not "success", its reference or method is
// missing, its method is not a valid method name or the encoded header exceeds
// the size limit, and for Extensions values JSON cannot encode.
func FormatReceiptWithError(r *Receipt) (string, error) {
	if r == nil {
		return "", fmt.Errorf("mpp: receipt is nil")
	}
	switch {
	case r.Status == "":
		return "", fmt.Errorf("mpp: receipt missing status")
	case r.Status != "success":
		return "", fmt.Errorf("mpp: invalid receipt status: %q", r.Status)
	case r.Reference == "":
		return "", fmt.Errorf("mpp: receipt missing reference")
	case r.Method == "":
		return "", fmt.Errorf("mpp: receipt missing method")
	case !isMethodName(r.Method):
		return "", fmt.Errorf("mpp: invalid receipt method %q", r.Method)
	}
	encoded, err := encodeStableJSON(receiptFields(r))
	if err != nil {
		return "", fmt.Errorf("mpp: encoding receipt: %w", err)
	}
	header := base64.RawURLEncoding.EncodeToString(encoded)
	if len(header) > maxHeaderPayload {
		return "", fmt.Errorf("mpp: Payment-Receipt header exceeds maximum size")
	}
	return header, nil
}

// receiptFields returns the canonical Payment-Receipt JSON fields of r.
func receiptFields(r *Receipt) map[string]any {
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
	if r.SubscriptionID != "" {
		data["subscriptionId"] = r.SubscriptionID
	}
	if r.FundingCurrency != "" {
		data["fundingCurrency"] = r.FundingCurrency
	}
	// Re-emit method-defined extension fields at the top level, matching the
	// canonical wire shape. Base fields always win, so reserved keys are never
	// overwritten by an Extensions entry.
	for k, v := range r.Extensions {
		if _, reserved := receiptBaseFields[k]; reserved || isEmptyReceiptExtra(k, v) {
			continue
		}
		if _, set := data[k]; set {
			// A named field (fundingCurrency) wins over its Extensions copy.
			continue
		}
		data[k] = v
	}
	return data
}

// b64EncodeAny encodes a value as compact JSON then base64url without padding.
func b64EncodeAny(data map[string]any) string {
	b, _ := encodeStableJSON(data)
	return base64.RawURLEncoding.EncodeToString(b)
}

func isMethodName(value string) bool {
	for i := 0; i < len(value); i++ {
		ch := value[i]
		if ch < 'a' || ch > 'z' {
			return false
		}
	}
	return value != ""
}

func isHTTPHeaderName(value string) bool {
	if value == "" {
		return false
	}
	for i := 0; i < len(value); i++ {
		if !isTokenChar(value[i]) {
			return false
		}
	}
	return true
}

// B64Decode decodes a base64url (no padding) string into a map.
//
// Numbers decode as json.Number rather than float64. The challenge ID is an
// HMAC over the re-encoded request, so a decode that cannot reproduce the
// bytes it was given cannot reproduce the ID either: float64 carries 53 bits
// of mantissa, and any JSON integer above 2^53 comes back rounded, making a
// challenge the server itself issued fail to verify.
func B64Decode(s string) (map[string]any, error) {
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("base64 decode: %w", err)
	}
	m, err := decodeJSONMap(raw)
	if err != nil {
		return nil, fmt.Errorf("json decode: %w", err)
	}
	return m, nil
}

func decodeJSONMap(raw []byte) (map[string]any, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var m map[string]any
	if err := decoder.Decode(&m); err != nil {
		return nil, err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return nil, fmt.Errorf("unexpected trailing data")
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
