package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"unsafe"
	"weak"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	mppclient "github.com/tempoxyz/mpp-go/pkg/client"
	"github.com/tempoxyz/mpp-go/pkg/internal/paymentctx"
	"github.com/tempoxyz/mpp-go/pkg/internal/tempoproof"
	"github.com/tempoxyz/mpp-go/pkg/mcp/internal/payment"
	"github.com/tempoxyz/mpp-go/pkg/mpp"
)

// maxPaymentAttempts bounds the paid retries of one tool call.
const maxPaymentAttempts = 3

const methodCallTool = "tools/call"

// ErrPaymentDeclined is returned, wrapped, when the approval hook declines a
// payment. It is client.ErrPaymentDeclined, which the HTTP client returns
// when an OnChallenge hook declines, so errors.Is matches either.
var ErrPaymentDeclined = mppclient.ErrPaymentDeclined

// ErrPaymentReconciliationRequired indicates a submitted transfer must be reconciled
// before retrying a tool call. Use errors.As to obtain its ReconciliationError.
var ErrPaymentReconciliationRequired = errors.New("mpp: reconcile the sent payment before paying another challenge")

// ReconciliationError preserves the submitted hash or zone Credential and the
// server failure.
// A caller must reconcile Credential before attempting a new payment.
type ReconciliationError struct {
	Credential *mpp.Credential
	Cause      error
}

func (e *ReconciliationError) Error() string { return ErrPaymentReconciliationRequired.Error() }
func (e *ReconciliationError) Unwrap() error {
	return errors.Join(ErrPaymentReconciliationRequired, e.Cause)
}

// OnPaymentRequired approves a payment before a Credential is created for
// challenge. Returning false declines it.
type OnPaymentRequired func(ctx context.Context, challenge *mpp.Challenge) (bool, error)

// OrderChallenges filters and sorts the payable Challenges before one is
// paid; the first one returned that passes the realm checks is paid. It has
// the semantics and signature of client.OrderChallenges, which the HTTP
// client takes with client.WithOrderChallenges, so one hook converts to
// either: mcpclient.OrderChallenges(fn).
type OrderChallenges func(
	ctx context.Context,
	candidates []mppclient.ChallengeCandidate,
) ([]mppclient.ChallengeCandidate, error)

// Config configures payment handling for an MCP client.
type Config struct {
	// Methods are the payment methods the client can pay with.
	Methods []mppclient.Method
	// OnPaymentRequired, when set, approves every payment before it is made.
	OnPaymentRequired OnPaymentRequired
	// OrderChallenges, when set, filters and sorts payable Challenges.
	OrderChallenges OrderChallenges
	// ValidateTempoProofSession, when set, also authorizes the session for a
	// zero-amount Tempo proof's realm before a proof is signed or reused;
	// return an error to refuse. Most clients leave it nil: a proof is then
	// bound to the session's transport, so only a session the client connected
	// with Connect (a StreamableClientTransport with HTTPClient's redirect guard), to
	// an endpoint whose origin the method trusts for the realm (by default an
	// HTTPS endpoint on the default port whose hostname is the realm), gets
	// one, whichever session relays the challenge. Set it for sessions without
	// an endpoint (stdio, in-memory or wrapped transports), which are otherwise
	// refused proofs. It cannot authorize a Streamable HTTP session whose
	// client follows cross-origin redirects, which is always refused; compare the
	// session pointer with sessions you connected yourself, never with a name
	// or realm the peer sent. WithOnPaymentRequired does not bypass it, and the
	// method's OriginValidator still runs with the session's origin.
	ValidateTempoProofSession func(context.Context, *mcp.ClientSession, string) error
	// PaymentPreferences ranks Challenges by "method/intent" q-values.
	PaymentPreferences mppclient.PaymentPreferences
}

type wrapper struct {
	config atomic.Pointer[Config]
}

var wrappers sync.Map // weak.Pointer[mcp.Client] -> *wrapper

// Middleware returns sending middleware that pays for tool calls, to install
// with c.AddSendingMiddleware(mcpclient.Middleware(config)). It is the
// explicit form of Wrap: every session of c, connected before or after,
// uses it, and installing it again adds a second payment layer rather than
// replacing the first.
func Middleware(config Config) mcp.Middleware {
	w := new(wrapper)
	w.config.Store(cloneConfig(config))
	return w.middleware
}

func cloneConfig(config Config) *Config {
	config.Methods = append([]mppclient.Method(nil), config.Methods...)
	config.PaymentPreferences = maps.Clone(config.PaymentPreferences)
	return &config
}

// Wrap adds automatic payment handling to tool calls made through sessions of
// c and returns c. Sessions connected before and after Wrap both use it.
// Calling Wrap again on the same client replaces its payment configuration.
// Middleware installs the same handling without Wrap's per-client registry.
func Wrap(c *mcp.Client, config Config) *mcp.Client {
	key := weak.Make(c)
	w := new(wrapper)
	actual, loaded := wrappers.LoadOrStore(key, w)
	w = actual.(*wrapper)
	w.config.Store(cloneConfig(config))
	if !loaded {
		runtime.AddCleanup(c, func(key weak.Pointer[mcp.Client]) { wrappers.Delete(key) }, key)
		c.AddSendingMiddleware(w.middleware)
	}
	return c
}

type onPaymentRequiredKey struct{}

type onPaymentRequiredOverride struct {
	fn OnPaymentRequired
}

// WithOnPaymentRequired returns a context whose tool calls use fn instead of
// Config.OnPaymentRequired. A nil fn pays without asking.
func WithOnPaymentRequired(ctx context.Context, fn OnPaymentRequired) context.Context {
	return context.WithValue(ctx, onPaymentRequiredKey{}, onPaymentRequiredOverride{fn: fn})
}

func (w *wrapper) middleware(next mcp.MethodHandler) mcp.MethodHandler {
	return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
		if method != methodCallTool {
			return next(ctx, method, req)
		}
		params, ok := req.GetParams().(*mcp.CallToolParams)
		session, sessionOK := req.GetSession().(*mcp.ClientSession)
		if !ok || params == nil || !sessionOK {
			return next(ctx, method, req)
		}
		config := w.config.Load()
		// last is the Credential the last call carried, by the caller or by
		// this middleware, and lastKey the Challenge it answers. As in
		// mppx's client Fetch and the HTTP Transport, a payment-required
		// answer that offers that same Challenge again gets the same
		// Credential, without paying again; any other Challenge gets a new
		// Credential.
		var last *mpp.Credential
		var lastKey credentialKey
		// sentPush is the last push credential (a Tempo hash or zone
		// credential, whose payment is already on chain) submitted for this
		// call, by the caller or by this middleware: as in the HTTP
		// Transport, the call may then only resend it, never pay a new
		// Challenge, which would pay twice.
		var sentPush *mpp.Credential
		authorization, parseErr := payment.Authorization(params.Meta)
		if parseErr != nil {
			return nil, parseErr
		}
		if authorization != "" {
			credential, parseErr := mpp.ParseCredential(authorization)
			if parseErr != nil {
				return nil, parseErr
			}
			key, keyErr := echoCredentialKey(credential.Challenge)
			if keyErr != nil {
				return nil, keyErr
			}
			last, lastKey = credential, key
			if isPushCredential(credential) {
				sentPush = credential
			}
		}
		result, err := next(ctx, method, req)
		for attempt := 0; ; attempt++ {
			data, ok := paymentRequired(result, err)
			if !ok {
				if sentPush != nil && err != nil {
					return result, &ReconciliationError{Credential: sentPush, Cause: err}
				}
				return result, err
			}
			if attempt == maxPaymentAttempts {
				if sentPush != nil {
					return result, &ReconciliationError{Credential: sentPush, Cause: err}
				}
				return result, err
			}
			cause := err
			if cause == nil {
				cause = errors.New("mpp: tool result requires payment")
			}
			paid, key, credential, unpayable, payErr := config.pay(ctx, session, params, data, last, lastKey, cause, sentPush != nil)
			if payErr != nil && sentPush != nil {
				return result, &ReconciliationError{Credential: sentPush, Cause: errors.Join(err, payErr)}
			}
			if unpayable && last != nil {
				// A payment-required answer to a Credential without a
				// Challenge this client may pay (such as a rejected
				// Credential) is the server's final answer, as in mppx.
				return result, err
			}
			if payErr != nil {
				return nil, payErr
			}
			last, lastKey = credential, key
			if isPushCredential(credential) {
				sentPush = credential
			}
			result, err = next(ctx, method, &mcp.ClientRequest[mcp.Params]{Session: session, Params: paid})
		}
	}
}

// credentialKey identifies a Credential by the whole Challenge it pays, so a
// re-offer that keeps the ID but changes the request (such as a proof
// challenge re-offered with a nonzero amount) never reuses it.
type credentialKey struct {
	id, method, intent string
	challenge          string
}

func newCredentialKey(challenge *mpp.Challenge) (credentialKey, error) {
	// The echo is what a Credential for challenge carries.
	return echoCredentialKey(challenge.ToEcho())
}

// echoCredentialKey returns the key of the Challenge a Credential echoes.
// The echo leaves out display fields such as the description.
func echoCredentialKey(echo mpp.ChallengeEcho) (credentialKey, error) {
	echo.Header = mpp.AdvertisedCredentialHeader(echo.Header)
	encoded, err := json.Marshal(echo)
	if err != nil {
		return credentialKey{}, fmt.Errorf("mpp: encoding challenge: %w", err)
	}
	return credentialKey{id: echo.ID, method: echo.Method, intent: echo.Intent, challenge: string(encoded)}, nil
}

// isPushCredential reports whether a credential names a payment the client
// already made on chain: a Tempo hash or zone credential.
func isPushCredential(credential *mpp.Credential) bool {
	if credential == nil {
		return false
	}
	switch credential.Payload["type"] {
	case "hash", "zone":
		return true
	}
	return false
}

// pay selects a Challenge and returns a copy of the original call's params
// with a Credential for it in their metadata, the Challenge's key and the
// Credential. When the selected Challenge is the one last answered (lastKey),
// it sends last again; otherwise it approves and pays the Challenge. With
// pushSent, it only resends last and returns
// ErrPaymentReconciliationRequired instead of creating a Credential.
// unpayable reports that the answer offers no Challenge a Method may pay.
func (config *Config) pay(
	ctx context.Context,
	session *mcp.ClientSession,
	original *mcp.CallToolParams,
	data *mpp.MCPPaymentRequiredData,
	last *mpp.Credential,
	lastKey credentialKey,
	cause error,
	pushSent bool,
) (params *mcp.CallToolParams, key credentialKey, credential *mpp.Credential, unpayable bool, err error) {
	offered := make([]mpp.Challenge, 0, len(data.Challenges))
	for _, challenge := range data.Challenges {
		if challenge != nil {
			offered = append(offered, *challenge)
		}
	}
	if len(offered) == 0 {
		// Every offered Challenge selected an unsupported credential header
		// and was dropped: none may be paid.
		return nil, credentialKey{}, nil, true, fmt.Errorf("%w: every offered challenge selects an unsupported credential header: %w: %w",
			mppclient.ErrNoMethodFound, mpp.ErrUnsupportedCredentialHeader, cause)
	}
	candidates, err := mppclient.SelectChallengeCandidates(offered, config.Methods, config.PaymentPreferences)
	if err != nil {
		return nil, credentialKey{}, nil, false, err
	}
	if config.OrderChallenges != nil {
		candidates, err = config.OrderChallenges(ctx, candidates)
		if err != nil {
			return nil, credentialKey{}, nil, false, err
		}
	}
	if len(candidates) == 0 {
		available := make([]string, len(offered))
		for i, challenge := range offered {
			available[i] = challenge.Method + "." + challenge.Intent
		}
		installed := make([]string, len(config.Methods))
		for i, method := range config.Methods {
			installed[i] = method.Name() + "." + method.Intent()
		}
		return nil, credentialKey{}, nil, true, fmt.Errorf(
			"mpp: no compatible payment method; server offers: %s; client has: %s: %w",
			strings.Join(available, ", "),
			strings.Join(installed, ", "),
			cause,
		)
	}
	var rejected error
	payable := candidates[:0:0]
	for _, candidate := range candidates {
		if err := config.checkOrigin(ctx, session, candidate); err != nil {
			rejected = err
			continue
		}
		payable = append(payable, candidate)
	}
	if len(payable) == 0 {
		return nil, credentialKey{}, nil, false, fmt.Errorf("%w: %w", rejected, cause)
	}
	selected := payable[0]
	key, err = newCredentialKey(selected.Challenge)
	if err != nil {
		return nil, credentialKey{}, nil, false, err
	}
	if last != nil && key == lastKey {
		// The same Challenge again: the outcome of its Credential is still
		// pending, so send that Credential again rather than paying twice.
		return withCredential(original, last), key, last, false, nil
	}
	// A hash or zone credential proves an already-broadcast payment. A fresh
	// challenge must not create another payment for the same tool call.
	if pushSent {
		return nil, credentialKey{}, nil, false, ErrPaymentReconciliationRequired
	}

	approve := config.OnPaymentRequired
	if override, ok := ctx.Value(onPaymentRequiredKey{}).(onPaymentRequiredOverride); ok {
		approve = override.fn
	}
	if approve != nil {
		approved, err := approve(ctx, selected.Challenge)
		if err != nil {
			return nil, credentialKey{}, nil, false, err
		}
		if !approved {
			return nil, credentialKey{}, nil, false, fmt.Errorf("%w: %w", ErrPaymentDeclined, cause)
		}
	}

	// A payment still settling is resumed for the same challenge, not paid
	// again under a new one. MCP carries no origin for the method to check.
	credential, err = paymentctx.RetryPending(ctx, maxPaymentAttempts, func() (*mpp.Credential, error) {
		return selected.Method.CreateCredential(ctx, selected.Challenge)
	})
	if err != nil {
		return nil, credentialKey{}, nil, false, fmt.Errorf("mpp: creating credential for method %q: %w", selected.Challenge.Method, err)
	}
	// Send the Credential in its canonical wire form.
	authorization, err := mpp.FormatCredential(credential)
	if err != nil {
		return nil, credentialKey{}, nil, false, err
	}
	credential, err = mpp.ParseCredential(authorization)
	if err != nil {
		return nil, credentialKey{}, nil, false, err
	}
	return withCredential(original, credential), key, credential, false, nil
}

func withCredential(original *mcp.CallToolParams, credential *mpp.Credential) *mcp.CallToolParams {
	params := *original
	params.Meta = maps.Clone(original.Meta)
	if params.Meta == nil {
		params.Meta = mcp.Meta{}
	}
	params.Meta[mpp.MCPCredentialMetaKey] = credential
	return &params
}

// checkOrigin asks the candidate's method, when it implements
// client.OriginValidator, whether it may pay a Challenge from the server at
// the session's transport origin (see sessionOrigin), nil when the session
// has none. Other methods keep the built-in rule: a zero-amount tempo/charge
// Challenge is paid only when that origin owns its realm.
//
// A proof authenticates the payer at the realm, so it is bound to the session
// that received the challenge: a session without a transport origin gets one
// only when Config.ValidateTempoProofSession authorizes it, so a server cannot
// relay another server's challenge through a shared client.
func (config *Config) checkOrigin(ctx context.Context, session *mcp.ClientSession, candidate mppclient.ChallengeCandidate) error {
	challenge := candidate.Challenge
	// origin is the session's endpoint even when its HTTP client follows
	// cross-origin redirects, so such a session is told apart from stdio and
	// refused proofs even when the hook approves it.
	origin, redirectSafe := sessionEndpoint(session)
	if tempoproof.IsProofChallenge(challenge) {
		switch {
		case origin != nil && !redirectSafe:
			// A redirect could carry the proof to another server, whatever the
			// hook says, so the hook only vouches for origin-less sessions.
			return tempoproof.RealmError(challenge.Realm, "", "connect with mcpclient.Connect, or set the transport's HTTPClient to mcpclient.HTTPClient(nil)")
		case config.ValidateTempoProofSession != nil:
			if session == nil {
				return tempoproof.RealmError(challenge.Realm, "", "the request has no MCP session")
			}
			if err := config.ValidateTempoProofSession(ctx, session, challenge.Realm); err != nil {
				return err
			}
		case origin == nil:
			return tempoproof.RealmError(challenge.Realm, "", "connect with mcpclient.Connect (or set the transport's HTTPClient to mcpclient.HTTPClient(nil)), or authorize the session with Config.ValidateTempoProofSession")
		}
	}
	if !redirectSafe {
		// A redirect could carry any Credential to another server, so a
		// method sees no origin (as on stdio) rather than the endpoint's. A
		// proof from such an endpoint was refused above.
		origin = nil
	}
	if validator, ok := candidate.Method.(mppclient.OriginValidator); ok {
		return validator.ValidateOrigin(origin, challenge)
	}
	if !tempoproof.IsProofChallenge(challenge) || tempoproof.OriginOwnsRealm(origin, challenge.Realm) {
		return nil
	}
	return tempoproof.RealmError(challenge.Realm, tempoproof.Origin(origin), "trust it with the Tempo method's Config.TrustedProofRealms")
}

// IsPaymentRequiredError reports whether err is an MCP payment-required error,
// or a payment-verification-failed error whose httpStatus is 402 or absent,
// that offers at least one Challenge,
// including one whose every Challenge selects an unsupported credential
// header (mpp.ErrUnsupportedCredentialHeader), which no Method may pay.
func IsPaymentRequiredError(err error) bool {
	_, ok := paymentRequired(nil, err)
	return ok
}

// paymentRequired returns the payment-required data of a tool call's error
// or, when it succeeded, of its result metadata. Only a payment-required
// answer starts a new round, as in mppx: a -32042 error, a -32043 error whose
// httpStatus is 402 or absent, or a result with payment-required metadata.
// Any other answer, such as an invalid-params (-32602) rejection, is final.
func paymentRequired(result mcp.Result, err error) (data *mpp.MCPPaymentRequiredData, ok bool) {
	if err != nil {
		var wireErr *jsonrpc.Error
		if !errors.As(err, &wireErr) {
			return nil, false
		}
		switch wireErr.Code {
		case mpp.MCPPaymentRequiredCode:
			return decodePaymentRequired(wireErr.Data)
		case mpp.MCPPaymentVerificationFailedCode:
			data, ok = decodePaymentRequired(wireErr.Data)
			if !ok || (data.HTTPStatus != 0 && data.HTTPStatus != http.StatusPaymentRequired) {
				return nil, false
			}
			return data, true
		}
		return nil, false
	}
	toolResult, isTool := result.(*mcp.CallToolResult)
	if !isTool || toolResult == nil {
		return nil, false
	}
	value, present := toolResult.Meta[mpp.MCPPaymentRequiredMetaKey]
	if !present {
		return nil, false
	}
	raw, marshalErr := json.Marshal(value)
	if marshalErr != nil {
		return nil, false
	}
	return decodePaymentRequired(raw)
}

func decodePaymentRequired(raw json.RawMessage) (*mpp.MCPPaymentRequiredData, bool) {
	if len(raw) == 0 {
		return nil, false
	}
	var data mpp.MCPPaymentRequiredData
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, false
	}
	for _, challenge := range data.Challenges {
		if challenge == nil || challenge.ID == "" || challenge.Method == "" || challenge.Intent == "" {
			return nil, false
		}
	}
	if len(data.Challenges) == 0 && !offeredChallenges(raw) {
		return nil, false
	}
	return &data, true
}

// offeredChallenges reports whether payment-required data lists any
// Challenge, including ones MCPPaymentRequiredData dropped because they
// select an unsupported credential header: such a response still requires
// payment, but has no Challenge a Method may pay.
func offeredChallenges(raw json.RawMessage) bool {
	var offered struct {
		Challenges []json.RawMessage `json:"challenges"`
	}
	return json.Unmarshal(raw, &offered) == nil && len(offered.Challenges) > 0
}

// Receipt returns the payment receipt in a tool result's
// _meta["org.paymentauth/receipt"], or nil when it has none.
func Receipt(result *mcp.CallToolResult) (*mpp.MCPReceipt, error) {
	if result == nil {
		return nil, nil
	}
	value, ok := result.Meta[mpp.MCPReceiptMetaKey]
	if !ok {
		return nil, nil
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("mpp: invalid MCP receipt: %w", err)
	}
	var receipt mpp.MCPReceipt
	if err := json.Unmarshal(raw, &receipt); err != nil {
		return nil, err
	}
	return &receipt, nil
}

// mcpPackage is the import path of the MCP Go SDK's mcp package.
var mcpPackage = reflect.TypeFor[mcp.ClientSession]().PkgPath()

// Connect connects c to the Streamable HTTP endpoint of transport, like
// c.Connect, with the transport's HTTPClient (http.DefaultClient when nil)
// wrapped by HTTPClient so it refuses cross-origin redirects. Sessions
// connected this way get zero-amount Tempo proofs for realms their endpoint's
// origin is trusted for, with no other configuration. transport is copied,
// not modified.
func Connect(ctx context.Context, c *mcp.Client, transport *mcp.StreamableClientTransport, opts *mcp.ClientSessionOptions) (*mcp.ClientSession, error) {
	if transport == nil {
		return nil, errors.New("mpp: Connect needs a StreamableClientTransport")
	}
	safe := *transport
	if !refusesCrossOriginRedirects(safe.HTTPClient) {
		safe.HTTPClient = HTTPClient(safe.HTTPClient)
	}
	return c.Connect(ctx, &safe, opts)
}

// HTTPClient returns a copy of base (http.DefaultClient when nil) whose
// CheckRedirect refuses every redirect away from the origin of the request it
// follows, after base's own CheckRedirect (or net/http's 10-redirect limit).
// Connect sets it for you; otherwise give it to mcp.StreamableClientTransport
// as HTTPClient: zero-amount Tempo
// proofs are signed only for Streamable HTTP sessions whose client refuses
// cross-origin redirects, since a redirect would deliver the proof to another
// server than the endpoint whose origin vouched for the realm.
//
// It is not inlined, so every client it returns shares one CheckRedirect code
// pointer, which refusesCrossOriginRedirects recognizes.
//
//go:noinline
func HTTPClient(base *http.Client) *http.Client {
	if base == nil {
		base = http.DefaultClient
	}
	c := *base
	prior := base.CheckRedirect
	c.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if prior != nil {
			if err := prior(req, via); err != nil {
				return err
			}
		} else if len(via) >= 10 {
			return errors.New("stopped after 10 redirects")
		}
		if len(via) > 0 && tempoproof.Origin(req.URL) != tempoproof.Origin(via[0].URL) {
			return fmt.Errorf("mpp: refusing cross-origin redirect from %q to %q", tempoproof.Origin(via[0].URL), tempoproof.Origin(req.URL))
		}
		return nil
	}
	return &c
}

// sameOriginHook is the code pointer of the CheckRedirect closure HTTPClient
// installs; every client HTTPClient returns shares it.
var sameOriginHook = reflect.ValueOf(HTTPClient(nil).CheckRedirect).Pointer()

// refusesCrossOriginRedirects reports whether c's CheckRedirect is the one
// HTTPClient installs, which refuses every cross-origin redirect whatever
// hook it wraps. Any other hook could allow some origins, so it does not
// count, nor does a nil client or hook (net/http follows redirects).
func refusesCrossOriginRedirects(c *http.Client) bool {
	return c != nil && c.CheckRedirect != nil && reflect.ValueOf(c.CheckRedirect).Pointer() == sameOriginHook
}

// sessionOrigin returns sessionEndpoint's endpoint when its HTTP client
// refuses cross-origin redirects, and nil otherwise.
func sessionOrigin(session *mcp.ClientSession) *url.URL {
	if endpoint, redirectSafe := sessionEndpoint(session); redirectSafe {
		return endpoint
	}
	return nil
}

// sessionEndpoint returns the endpoint URL of the mcp.StreamableClientTransport
// session was connected with, or nil when it has none: other transports
// (stdio, in-memory, SSE, a wrapped transport), a nil session, or an SDK
// version whose layout this does not recognize, so it fails closed.
// redirectSafe reports whether the transport's HTTP client was built by
// HTTPClient, so it refuses cross-origin redirects; http.DefaultClient, the
// SDK's default, follows them.
//
// The endpoint is what this client configured and dialed, not anything the
// server sent, so it identifies the server the way a request URL does over
// HTTP. The SDK does not export a session's transport, so it is read with
// reflection; TestSessionOriginReadsStreamableEndpoint pins the layout.
func sessionEndpoint(session *mcp.ClientSession) (endpoint *url.URL, redirectSafe bool) {
	if session == nil {
		return nil, false
	}
	conn := reflect.ValueOf(session).Elem().FieldByName("mcpConn")
	if conn.Kind() != reflect.Interface || conn.IsNil() {
		return nil, false
	}
	conn = conn.Elem()
	if conn.Kind() != reflect.Pointer || conn.IsNil() {
		return nil, false
	}
	if typ := conn.Type().Elem(); typ.PkgPath() != mcpPackage || typ.Name() != "streamableClientConn" {
		return nil, false
	}
	raw := conn.Elem().FieldByName("url")
	if raw.Kind() != reflect.String {
		return nil, false
	}
	u, err := url.Parse(raw.String())
	if err != nil || u.Scheme == "" || u.Host == "" {
		return nil, false
	}
	// The connection sends with this client, http.DefaultClient when nil.
	field := conn.Elem().FieldByName("client")
	if !field.IsValid() || field.Type() != reflect.TypeFor[*http.Client]() || !field.CanAddr() {
		return u, false
	}
	c := *(**http.Client)(unsafe.Pointer(field.UnsafeAddr()))
	if c == nil {
		c = http.DefaultClient
	}
	return u, refusesCrossOriginRedirects(c)
}
