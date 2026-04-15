package server

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/tempoxyz/mpp-go/pkg/mpp"
	"github.com/tempoxyz/mpp-go/pkg/tempo"
	temposigner "github.com/tempoxyz/tempo-go/pkg/signer"
	tempotx "github.com/tempoxyz/tempo-go/pkg/transaction"
)

const receiptRetryDelay = 500 * time.Millisecond
const receiptRetryAttempts = 20

type ChargeIntentConfig struct {
	RPC                tempo.RPCClient
	RPCURL             string
	FeePayerSigner     *temposigner.Signer
	FeePayerPrivateKey string
	Store              tempo.Store
}

type ChargeIntent struct {
	rpc            tempo.RPCClient
	rpcURL         string
	feePayerSigner *temposigner.Signer
	store          tempo.Store
}

func NewChargeIntent(config ChargeIntentConfig) (*ChargeIntent, error) {
	feePayerSigner := config.FeePayerSigner
	if feePayerSigner == nil && config.FeePayerPrivateKey != "" {
		resolved, err := temposigner.NewSigner(config.FeePayerPrivateKey)
		if err != nil {
			return nil, err
		}
		feePayerSigner = resolved
	}
	store := config.Store
	if store == nil {
		store = tempo.NewMemoryStore()
	}
	return &ChargeIntent{
		rpc:            config.RPC,
		rpcURL:         config.RPCURL,
		feePayerSigner: feePayerSigner,
		store:          store,
	}, nil
}

func (i *ChargeIntent) Name() string {
	return tempo.IntentCharge
}

func (i *ChargeIntent) Verify(
	ctx context.Context,
	credential *mpp.Credential,
	requestMap map[string]any,
) (*mpp.Receipt, error) {
	request, err := tempo.ParseChargeRequest(requestMap)
	if err != nil {
		return nil, mpp.ErrBadRequest(err.Error())
	}
	payload, err := tempo.ParseChargeCredentialPayload(credential.Payload)
	if err != nil {
		return nil, mpp.ErrInvalidPayload(err.Error())
	}
	if request.MethodDetails.FeePayer && payload.Type != tempo.CredentialTypeTransaction {
		return nil, mpp.ErrInvalidPayload("fee payer challenges require a transaction credential")
	}
	if !request.Allows(payload.Type) {
		return nil, mpp.ErrInvalidPayload(fmt.Sprintf("credential type %q is not allowed for this challenge", payload.Type))
	}

	rpc := i.resolveRPC(request)

	switch payload.Type {
	case tempo.CredentialTypeHash:
		return i.verifyHash(ctx, rpc, credential, request, payload.Hash)
	case tempo.CredentialTypeTransaction:
		return i.verifyTransaction(ctx, rpc, credential, request, payload.Signature)
	default:
		return nil, mpp.ErrInvalidPayload(fmt.Sprintf("unsupported credential type %q", payload.Type))
	}
}

func (i *ChargeIntent) verifyHash(
	ctx context.Context,
	rpc tempo.RPCClient,
	credential *mpp.Credential,
	request tempo.ChargeRequest,
	hash string,
) (*mpp.Receipt, error) {
	receiptMap, err := fetchReceipt(ctx, rpc, hash)
	if err != nil {
		return nil, err
	}
	if !receiptMatches(receiptMap, credential, request) {
		return nil, mpp.ErrVerificationFailed("transaction receipt does not satisfy the charge request")
	}
	accepted, err := i.store.PutIfAbsent(ctx, tempo.ChargeStoreKey(hash), hash)
	if err != nil {
		return nil, err
	}
	if !accepted {
		return nil, mpp.ErrVerificationFailed("transaction hash already used")
	}
	return mpp.Success(hash, mpp.WithReceiptMethod(tempo.MethodName)), nil
}

func (i *ChargeIntent) verifyTransaction(
	ctx context.Context,
	rpc tempo.RPCClient,
	credential *mpp.Credential,
	request tempo.ChargeRequest,
	raw string,
) (*mpp.Receipt, error) {
	tx, err := tempotx.Deserialize(raw)
	if err != nil {
		return nil, mpp.ErrInvalidPayload("failed to deserialize transaction payload")
	}
	if !transactionMatches(tx, request, credential.Challenge.Realm, credential.Challenge.ID) {
		return nil, mpp.ErrInvalidPayload("transaction does not contain a matching Tempo transfer")
	}

	sender, err := tempotx.VerifySignature(tx)
	if err != nil {
		return nil, mpp.ErrInvalidPayload("transaction signature is invalid")
	}
	if sourceAddress := parseSourceAddress(credential.Source); sourceAddress != "" && !strings.EqualFold(sourceAddress, sender.Hex()) {
		return nil, mpp.ErrInvalidPayload("credential source does not match transaction signer")
	}

	if request.MethodDetails.FeePayer {
		// TODO: Upstream a higher-level sponsored transaction validation + co-sign helper into tempo-go.
		// The low-level signing primitives live there already, but SDKs still duplicate these invariants.
		if !tx.AwaitingFeePayer {
			return nil, mpp.ErrInvalidPayload("fee payer transaction must be marked as awaiting a fee payer")
		}
		if tx.ValidBefore == 0 || time.Now().Unix() >= int64(tx.ValidBefore) {
			return nil, mpp.ErrVerificationFailed("fee payer transaction has expired")
		}
		if tx.NonceKey == nil || tx.NonceKey.Cmp(tempo.ExpiringNonceKey) != 0 {
			return nil, mpp.ErrInvalidPayload("fee payer transaction must use the expiring nonce key")
		}
		if tx.FeeToken != (common.Address{}) {
			return nil, mpp.ErrInvalidPayload("fee payer transaction must omit fee token before co-signing")
		}
		if i.feePayerSigner != nil {
			tx.From = sender
			tx.FeeToken = common.HexToAddress(request.Currency)
			tx.AwaitingFeePayer = false
			if err := tempotx.AddFeePayerSignature(tx, i.feePayerSigner); err != nil {
				return nil, mpp.ErrVerificationFailed("failed to co-sign fee payer transaction")
			}
		} else if request.MethodDetails.FeePayerURL != "" {
			coSignedRaw, err := signWithRemoteFeePayer(ctx, request.MethodDetails.FeePayerURL, raw)
			if err != nil {
				return nil, err
			}
			tx, err = tempotx.Deserialize(coSignedRaw)
			if err != nil {
				return nil, mpp.ErrVerificationFailed("fee payer returned an invalid transaction")
			}
		} else {
			return nil, mpp.ErrVerificationFailed("fee payer challenge requires a configured fee payer signer or fee payer URL")
		}
		if _, _, err := tempotx.VerifyDualSignatures(tx); err != nil {
			return nil, mpp.ErrVerificationFailed("co-signed transaction failed signature verification")
		}
	}

	serialized, err := tempotx.Serialize(tx, nil)
	if err != nil {
		return nil, mpp.ErrVerificationFailed("failed to serialize transaction")
	}

	txHash, err := rpc.SendRawTransaction(ctx, serialized)
	if err != nil {
		return nil, mpp.ErrVerificationFailed("transaction submission failed")
	}
	receiptMap, err := fetchReceipt(ctx, rpc, txHash)
	if err != nil {
		return nil, err
	}
	if !receiptMatches(receiptMap, credential, request) {
		return nil, mpp.ErrVerificationFailed("transaction receipt does not satisfy the charge request")
	}
	accepted, err := i.store.PutIfAbsent(ctx, tempo.ChargeStoreKey(txHash), txHash)
	if err != nil {
		return nil, err
	}
	if !accepted {
		return nil, mpp.ErrVerificationFailed("transaction hash already used")
	}
	return mpp.Success(txHash, mpp.WithReceiptMethod(tempo.MethodName)), nil
}

func (i *ChargeIntent) resolveRPC(request tempo.ChargeRequest) tempo.RPCClient {
	if i.rpc != nil {
		return i.rpc
	}
	if i.rpcURL != "" {
		return tempo.NewRPCClient(i.rpcURL)
	}
	if request.MethodDetails.ChainID != nil {
		return tempo.NewRPCClient(tempo.DefaultRPCURLForChain(*request.MethodDetails.ChainID))
	}
	return tempo.NewRPCClient(tempo.DefaultRPCURLForChain(0))
}

func transactionMatches(tx *tempotx.Tx, request tempo.ChargeRequest, realm, challengeID string) bool {
	if len(tx.Calls) != 1 || len(tx.AccessList) != 0 || tx.KeyAuthorization != nil {
		return false
	}
	for _, call := range tx.Calls {
		if call.To == nil || !strings.EqualFold(call.To.Hex(), request.Currency) {
			continue
		}
		if call.Value != nil && call.Value.Sign() != 0 {
			continue
		}
		if tempo.MatchTransferCalldata(common.Bytes2Hex(call.Data), request, realm, challengeID) {
			return true
		}
	}
	return false
}

// TODO(tempo-go): replace this polling loop with a shared wait-for-receipt helper
// once tempo-go exposes RPC convenience APIs for receipt fetch/retry behavior.
func fetchReceipt(ctx context.Context, rpc tempo.RPCClient, hash string) (map[string]any, error) {
	for attempt := 0; attempt < receiptRetryAttempts; attempt++ {
		response, err := rpc.SendRequest(ctx, "eth_getTransactionReceipt", hash)
		if err != nil {
			return nil, mpp.ErrVerificationFailed("failed to fetch transaction receipt")
		}
		if err := response.CheckError(); err != nil {
			return nil, mpp.ErrVerificationFailed(err.Error())
		}
		if receipt, ok := response.Result.(map[string]any); ok && len(receipt) > 0 {
			status := asString(receipt["status"])
			if status != "0x1" {
				return nil, mpp.ErrVerificationFailed("transaction reverted")
			}
			return receipt, nil
		}
		if attempt < receiptRetryAttempts-1 {
			time.Sleep(receiptRetryDelay)
		}
	}
	return nil, mpp.ErrVerificationFailed("transaction receipt not found")
}

func receiptMatches(receipt map[string]any, credential *mpp.Credential, request tempo.ChargeRequest) bool {
	logs, ok := receipt["logs"].([]any)
	if !ok {
		return false
	}
	expectedSender := parseSourceAddress(credential.Source)
	for _, rawLog := range logs {
		entry, ok := rawLog.(map[string]any)
		if !ok || !strings.EqualFold(asString(entry["address"]), request.Currency) {
			continue
		}
		topics, ok := entry["topics"].([]any)
		if !ok || len(topics) < 3 {
			continue
		}
		fromAddress := tempo.ParseTopicAddress(asString(topics[1]))
		toAddress := tempo.ParseTopicAddress(asString(topics[2]))
		if !strings.EqualFold(toAddress, request.Recipient) {
			continue
		}
		if expectedSender != "" && !strings.EqualFold(fromAddress, expectedSender) {
			continue
		}
		if matchLog(topics, entry, request, credential.Challenge.Realm, credential.Challenge.ID) {
			return true
		}
	}
	return false
}

func matchLog(topics []any, entry map[string]any, request tempo.ChargeRequest, realm, challengeID string) bool {
	amount, err := tempo.ParseHexBigInt(asString(entry["data"]))
	if err != nil {
		return false
	}
	expectedAmount := request.Amount
	if amount.String() != expectedAmount {
		return false
	}
	topic0 := asString(topics[0])
	if request.MethodDetails.Memo != "" {
		if !strings.EqualFold(topic0, tempo.TransferWithMemoTopic.Hex()) || len(topics) < 4 {
			return false
		}
		return strings.EqualFold(asString(topics[3]), request.MethodDetails.Memo)
	}
	if strings.EqualFold(topic0, tempo.TransferTopic.Hex()) {
		return false
	}
	if strings.EqualFold(topic0, tempo.TransferWithMemoTopic.Hex()) && len(topics) >= 4 {
		memo := asString(topics[3])
		return tempo.VerifyAttributionServer(memo, realm) && tempo.VerifyAttributionChallenge(memo, challengeID)
	}
	return false
}

func signWithRemoteFeePayer(ctx context.Context, feePayerURL, raw string) (string, error) {
	response, err := tempo.NewRPCClient(feePayerURL).SendRequest(ctx, "eth_signRawTransaction", raw)
	if err != nil {
		return "", mpp.ErrVerificationFailed("fee payer signing failed")
	}
	if err := response.CheckError(); err != nil {
		return "", mpp.ErrVerificationFailed(err.Error())
	}
	serialized, ok := response.Result.(string)
	if !ok || serialized == "" {
		return "", mpp.ErrVerificationFailed("fee payer returned no signed transaction")
	}
	return serialized, nil
}

func parseSourceAddress(source string) string {
	if source == "" {
		return ""
	}
	parts := strings.Split(source, ":")
	if len(parts) < 5 {
		return ""
	}
	return parts[len(parts)-1]
}

func asString(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	default:
		return fmt.Sprintf("%v", value)
	}
}
