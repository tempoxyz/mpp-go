// Package chargeclient creates Tempo charge Credentials for MPP HTTP clients.
package chargeclient

import (
	"context"
	"fmt"
	"math/big"
	"os"
	"strings"
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
	// PrivateKeyEnv loads the signing key from an environment variable when PrivateKey is empty.
	PrivateKeyEnv string
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
}

// Method implements Tempo charge credential creation for the generic MPP client.
type Method struct {
	signer         *temposigner.Signer
	rpc            tempo.RPCClient
	rpcURL         string
	chainID        int64
	clientID       string
	credentialType tempo.CredentialType
}

var _ mppclient.Method = (*Method)(nil)

// New constructs a Tempo charge client method.
func New(config Config) (*Method, error) {
	if config.RPC == nil && config.RPCURL == "" && config.ChainID != 0 && !tempo.IsKnownChainID(config.ChainID) {
		return nil, fmt.Errorf("tempo client: unknown chain id %d; configure RPC or RPCURL explicitly", config.ChainID)
	}
	privateKey := config.PrivateKey
	if privateKey == "" && config.PrivateKeyEnv != "" {
		privateKey = os.Getenv(config.PrivateKeyEnv)
		if privateKey == "" {
			return nil, fmt.Errorf("tempo client: %s is not set", config.PrivateKeyEnv)
		}
	}
	signer := config.Signer
	if signer == nil {
		if privateKey == "" {
			return nil, fmt.Errorf("tempo client: signer or private key is required")
		}
		resolved, err := temposigner.NewSigner(privateKey)
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
	}, nil
}

// Name returns the method token used in Challenges and Credentials.
func (m *Method) Name() string {
	return tempo.MethodName
}

// CreateCredential turns a Tempo charge Challenge into a Tempo Credential.
func (m *Method) CreateCredential(ctx context.Context, challenge *mpp.Challenge) (*mpp.Credential, error) {
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
	if expected := m.expectedChainID(request); expected != 0 && int64(chainID) != expected {
		return nil, fmt.Errorf("tempo client: chain id mismatch (rpc=%d, expected=%d)", chainID, expected)
	}
	if request.Amount == "0" {
		signature, err := m.signProof(int64(chainID), challenge.ID)
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
	if credentialType == tempo.CredentialTypeHash && request.MethodDetails.Memo != "" {
		return nil, fmt.Errorf("tempo client: hash credentials cannot be used with explicit memo challenges")
	}
	if credentialType == tempo.CredentialTypeHash && request.MethodDetails.FeePayer {
		return nil, fmt.Errorf("tempo client: hash credentials cannot be used with fee payer challenges")
	}

	memo := request.MethodDetails.Memo
	if memo == "" {
		memo = tempo.EncodeAttribution(challenge.Realm, m.clientID, challenge.ID)
	}

	rawTx, err := m.buildTransfer(ctx, rpc, request, memo, int64(chainID))
	if err != nil {
		return nil, err
	}

	payload := tempo.ChargeCredentialPayload{Type: credentialType}
	if credentialType == tempo.CredentialTypeHash {
		hash, err := rpc.SendRawTransaction(ctx, rawTx)
		if err != nil {
			return nil, fmt.Errorf("tempo client: send raw transaction: %w", err)
		}
		payload.Hash = hash
	} else {
		payload.Signature = rawTx
	}

	return &mpp.Credential{
		Challenge: challenge.ToEcho(),
		Payload:   payload.Map(),
		Source:    tempo.ProofSource(int64(chainID), m.signer.Address()),
	}, nil
}

func (m *Method) buildTransfer(
	ctx context.Context,
	rpc tempo.RPCClient,
	request tempo.ChargeRequest,
	memo string,
	chainID int64,
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
		builder.
			SetSponsored(true).
			SetNonceKey(new(big.Int).Set(tempo.ExpiringNonceKey)).
			SetNonce(0).
			SetValidBefore(uint64(time.Now().Add(tempo.FeePayerWindow).Unix()))
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

func (m *Method) signProof(chainID int64, challengeID string) (string, error) {
	hash, err := tempo.ProofTypedDataHash(chainID, challengeID)
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

func (m *Method) expectedChainID(request tempo.ChargeRequest) int64 {
	if request.MethodDetails.ChainID != nil {
		return *request.MethodDetails.ChainID
	}
	return m.chainID
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
