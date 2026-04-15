package server

import (
	"context"
	"fmt"
	"math/big"
	"strconv"
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

var feePayerMaxGas = uint64(2_000_000)

var feePayerMaxFeePerGas = big.NewInt(100_000_000_000)

var feePayerMaxPriorityFeePerGas = big.NewInt(100_000_000_000)

var feePayerMaxTotalFee = big.NewInt(50_000_000_000_000_000)

const feePayerMaxValidityWindow = 15 * time.Minute

type sourceDID struct {
	chainID int64
	address string
}

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
	source, err := parseSourceDID(credential.Source)
	if err != nil {
		return nil, mpp.ErrInvalidPayload("credential source is invalid")
	}
	if source != nil && request.MethodDetails.ChainID != nil && source.chainID != *request.MethodDetails.ChainID {
		return nil, mpp.ErrInvalidPayload("credential source chain id does not match the challenge")
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
		return i.verifyHash(ctx, rpc, credential, request, payload.Hash, source)
	case tempo.CredentialTypeTransaction:
		return i.verifyTransaction(ctx, rpc, credential, request, payload.Signature, source)
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
	source *sourceDID,
) (*mpp.Receipt, error) {
	receiptMap, err := fetchReceipt(ctx, rpc, hash)
	if err != nil {
		return nil, err
	}
	if !receiptMatches(receiptMap, credential, request, source) {
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
	source *sourceDID,
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
	if source != nil && !strings.EqualFold(source.address, sender.Hex()) {
		return nil, mpp.ErrInvalidPayload("credential source does not match transaction signer")
	}

	if request.MethodDetails.FeePayer {
		// TODO: Upstream a higher-level sponsored transaction validation + co-sign helper into tempo-go.
		// The low-level signing primitives live there already, but SDKs still duplicate these invariants.
		if err := validateFeePayerTransaction(tx, credential.Challenge.Expires); err != nil {
			return nil, err
		}
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
	if !receiptMatches(receiptMap, credential, request, source) {
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
		if err := ctx.Err(); err != nil {
			return nil, mpp.ErrVerificationFailed(err.Error())
		}
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
			timer := time.NewTimer(receiptRetryDelay)
			select {
			case <-ctx.Done():
				if !timer.Stop() {
					<-timer.C
				}
				return nil, mpp.ErrVerificationFailed(ctx.Err().Error())
			case <-timer.C:
			}
		}
	}
	return nil, mpp.ErrVerificationFailed("transaction receipt not found")
}

func receiptMatches(receipt map[string]any, credential *mpp.Credential, request tempo.ChargeRequest, source *sourceDID) bool {
	logs, ok := receipt["logs"].([]any)
	if !ok {
		return false
	}
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
		if source != nil && !strings.EqualFold(fromAddress, source.address) {
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

func validateFeePayerTransaction(tx *tempotx.Tx, challengeExpires string) error {
	if tx.Gas == 0 {
		return mpp.ErrInvalidPayload("fee payer transaction must declare gas")
	}
	if tx.Gas > feePayerMaxGas {
		return mpp.ErrInvalidPayload("fee payer transaction gas exceeds sponsor policy")
	}
	if tx.MaxFeePerGas == nil || tx.MaxFeePerGas.Sign() <= 0 {
		return mpp.ErrInvalidPayload("fee payer transaction must declare max fee per gas")
	}
	if tx.MaxFeePerGas.Cmp(feePayerMaxFeePerGas) > 0 {
		return mpp.ErrInvalidPayload("fee payer transaction max fee per gas exceeds sponsor policy")
	}
	if tx.MaxPriorityFeePerGas != nil {
		if tx.MaxPriorityFeePerGas.Cmp(tx.MaxFeePerGas) > 0 {
			return mpp.ErrInvalidPayload("fee payer transaction max priority fee exceeds max fee")
		}
		if tx.MaxPriorityFeePerGas.Cmp(feePayerMaxPriorityFeePerGas) > 0 {
			return mpp.ErrInvalidPayload("fee payer transaction max priority fee exceeds sponsor policy")
		}
	}
	maxTotalFee := new(big.Int).Mul(new(big.Int).SetUint64(tx.Gas), tx.MaxFeePerGas)
	if maxTotalFee.Cmp(feePayerMaxTotalFee) > 0 {
		return mpp.ErrInvalidPayload("fee payer transaction total fee budget exceeds sponsor policy")
	}
	if tx.ValidBefore != 0 {
		maxValidBefore := time.Now().Add(feePayerMaxValidityWindow).Unix()
		if challengeExpires != "" {
			if expiry, err := parseExpires(challengeExpires); err == nil {
				challengeMax := expiry.Add(time.Minute).Unix()
				if challengeMax < maxValidBefore {
					maxValidBefore = challengeMax
				}
			}
		}
		if int64(tx.ValidBefore) > maxValidBefore {
			return mpp.ErrInvalidPayload("fee payer transaction validity window exceeds sponsor policy")
		}
	}
	return nil
}

func parseSourceDID(source string) (*sourceDID, error) {
	if source == "" {
		return nil, nil
	}
	parts := strings.Split(source, ":")
	if len(parts) != 5 || parts[0] != "did" || parts[1] != "pkh" || parts[2] != "eip155" {
		return nil, fmt.Errorf("invalid source format")
	}
	if parts[3] == "" || (len(parts[3]) > 1 && parts[3][0] == '0') {
		return nil, fmt.Errorf("invalid source chain id")
	}
	chainID, err := strconv.ParseInt(parts[3], 10, 64)
	if err != nil {
		return nil, err
	}
	if chainID < 0 {
		return nil, fmt.Errorf("invalid source chain id")
	}
	if !common.IsHexAddress(parts[4]) {
		return nil, fmt.Errorf("invalid source address")
	}
	return &sourceDID{chainID: chainID, address: common.HexToAddress(parts[4]).Hex()}, nil
}

func parseExpires(value string) (time.Time, error) {
	if expires, err := time.Parse(time.RFC3339, value); err == nil {
		return expires, nil
	}
	return time.Parse("2006-01-02T15:04:05.000Z", value)
}

func asString(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	default:
		return fmt.Sprintf("%v", value)
	}
}
