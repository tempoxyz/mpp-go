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
	"github.com/tempoxyz/mpp-go/pkg/mpp"
)

// maxPaymentAttempts bounds the paid retries of one tool call.
const maxPaymentAttempts = 3

const methodCallTool = "tools/call"

// ErrPaymentDeclined is returned when the approval hook declines a payment.
var ErrPaymentDeclined = errors.New("mpp: payment declined")

// OnPaymentRequired approves a payment before a Credential is created for
// challenge. Returning false declines it.
type OnPaymentRequired func(ctx context.Context, challenge *mpp.Challenge) (bool, error)

// OrderChallenges filters and sorts the payable Challenges before one is
// paid; the first one returned is paid.
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
}

type wrapper struct {
	config atomic.Pointer[Config]
}

var wrappers sync.Map // weak.Pointer[mcp.Client] -> *wrapper

// Wrap adds automatic payment handling to tool calls made through sessions of
// c and returns c. Sessions connected before and after Wrap both use it.
// Calling Wrap again on the same client replaces its payment configuration.
func Wrap(c *mcp.Client, config Config) *mcp.Client {
	config.Methods = append([]mppclient.Method(nil), config.Methods...)
	config.PaymentPreferences = maps.Clone(config.PaymentPreferences)

	key := weak.Make(c)
	w := new(wrapper)
	actual, loaded := wrappers.LoadOrStore(key, w)
	w = actual.(*wrapper)
	w.config.Store(&config)
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
		result, err := next(ctx, method, req)
		for attempt := 0; ; attempt++ {
			data, ok := paymentRequired(result, err)
			if !ok || attempt == maxPaymentAttempts {
				return result, err
			}
			cause := err
			if cause == nil {
				cause = errors.New("mpp: tool result requires payment")
			}
			paid, payErr := config.pay(ctx, params, data, cause)
			if payErr != nil {
				return nil, payErr
			}
			result, err = next(ctx, method, &mcp.ClientRequest[mcp.Params]{Session: session, Params: paid})
		}
	}
}

// pay selects a Challenge, approves and pays it, and returns a copy of the
// original call's params with the Credential in their metadata.
func (config *Config) pay(
	ctx context.Context,
	original *mcp.CallToolParams,
	data *mpp.MCPPaymentRequiredData,
	cause error,
) (*mcp.CallToolParams, error) {
	offered := make([]mpp.Challenge, 0, len(data.Challenges))
	for _, challenge := range data.Challenges {
		if challenge != nil {
			offered = append(offered, *challenge)
		}
	}
	candidates, err := mppclient.SelectChallengeCandidates(offered, config.Methods, config.PaymentPreferences)
	if err != nil {
		return nil, err
	}
	if config.OrderChallenges != nil {
		candidates, err = config.OrderChallenges(ctx, candidates)
		if err != nil {
			return nil, err
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
		return nil, fmt.Errorf(
			"mpp: no compatible payment method; server offers: %s; client has: %s: %w",
			strings.Join(available, ", "),
			strings.Join(installed, ", "),
			cause,
		)
	}
	selected := candidates[0]

	approve := config.OnPaymentRequired
	if override, ok := ctx.Value(onPaymentRequiredKey{}).(onPaymentRequiredOverride); ok {
		approve = override.fn
	}
	if approve != nil {
		approved, err := approve(ctx, selected.Challenge)
		if err != nil {
			return nil, err
		}
		if !approved {
			return nil, fmt.Errorf("%w: %w", ErrPaymentDeclined, cause)
		}
	}

	credential, err := selected.Method.CreateCredential(ctx, selected.Challenge)
	if err != nil {
		return nil, fmt.Errorf("mpp: creating credential for method %q: %w", selected.Challenge.Method, err)
	}
	// Send the Credential in its canonical wire form.
	authorization, err := mpp.FormatAuthorizationStrict(credential)
	if err != nil {
		return nil, err
	}
	credential, err = mpp.ParseCredential(authorization)
	if err != nil {
		return nil, err
	}

	params := *original
	params.Meta = maps.Clone(original.Meta)
	if params.Meta == nil {
		params.Meta = mcp.Meta{}
	}
	params.Meta[mpp.MCPCredentialMetaKey] = credential
	return &params, nil
}

// IsPaymentRequiredError reports whether err is an MCP payment-required or
// payment-verification-failed error that carries at least one Challenge.
func IsPaymentRequiredError(err error) bool {
	_, ok := paymentRequired(nil, err)
	return ok
}

// paymentRequired returns the payment-required data of a tool call's error
// or, when it succeeded, of its result metadata.
func paymentRequired(result mcp.Result, err error) (*mpp.MCPPaymentRequiredData, bool) {
	if err != nil {
		var wireErr *jsonrpc.Error
		if !errors.As(err, &wireErr) {
			return nil, false
		}
		if wireErr.Code != mpp.MCPPaymentRequiredCode && wireErr.Code != mpp.MCPPaymentVerificationFailedCode {
			return nil, false
		}
		return decodePaymentRequired(wireErr.Data)
	}
	toolResult, ok := result.(*mcp.CallToolResult)
	if !ok || toolResult == nil {
		return nil, false
	}
	value, ok := toolResult.Meta[mpp.MCPPaymentRequiredMetaKey]
	if !ok {
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
