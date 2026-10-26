package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"weak"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	mppclient "github.com/tempoxyz/mpp-go/pkg/client"
	"github.com/tempoxyz/mpp-go/pkg/internal/tempoproof"
	"github.com/tempoxyz/mpp-go/pkg/mpp"
)

// maxPaymentAttempts bounds the paid retries of one tool call.
const maxPaymentAttempts = 3

const methodCallTool = "tools/call"

// ErrPaymentDeclined is returned, wrapped, when the approval hook declines a
// payment. It is client.ErrPaymentDeclined, which the HTTP client returns
// when an OnChallenge hook declines, so errors.Is matches either.
var ErrPaymentDeclined = mppclient.ErrPaymentDeclined

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
	// PaymentPreferences ranks Challenges by "method/intent" q-values.
	PaymentPreferences mppclient.PaymentPreferences
	// TrustedTempoProofRealms lists the realms the client signs zero-amount
	// Tempo proofs for. A zero-amount tempo/charge Challenge authenticates the
	// payer at the realm it names, and an MCP server has no origin to compare
	// that realm with, so other realms are refused, like the HTTP Transport
	// refuses realms its origin does not own. Realms compare case-insensitively.
	//
	// Deprecated: Trust the realm in the Tempo charge method instead, with a
	// TrustedProofRealm without an Origin in its Config.TrustedProofRealms
	// (package github.com/tempoxyz/mpp-go/pkg/tempo/client).
	TrustedTempoProofRealms []string
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
	config.TrustedTempoProofRealms = append([]string(nil), config.TrustedTempoProofRealms...)
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
		// Credentials are reused per Challenge for the whole call, like the
		// HTTP Transport, so a rejected or re-offered Challenge is not paid
		// twice. Only an invalid-payload rejection discards the Credential.
		credentials := make(map[credentialKey]*mpp.Credential)
		var sent credentialKey
		result, err := next(ctx, method, req)
		for attempt := 0; ; attempt++ {
			data, invalidPayload, ok := paymentRequired(result, err)
			if !ok || attempt == maxPaymentAttempts {
				return result, err
			}
			if invalidPayload {
				delete(credentials, sent)
			}
			cause := err
			if cause == nil {
				cause = errors.New("mpp: tool result requires payment")
			}
			paid, key, payErr := config.pay(ctx, params, data, credentials, cause)
			if payErr != nil {
				return nil, payErr
			}
			sent = key
			result, err = next(ctx, method, &mcp.ClientRequest[mcp.Params]{Session: session, Params: paid})
		}
	}
}

type credentialKey struct {
	id, method, intent string
}

// pay selects a Challenge, approves and pays it (or reuses the Credential
// already created for it), and returns a copy of the original call's params
// with the Credential in their metadata.
func (config *Config) pay(
	ctx context.Context,
	original *mcp.CallToolParams,
	data *mpp.MCPPaymentRequiredData,
	credentials map[credentialKey]*mpp.Credential,
	cause error,
) (*mcp.CallToolParams, credentialKey, error) {
	offered := make([]mpp.Challenge, 0, len(data.Challenges))
	for _, challenge := range data.Challenges {
		if challenge != nil {
			offered = append(offered, *challenge)
		}
	}
	candidates, err := mppclient.SelectChallengeCandidates(offered, config.Methods, config.PaymentPreferences)
	if err != nil {
		return nil, credentialKey{}, err
	}
	if config.OrderChallenges != nil {
		candidates, err = config.OrderChallenges(ctx, candidates)
		if err != nil {
			return nil, credentialKey{}, err
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
		return nil, credentialKey{}, fmt.Errorf(
			"mpp: no compatible payment method; server offers: %s; client has: %s: %w",
			strings.Join(available, ", "),
			strings.Join(installed, ", "),
			cause,
		)
	}
	var rejected error
	payable := candidates[:0:0]
	for _, candidate := range candidates {
		if err := config.checkOrigin(candidate); err != nil {
			rejected = err
			continue
		}
		payable = append(payable, candidate)
	}
	if len(payable) == 0 {
		return nil, credentialKey{}, fmt.Errorf("%w: %w", rejected, cause)
	}
	selected := payable[0]
	key := credentialKey{id: selected.Challenge.ID, method: selected.Challenge.Method, intent: selected.Challenge.Intent}
	if credential, ok := credentials[key]; ok {
		return withCredential(original, credential), key, nil
	}

	approve := config.OnPaymentRequired
	if override, ok := ctx.Value(onPaymentRequiredKey{}).(onPaymentRequiredOverride); ok {
		approve = override.fn
	}
	if approve != nil {
		approved, err := approve(ctx, selected.Challenge)
		if err != nil {
			return nil, credentialKey{}, err
		}
		if !approved {
			return nil, credentialKey{}, fmt.Errorf("%w: %w", ErrPaymentDeclined, cause)
		}
	}

	credential, err := selected.Method.CreateCredential(ctx, selected.Challenge)
	if err != nil {
		return nil, credentialKey{}, fmt.Errorf("mpp: creating credential for method %q: %w", selected.Challenge.Method, err)
	}
	// Send the Credential in its canonical wire form.
	authorization, err := mpp.FormatCredential(credential)
	if err != nil {
		return nil, credentialKey{}, err
	}
	credential, err = mpp.ParseCredential(authorization)
	if err != nil {
		return nil, credentialKey{}, err
	}
	credentials[key] = credential
	return withCredential(original, credential), key, nil
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
// client.OriginValidator, whether it may pay a Challenge from a server without
// an origin; a realm in the deprecated TrustedTempoProofRealms still passes.
// Other methods keep the built-in rule: a zero-amount tempo/charge Challenge
// is refused unless TrustedTempoProofRealms lists its realm.
func (config *Config) checkOrigin(candidate mppclient.ChallengeCandidate) error {
	challenge := candidate.Challenge
	if validator, ok := candidate.Method.(mppclient.OriginValidator); ok {
		if err := validator.ValidateOrigin(nil, challenge); err != nil && !config.trustsProofRealm(challenge.Realm) {
			return err
		}
		return nil
	}
	if !tempoproof.IsProofChallenge(challenge) || config.trustsProofRealm(challenge.Realm) {
		return nil
	}
	return tempoproof.RealmError(challenge.Realm, "", "trust it with the Tempo method's Config.TrustedProofRealms")
}

func (config *Config) trustsProofRealm(realm string) bool {
	for _, trusted := range config.TrustedTempoProofRealms {
		if strings.EqualFold(trusted, realm) {
			return true
		}
	}
	return false
}

// IsPaymentRequiredError reports whether err is an MCP payment-required or
// payment-verification-failed error that carries at least one Challenge.
func IsPaymentRequiredError(err error) bool {
	_, _, ok := paymentRequired(nil, err)
	return ok
}

// paymentRequired returns the payment-required data of a tool call's error
// or, when it succeeded, of its result metadata. invalidPayload reports an
// invalid-payload rejection (-32602 with payment data), whose Credential must
// be recreated, as the HTTP Transport does.
func paymentRequired(result mcp.Result, err error) (data *mpp.MCPPaymentRequiredData, invalidPayload, ok bool) {
	if err != nil {
		var wireErr *jsonrpc.Error
		if !errors.As(err, &wireErr) {
			return nil, false, false
		}
		switch wireErr.Code {
		case mpp.MCPPaymentRequiredCode, mpp.MCPPaymentVerificationFailedCode:
			data, ok = decodePaymentRequired(wireErr.Data)
			return data, false, ok
		case mpp.MCPInvalidParamsCode:
			data, ok = decodePaymentRequired(wireErr.Data)
			if !ok || data.Problem["type"] != string(mpp.ErrorTypeInvalidPayload) {
				return nil, false, false
			}
			return data, true, true
		}
		return nil, false, false
	}
	toolResult, isTool := result.(*mcp.CallToolResult)
	if !isTool || toolResult == nil {
		return nil, false, false
	}
	value, present := toolResult.Meta[mpp.MCPPaymentRequiredMetaKey]
	if !present {
		return nil, false, false
	}
	raw, marshalErr := json.Marshal(value)
	if marshalErr != nil {
		return nil, false, false
	}
	data, ok = decodePaymentRequired(raw)
	return data, false, ok
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
	if len(data.Challenges) == 0 {
		return nil, false
	}
	return &data, true
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
