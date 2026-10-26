// Package chargeclient creates Tempo charge Credentials for MPP HTTP clients.
package chargeclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math/big"
	"strings"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	mppclient "github.com/tempoxyz/mpp-go/pkg/client"
	"github.com/tempoxyz/mpp-go/pkg/mpp"
	"github.com/tempoxyz/mpp-go/pkg/tempo"
	temposigner "github.com/tempoxyz/tempo-go/pkg/signer"
	tempotx "github.com/tempoxyz/tempo-go/pkg/transaction"
)

// feePayerMarker is the legacy sponsored-transaction suffix recognized by
// tempo-go during deserialize.
const feePayerMarker = "feefeefeefee"

// Config configures a Tempo charge client method.
type Config struct {
	// Signer signs Tempo transactions and proof payloads.
	Signer *temposigner.Signer
	// PrivateKey constructs Signer when Signer is nil.
	PrivateKey string
	// RPC overrides the Tempo JSON-RPC client used for signing flows.
	RPC tempo.RPCClient
	// RPCURL is used to build an RPC client when RPC is nil.
	RPCURL string
	// ChainID constrains the challenge to a specific Tempo chain when set.
	ChainID int64
	// ClientID is folded into automatically generated attribution memos.
	ClientID string
	// CredentialType selects transaction, hash, or proof credentials.
	CredentialType tempo.CredentialType
	// PaymentPolicy must authorize every nonzero payment before signing or broadcasting.
	// Nil rejects paid challenges. Check the normalized amount (including splits),
	// currency, recipient, and verified RPC chain; enforce cumulative budgets atomically if needed.
	// The callback may run concurrently and must not mutate the request.
	PaymentPolicy func(context.Context, tempo.ChargeRequest) error
}

// Method implements Tempo charge credential creation for the generic MPP client.
type Method struct {
	// now is the clock used for sponsored validity; nil means time.Now.
	now func() time.Time

	signer         *temposigner.Signer
	rpc            tempo.RPCClient
	rpcURL         string
	chainID        int64
	clientID       string
	credentialType tempo.CredentialType
	paymentPolicy  func(context.Context, tempo.ChargeRequest) error

	signingMu sync.Mutex
	signing   map[string]*signingCall
}

// signingCall is one in-flight CreateCredential shared by concurrent callers.
type signingCall struct {
	done       chan struct{}
	waiters    int
	credential *mpp.Credential
	err        error
}

var _ mppclient.Method = (*Method)(nil)

// New constructs a Tempo charge client method.
func New(config Config) (*Method, error) {
	switch config.CredentialType {
	case "", tempo.CredentialTypeTransaction, tempo.CredentialTypeHash, tempo.CredentialTypeProof:
	default:
		return nil, fmt.Errorf("tempo client: unsupported credential type %q", config.CredentialType)
	}
	if config.RPC == nil && config.RPCURL == "" && config.ChainID != 0 && !tempo.IsKnownChainID(config.ChainID) {
		return nil, fmt.Errorf("tempo client: unknown chain id %d; configure RPC or RPCURL explicitly", config.ChainID)
	}
	signer := config.Signer
	if signer == nil {
		if config.PrivateKey == "" {
			return nil, fmt.Errorf("tempo client: signer or private key is required")
		}
		resolved, err := temposigner.NewSigner(config.PrivateKey)
		if err != nil {
			return nil, err
		}
		signer = resolved
	}
	chainID := config.ChainID
	if chainID == 0 {
		chainID = tempo.InferChainIDFromRPCURL(config.RPCURL)
	}
	return &Method{
		signer:         signer,
		rpc:            config.RPC,
		rpcURL:         config.RPCURL,
		chainID:        chainID,
		clientID:       config.ClientID,
		credentialType: config.CredentialType,
		paymentPolicy:  config.PaymentPolicy,
	}, nil
}

// Name returns the method token used in Challenges and Credentials.
func (m *Method) Name() string {
	return tempo.MethodName
}

// Intent returns the charge intent handled by this method.
func (m *Method) Intent() string {
	return tempo.IntentCharge
}

// CreateCredential turns a Tempo charge Challenge into a Tempo Credential.
//
// Concurrent calls for the same challenge share one in-flight signing, so they
// cannot produce two independently settleable transactions. Once that signing
// finishes, successfully or not, a later call signs afresh. Calls share a
// signing only when every challenge field matches, and each caller gets its own
// copy of the credential.
func (m *Method) CreateCredential(ctx context.Context, challenge *mpp.Challenge) (*mpp.Credential, error) {
	if challenge == nil {
		return nil, fmt.Errorf("tempo client: challenge is required")
	}
	encoded, err := json.Marshal(challenge)
	if err != nil {
		return nil, fmt.Errorf("tempo client: encoding challenge: %w", err)
	}
	key := string(encoded)
	for {
		m.signingMu.Lock()
		call, ok := m.signing[key]
		if !ok {
			break
		}
		call.waiters++
		m.signingMu.Unlock()
		select {
		case <-call.done:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		if call.err != nil {
			// The leading call's own context ended. Ours is still live, so
			// sign again (or join a newer signing) instead of failing with it.
			if (errors.Is(call.err, context.Canceled) || errors.Is(call.err, context.DeadlineExceeded)) && ctx.Err() == nil {
				continue
			}
			return nil, call.err
		}
		return cloneCredential(call.credential), nil
	}
	call := &signingCall{done: make(chan struct{})}
	if m.signing == nil {
		m.signing = map[string]*signingCall{}
	}
	m.signing[key] = call
	m.signingMu.Unlock()
	completed := false
	defer func() {
		if !completed {
			// createCredential panicked: the panic continues in this goroutine,
			// and joining calls fail instead of reading a nil credential.
			call.credential, call.err = nil, errSigningPanicked
		}
		m.signingMu.Lock()
		delete(m.signing, key)
		m.signingMu.Unlock()
		close(call.done)
	}()
	credential, err := m.createCredential(ctx, challenge)
	if err == nil && credential == nil {
		err = fmt.Errorf("tempo client: no credential created")
	}
	call.credential, call.err = credential, err
	completed = true
	if err != nil {
		return nil, err
	}
	return cloneCredential(credential), nil
}

var errSigningPanicked = errors.New("tempo client: shared credential signing panicked")

// cloneCredential copies the credential's maps so callers sharing a signing
// never share mutable state.
func cloneCredential(credential *mpp.Credential) *mpp.Credential {
	clone := *credential
	clone.Payload = maps.Clone(credential.Payload)
	clone.Challenge.Opaque = maps.Clone(credential.Challenge.Opaque)
	return &clone
}

func (m *Method) createCredential(ctx context.Context, challenge *mpp.Challenge) (*mpp.Credential, error) {
	if challenge.Method != tempo.MethodName {
		return nil, fmt.Errorf("tempo client: unsupported challenge method %q", challenge.Method)
	}
	if challenge.Intent != tempo.IntentCharge {
		return nil, fmt.Errorf("tempo client: unsupported challenge intent %q", challenge.Intent)
	}

	request, err := tempo.ParseChargeRequest(challenge.Request)
	if err != nil {
		return nil, err
	}

	credentialType := m.credentialType
	if credentialType == "" {
		credentialType = tempo.CredentialTypeTransaction
	}
	expectedChainID, err := m.expectedChainID(request)
	if err != nil {
		return nil, err
	}

	rpc, rpcURL, err := m.resolveRPC(request)
	if err != nil {
		return nil, err
	}
	if rpc == nil {
		rpc = tempo.NewRPCClient(rpcURL)
	}

	chainID, err := rpc.GetChainID(ctx)
	if err != nil {
		return nil, fmt.Errorf("tempo client: get chain id: %w", err)
	}
	if expectedChainID != 0 && int64(chainID) != expectedChainID {
		return nil, fmt.Errorf("tempo client: chain id mismatch (rpc=%d, expected=%d)", chainID, expectedChainID)
	}
	if request.Amount == "0" {
		signature, err := m.signProof(int64(chainID), challenge.ID, challenge.Realm)
		if err != nil {
			return nil, err
		}
		return &mpp.Credential{
			Challenge: challenge.ToEcho(),
			Payload: tempo.ChargeCredentialPayload{
				Type:      tempo.CredentialTypeProof,
				Signature: signature,
			}.Map(),
			Source: tempo.ProofSource(int64(chainID), m.signer.Address()),
		}, nil
	}
	if !request.Allows(credentialType) {
		return nil, fmt.Errorf("tempo client: credential type %q is not allowed for this challenge", credentialType)
	}
	if credentialType == tempo.CredentialTypeProof {
		return nil, fmt.Errorf("tempo client: proof credentials are only valid for zero-amount challenges")
	}
	if credentialType == tempo.CredentialTypeHash && request.MethodDetails.FeePayer {
		return nil, fmt.Errorf("tempo client: hash credentials cannot be used with fee payer challenges")
	}

	if request.MethodDetails.FeePayer {
		// Refuse a too-short sponsored challenge before authorizing payment.
		// valid_before itself is computed just before signing, after the
		// payment policy and gas RPCs, so their latency does not eat into it.
		if _, err := sponsoredValidBefore(challenge.Expires, m.clock()); err != nil {
			return nil, err
		}
	}

	if m.paymentPolicy == nil {
		return nil, fmt.Errorf("tempo client: a PaymentPolicy is required to authorize nonzero payments")
	}
	policyRequest := request
	verifiedChainID := int64(chainID)
	policyRequest.MethodDetails.ChainID = &verifiedChainID
	if err := m.paymentPolicy(ctx, policyRequest); err != nil {
		return nil, fmt.Errorf("tempo client: payment rejected by policy: %w", err)
	}
	memo := tempo.EncodeAttribution(challenge.Realm, m.clientID, challenge.ID)

	rawTx, err := m.buildTransfer(ctx, rpc, request, memo, int64(chainID), challenge.Expires)
	if err != nil {
		return nil, err
	}

	payload := tempo.ChargeCredentialPayload{Type: credentialType}
	if credentialType == tempo.CredentialTypeHash {
		// A transaction hash is 32 bytes, encoded as 0x-prefixed hex.
		//
		// The preflight assumes the server accepts the same 64 KiB Authorization
		// limit as this client and mppx. mpp-go servers released before that
		// limit reject credentials over 16 KiB only after this payment has been
		// broadcast, so upgrade servers before clients.
		preflight := &mpp.Credential{
			Challenge: challenge.ToEcho(),
			Payload:   tempo.ChargeCredentialPayload{Type: credentialType, Hash: "0x" + strings.Repeat("0", 64)}.Map(),
			Source:    tempo.ProofSource(int64(chainID), m.signer.Address()),
		}
		if _, err := mpp.FormatCredential(preflight); err != nil {
			return nil, err
		}
		hash, err := rpc.SendRawTransaction(ctx, rawTx)
		if err != nil {
			return nil, fmt.Errorf("tempo client: send raw transaction: %w", err)
		}
		payload.Hash = hash
	} else {
		payload.Signature = rawTx
	}

	credential := &mpp.Credential{
		Challenge: challenge.ToEcho(),
		Payload:   payload.Map(),
		Source:    tempo.ProofSource(int64(chainID), m.signer.Address()),
	}
	if _, err := mpp.FormatCredential(credential); err != nil {
		return nil, err
	}
	return credential, nil
}

func (m *Method) buildTransfer(
	ctx context.Context,
	rpc tempo.RPCClient,
	request tempo.ChargeRequest,
	memo string,
	chainID int64,
	challengeExpires string,
) (string, error) {
	transfers, err := buildTransfers(request, memo)
	if err != nil {
		return "", err
	}

	gasPrice, err := m.gasPrice(ctx, rpc)
	if err != nil {
		return "", err
	}
	token := common.HexToAddress(request.Currency)
	gasLimit := tempo.DefaultGasLimit
	if len(transfers) == 1 {
		dataHex := transferDataHex(transfers[0])
		if estimated, err := m.estimateGas(ctx, rpc, token.Hex(), dataHex); err == nil && estimated+5_000 > gasLimit {
			gasLimit = estimated + 5_000
		}
	}

	builder := tempotx.NewBuilder(big.NewInt(chainID)).
		SetMaxFeePerGas(gasPrice).
		SetMaxPriorityFeePerGas(new(big.Int).Set(gasPrice)).
		SetGas(gasLimit).
		SetNonceKey(big.NewInt(0))
	for _, transfer := range transfers {
		builder.AddCall(token, big.NewInt(0), common.FromHex(transferDataHex(transfer)))
	}

	if request.MethodDetails.FeePayer {
		validBefore, err := sponsoredValidBefore(challengeExpires, m.clock())
		if err != nil {
			return "", err
		}
		builder.
			SetSponsored(true).
			SetNonceKey(new(big.Int).Set(tempo.ExpiringNonceKey)).
			SetNonce(0).
			SetValidBefore(validBefore)
	} else {
		nonce, err := rpc.GetTransactionCount(ctx, m.signer.Address().Hex())
		if err != nil {
			return "", fmt.Errorf("tempo client: get nonce: %w", err)
		}
		builder.SetNonce(nonce).SetFeeToken(token)
	}
	tx, err := builder.BuildAndValidate()
	if err != nil {
		return "", fmt.Errorf("tempo client: build transaction: %w", err)
	}

	if err := tempotx.SignTransaction(tx, m.signer); err != nil {
		return "", fmt.Errorf("tempo client: sign transaction: %w", err)
	}

	serialized, err := tempotx.Serialize(tx, nil)
	if err != nil {
		return "", fmt.Errorf("tempo client: serialize transaction: %w", err)
	}
	if request.MethodDetails.FeePayer {
		return serialized + strings.TrimPrefix(strings.ToLower(m.signer.Address().Hex()), "0x") + feePayerMarker, nil
	}
	return serialized, nil
}

// ErrSponsoredChallengeExpiresTooSoon reports a sponsored (fee-payer) charge
// challenge that expires less than tempo.FeePayerMinRemainingValidity from
// now. The client refuses it before signing or broadcasting anything, since a
// server would reject the sponsored transaction for too little validity.
var ErrSponsoredChallengeExpiresTooSoon = errors.New("tempo client: sponsored challenge expires too soon")

// CanHandleChallenge skips a sponsored charge challenge that expires too soon
// to sign, so a transport offered other challenges can pay with one of those
// instead of failing on this one.
func (m *Method) CanHandleChallenge(challenge *mpp.Challenge) bool {
	request, err := tempo.ParseChargeRequest(challenge.Request)
	if err != nil || !request.MethodDetails.FeePayer || request.Amount == "0" {
		return true
	}
	_, err = sponsoredValidBefore(challenge.Expires, m.clock())
	return !errors.Is(err, ErrSponsoredChallengeExpiresTooSoon)
}

func (m *Method) clock() time.Time {
	if m.now != nil {
		return m.now()
	}
	return time.Now()
}

// sponsoredValidBefore returns the expiring-nonce valid_before for a sponsored
// charge: now + tempo.SponsoredValidBeforeWindow, capped at
// tempo.FeePayerMinRemainingValidity past the second after the challenge
// expiry. Servers require that much validity left on arrival and accept a
// valid_before up to a minute past the challenge expiry, so with this cap any
// credential that arrives before the challenge expires keeps enough validity.
// It refuses challenges that expire less than
// tempo.FeePayerMinRemainingValidity from now.
func sponsoredValidBefore(expires string, now time.Time) (uint64, error) {
	validBefore := now.Add(tempo.SponsoredValidBeforeWindow).Unix()
	if expires == "" {
		return uint64(validBefore), nil
	}
	expiry, err := mpp.ParseExpires(expires)
	if err != nil {
		return 0, fmt.Errorf("tempo client: invalid challenge expires %q: %w", expires, err)
	}
	if remaining := expiry.Sub(now); remaining < tempo.FeePayerMinRemainingValidity {
		return 0, fmt.Errorf("%w: %s left before %s, need at least %s",
			ErrSponsoredChallengeExpiresTooSoon, remaining.Truncate(time.Millisecond), expires, tempo.FeePayerMinRemainingValidity)
	}
	challengeMax := expiry.Unix() + 1 + int64(tempo.FeePayerMinRemainingValidity/time.Second)
	if challengeMax < validBefore {
		validBefore = challengeMax
	}
	return uint64(validBefore), nil
}

func (m *Method) signProof(chainID int64, challengeID, realm string) (string, error) {
	// Bind the proof to the signing wallet, echoed as the credential source.
	hash, err := tempo.ProofTypedDataHash(chainID, m.signer.Address(), challengeID, realm)
	if err != nil {
		return "", fmt.Errorf("tempo client: build proof payload: %w", err)
	}
	signature, err := m.signer.Sign(hash)
	if err != nil {
		return "", fmt.Errorf("tempo client: sign proof payload: %w", err)
	}
	raw := make([]byte, 65)
	signature.R.FillBytes(raw[:32])
	signature.S.FillBytes(raw[32:64])
	raw[64] = signature.YParity
	return hexutil.Encode(raw), nil
}

type transfer struct {
	amount    *big.Int
	memo      string
	recipient string
}

func buildTransfers(request tempo.ChargeRequest, memo string) ([]transfer, error) {
	totalAmount, ok := new(big.Int).SetString(request.Amount, 10)
	if !ok {
		return nil, fmt.Errorf("tempo client: invalid amount %q", request.Amount)
	}
	primaryAmount := new(big.Int).Set(totalAmount)
	transfers := make([]transfer, 0, len(request.MethodDetails.Splits)+1)
	for _, split := range request.MethodDetails.Splits {
		splitAmount, ok := new(big.Int).SetString(split.Amount, 10)
		if !ok {
			return nil, fmt.Errorf("tempo client: invalid split amount %q", split.Amount)
		}
		primaryAmount.Sub(primaryAmount, splitAmount)
		transfers = append(transfers, transfer{
			amount:    splitAmount,
			memo:      split.Memo,
			recipient: split.Recipient,
		})
	}
	transfers = append([]transfer{{
		amount:    primaryAmount,
		memo:      memo,
		recipient: request.Recipient,
	}}, transfers...)
	return transfers, nil
}

func transferDataHex(transfer transfer) string {
	if transfer.memo != "" {
		dataHex, _ := tempo.EncodeTransferWithMemo(transfer.recipient, transfer.amount, transfer.memo)
		return dataHex
	}
	return tempo.EncodeTransfer(transfer.recipient, transfer.amount)
}

// TODO(tempo-go): replace these JSON-RPC helpers with shared transaction-prep
// helpers once tempo-go exposes gas/estimation convenience methods.
func (m *Method) gasPrice(ctx context.Context, rpc tempo.RPCClient) (*big.Int, error) {
	response, err := rpc.SendRequest(ctx, "eth_gasPrice")
	if err != nil {
		return nil, fmt.Errorf("tempo client: eth_gasPrice: %w", err)
	}
	if err := response.CheckError(); err != nil {
		return nil, err
	}
	value, ok := response.Result.(string)
	if !ok {
		return nil, fmt.Errorf("tempo client: unexpected eth_gasPrice result %T", response.Result)
	}
	parsed, err := tempo.ParseHexBigInt(value)
	if err != nil {
		return nil, err
	}
	return parsed, nil
}

func (m *Method) estimateGas(
	ctx context.Context,
	rpc tempo.RPCClient,
	to string,
	data string,
) (uint64, error) {
	response, err := rpc.SendRequest(ctx, "eth_estimateGas", map[string]any{
		"from": m.signer.Address().Hex(),
		"to":   to,
		"data": data,
	})
	if err != nil {
		return 0, err
	}
	if err := response.CheckError(); err != nil {
		return 0, err
	}
	value, ok := response.Result.(string)
	if !ok {
		return 0, fmt.Errorf("tempo client: unexpected eth_estimateGas result %T", response.Result)
	}
	return tempo.ParseHexUint64(value)
}

func (m *Method) expectedChainID(request tempo.ChargeRequest) (int64, error) {
	if request.MethodDetails.ChainID != nil {
		challengeChainID := *request.MethodDetails.ChainID
		if m.chainID != 0 && challengeChainID != m.chainID {
			return 0, fmt.Errorf("tempo client: challenge chain id mismatch (challenge=%d, expected=%d)", challengeChainID, m.chainID)
		}
		return challengeChainID, nil
	}
	return m.chainID, nil
}

func (m *Method) resolveRPC(request tempo.ChargeRequest) (tempo.RPCClient, string, error) {
	if m.rpc != nil {
		return m.rpc, m.rpcURL, nil
	}
	if m.rpcURL != "" {
		return nil, m.rpcURL, nil
	}
	if request.MethodDetails.ChainID != nil {
		rpcURL, err := tempo.RPCURLForChain(*request.MethodDetails.ChainID)
		if err != nil {
			return nil, "", fmt.Errorf("tempo client: %w; configure RPC or RPCURL explicitly", err)
		}
		return nil, rpcURL, nil
	}
	if m.chainID != 0 {
		rpcURL, err := tempo.RPCURLForChain(m.chainID)
		if err != nil {
			return nil, "", fmt.Errorf("tempo client: %w; configure RPC or RPCURL explicitly", err)
		}
		return nil, rpcURL, nil
	}
	return nil, tempo.DefaultRPCURLForChain(0), nil
}
