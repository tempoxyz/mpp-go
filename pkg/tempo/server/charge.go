package chargeserver

import (
	"context"
	"encoding/hex"
	"fmt"
	"math/big"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/tempoxyz/mpp-go/pkg/mpp"
	"github.com/tempoxyz/mpp-go/pkg/tempo"
	"github.com/tempoxyz/tempo-go/pkg/keychain"
	temposigner "github.com/tempoxyz/tempo-go/pkg/signer"
	tempotx "github.com/tempoxyz/tempo-go/pkg/transaction"
)

const receiptRetryDelay = 500 * time.Millisecond
const receiptRetryAttempts = 20

// Sponsor policy caps for fee-payer transactions. These values match the
// narrow Tempo charge flow this package supports today.
var feePayerMaxGas = uint64(2_000_000)

var feePayerMaxFeePerGas = big.NewInt(100_000_000_000)

var feePayerMaxPriorityFeePerGas = big.NewInt(100_000_000_000)

var feePayerMaxTotalFee = big.NewInt(50_000_000_000_000_000)

var feeControllerAddress = common.HexToAddress("0xfeec000000000000000000000000000000000000")

const feePayerMaxValidityWindow = 15 * time.Minute

type sourceDID struct {
	chainID int64
	address string
}

// IntentConfig configures Tempo charge verification.
type IntentConfig struct {
	// RPC overrides the Tempo JSON-RPC client used for verification.
	RPC tempo.RPCClient
	// RPCURL is used to build an RPC client when RPC is nil.
	RPCURL string
	// FeePayerSigner co-signs sponsored transactions locally when provided.
	FeePayerSigner *temposigner.Signer
	// FeePayerPrivateKey constructs FeePayerSigner when FeePayerSigner is nil.
	FeePayerPrivateKey string
	// FeePayerPrivateKeyEnv loads the fee-payer key from an environment variable when FeePayerPrivateKey is empty.
	FeePayerPrivateKeyEnv string
	// Store persists replay-protection keys for hash and proof credentials.
	Store tempo.Store
}

// Intent verifies Tempo charge Credentials and returns Receipts.
type Intent struct {
	rpc            tempo.RPCClient
	rpcURL         string
	feePayerSigner *temposigner.Signer
	store          tempo.Store
}

// NewIntent constructs a Tempo charge verifier.
func NewIntent(config IntentConfig) (*Intent, error) {
	feePayerPrivateKey := config.FeePayerPrivateKey
	if feePayerPrivateKey == "" && config.FeePayerPrivateKeyEnv != "" {
		feePayerPrivateKey = os.Getenv(config.FeePayerPrivateKeyEnv)
		if feePayerPrivateKey == "" {
			return nil, fmt.Errorf("tempo server: %s is not set", config.FeePayerPrivateKeyEnv)
		}
	}
	feePayerSigner := config.FeePayerSigner
	if feePayerSigner == nil && feePayerPrivateKey != "" {
		resolved, err := temposigner.NewSigner(feePayerPrivateKey)
		if err != nil {
			return nil, err
		}
		feePayerSigner = resolved
	}
	store := config.Store
	if store == nil {
		store = tempo.NewMemoryStore()
	}
	return &Intent{
		rpc:            config.RPC,
		rpcURL:         config.RPCURL,
		feePayerSigner: feePayerSigner,
		store:          store,
	}, nil
}

// Name returns the intent token handled by this verifier.
func (i *Intent) Name() string {
	return tempo.IntentCharge
}

// Verify validates a Tempo charge Credential against the supplied request.
func (i *Intent) Verify(
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
	if request.MethodDetails.FeePayer && request.Amount != "0" && payload.Type != tempo.CredentialTypeTransaction {
		return nil, mpp.ErrInvalidPayload("fee payer challenges require a transaction credential")
	}
	if !request.Allows(payload.Type) {
		return nil, mpp.ErrInvalidPayload(fmt.Sprintf("credential type %q is not allowed for this challenge", payload.Type))
	}

	rpc, err := i.resolveRPC(request)
	if err != nil {
		return nil, err
	}

	switch payload.Type {
	case tempo.CredentialTypeHash:
		return i.verifyHash(ctx, rpc, credential, request, payload.Hash, source)
	case tempo.CredentialTypeProof:
		return i.verifyProof(ctx, rpc, credential, request, payload.Signature, source)
	case tempo.CredentialTypeTransaction:
		return i.verifyTransaction(ctx, rpc, credential, request, payload.Signature, source)
	default:
		return nil, mpp.ErrInvalidPayload(fmt.Sprintf("unsupported credential type %q", payload.Type))
	}
}

func (i *Intent) verifyHash(
	ctx context.Context,
	rpc tempo.RPCClient,
	credential *mpp.Credential,
	request tempo.ChargeRequest,
	hash string,
	source *sourceDID,
) (*mpp.Receipt, error) {
	if request.MethodDetails.Memo != "" {
		return nil, mpp.ErrInvalidPayload("hash credentials are not supported when the primary transfer uses an explicit memo")
	}
	if source == nil {
		return nil, mpp.ErrInvalidPayload("hash credential must include a source")
	}
	receiptMap, err := fetchReceipt(ctx, rpc, hash)
	if err != nil {
		return nil, err
	}
	if !receiptMatches(receiptMap, credential, request, source.address) {
		return nil, mpp.ErrVerificationFailed("transaction receipt does not satisfy the charge request")
	}
	accepted, err := i.store.PutIfAbsent(ctx, tempo.ChargeStoreKey(hash), hash)
	if err != nil {
		return nil, err
	}
	if !accepted {
		return nil, mpp.ErrVerificationFailed("transaction hash already used")
	}
	return mpp.Success(
		hash,
		mpp.WithReceiptMethod(tempo.MethodName),
		mpp.WithExternalID(request.ExternalID),
	), nil
}

func (i *Intent) verifyProof(
	ctx context.Context,
	rpc tempo.RPCClient,
	credential *mpp.Credential,
	request tempo.ChargeRequest,
	signature string,
	source *sourceDID,
) (*mpp.Receipt, error) {
	if request.Amount != "0" {
		return nil, mpp.ErrInvalidPayload("proof credentials are only valid for zero-amount challenges")
	}
	if source == nil {
		return nil, mpp.ErrInvalidPayload("proof credential must include a source")
	}
	chainID, err := resolveChallengeChainID(ctx, rpc, request)
	if err != nil {
		return nil, err
	}
	if source.chainID != chainID {
		return nil, mpp.ErrInvalidPayload("credential source chain id does not match the challenge")
	}
	proofHash, err := tempo.ProofTypedDataHash(chainID, credential.Challenge.ID)
	if err != nil {
		return nil, mpp.ErrVerificationFailed("failed to construct proof payload")
	}
	proofSigner, err := recoverProofSigner(proofHash, signature, common.HexToAddress(source.address))
	if err != nil {
		return nil, mpp.ErrInvalidPayload("proof signature is invalid")
	}
	if !strings.EqualFold(proofSigner.Hex(), source.address) {
		active, err := isActiveAccessKey(ctx, rpc, common.HexToAddress(source.address), proofSigner)
		if err != nil || !active {
			return nil, mpp.ErrInvalidPayload("proof signature does not match source")
		}
	}
	accepted, err := i.store.PutIfAbsent(ctx, tempo.ChargeProofStoreKey(credential.Challenge.ID), credential.Challenge.ID)
	if err != nil {
		return nil, err
	}
	if !accepted {
		return nil, mpp.ErrVerificationFailed("proof credential already used")
	}
	return mpp.Success(
		credential.Challenge.ID,
		mpp.WithReceiptMethod(tempo.MethodName),
		mpp.WithExternalID(request.ExternalID),
	), nil
}

func (i *Intent) verifyTransaction(
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
		// tempo-go exposes the signing primitives already; this package keeps the
		// sponsor policy checks local until a shared helper exists upstream.
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
		if !transactionMatches(tx, request, credential.Challenge.Realm, credential.Challenge.ID) {
			return nil, mpp.ErrVerificationFailed("co-signed transaction does not contain a matching Tempo transfer")
		}
		if err := validateFeePayerTransaction(tx, credential.Challenge.Expires); err != nil {
			return nil, err
		}
		if tx.AwaitingFeePayer {
			return nil, mpp.ErrVerificationFailed("co-signed transaction must clear the awaiting fee payer marker")
		}
		if tx.FeeToken != common.HexToAddress(request.Currency) {
			return nil, mpp.ErrVerificationFailed("co-signed transaction fee token does not match the charge request")
		}
		coSignedSender, _, err := tempotx.VerifyDualSignatures(tx)
		if err != nil {
			return nil, mpp.ErrVerificationFailed("co-signed transaction failed signature verification")
		}
		if coSignedSender != sender {
			return nil, mpp.ErrVerificationFailed("co-signed transaction sender does not match the credential signer")
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
	if !receiptMatches(receiptMap, credential, request, sender.Hex()) {
		return nil, mpp.ErrVerificationFailed("transaction receipt does not satisfy the charge request")
	}
	accepted, err := i.store.PutIfAbsent(ctx, tempo.ChargeStoreKey(txHash), txHash)
	if err != nil {
		return nil, err
	}
	if !accepted {
		return nil, mpp.ErrVerificationFailed("transaction hash already used")
	}
	return mpp.Success(
		txHash,
		mpp.WithReceiptMethod(tempo.MethodName),
		mpp.WithExternalID(request.ExternalID),
	), nil
}

func (i *Intent) resolveRPC(request tempo.ChargeRequest) (tempo.RPCClient, error) {
	if i.rpc != nil {
		return i.rpc, nil
	}
	if i.rpcURL != "" {
		return tempo.NewRPCClient(i.rpcURL), nil
	}
	if request.MethodDetails.ChainID != nil {
		rpcURL, err := tempo.RPCURLForChain(*request.MethodDetails.ChainID)
		if err != nil {
			return nil, fmt.Errorf("tempo server: %w; configure Intent.RPC or Intent.RPCURL explicitly", err)
		}
		return tempo.NewRPCClient(rpcURL), nil
	}
	return tempo.NewRPCClient(tempo.DefaultRPCURLForChain(0)), nil
}

// TODO(tempo-go): extract the Tempo transaction/receipt matching and fee-payer
// verification helpers below once tempo-go exposes a shared verifier surface for
// TIP-20 charge flows.

func transactionMatches(tx *tempotx.Tx, request tempo.ChargeRequest, realm, challengeID string) bool {
	expected := expectedTransfers(request)
	if len(tx.Calls) != len(expected) || len(tx.AccessList) != 0 || tx.KeyAuthorization != nil {
		return false
	}
	actual := make([]decodedTransfer, 0, len(tx.Calls))
	for _, call := range tx.Calls {
		if call.To == nil || !strings.EqualFold(call.To.Hex(), request.Currency) {
			return false
		}
		if call.Value != nil && call.Value.Sign() != 0 {
			return false
		}
		decoded, ok := decodeCallTransfer(call.Data)
		if !ok {
			return false
		}
		actual = append(actual, decoded)
	}
	return matchTransfers(actual, expected, realm, challengeID)
}

// fetchReceipt polls until a Tempo receipt appears because tempo-go does not
// yet expose a shared wait-for-receipt helper.
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

func receiptMatches(receipt map[string]any, credential *mpp.Credential, request tempo.ChargeRequest, sourceAddress string) bool {
	logs, ok := receipt["logs"].([]any)
	if !ok {
		return false
	}
	expected := expectedTransfers(request)
	actual := make([]decodedTransfer, 0, len(logs))
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
		if sourceAddress != "" && !strings.EqualFold(fromAddress, sourceAddress) {
			continue
		}
		decoded, ok := decodeLogTransfer(topics, entry)
		if !ok {
			continue
		}
		decoded.recipient = toAddress
		if isFeeControllerTransfer(fromAddress, decoded.recipient, decoded.amount, expected) {
			continue
		}
		actual = append(actual, decoded)
	}
	actual = canonicalReceiptTransfers(actual)
	return matchTransfers(actual, expected, credential.Challenge.Realm, credential.Challenge.ID)
}

func isFeeControllerTransfer(fromAddress, recipient, amount string, expected []expectedTransfer) bool {
	if !strings.EqualFold(fromAddress, feeControllerAddress.Hex()) && !strings.EqualFold(recipient, feeControllerAddress.Hex()) {
		return false
	}
	for _, transfer := range expected {
		if strings.EqualFold(transfer.recipient, recipient) && transfer.amount == amount {
			return false
		}
	}
	return true
}

// Tempo TIP-20 emits a standard Transfer alongside TransferWithMemo for the same
// logical payment. Collapse those paired logs so receipt matching counts the
// payment once while still rejecting unrelated extra transfers.
func canonicalReceiptTransfers(transfers []decodedTransfer) []decodedTransfer {
	canonical := append([]decodedTransfer(nil), transfers...)
	skipped := make([]bool, len(canonical))
	for index, transfer := range canonical {
		if !transfer.hasMemo {
			continue
		}
		if paired := pairedTransferIndex(canonical, index); paired >= 0 {
			skipped[paired] = true
		}
	}
	result := make([]decodedTransfer, 0, len(canonical))
	for index, transfer := range canonical {
		if skipped[index] {
			continue
		}
		result = append(result, transfer)
	}
	return result
}

func pairedTransferIndex(transfers []decodedTransfer, memoIndex int) int {
	withMemo := transfers[memoIndex]
	for index, transfer := range transfers {
		if index == memoIndex || transfer.hasMemo {
			continue
		}
		if transfer.amount == withMemo.amount && strings.EqualFold(transfer.recipient, withMemo.recipient) {
			return index
		}
	}
	return -1
}

type expectedTransfer struct {
	amount             string
	allowAnyMemo       bool
	memo               string
	recipient          string
	requireAttribution bool
}

type decodedTransfer struct {
	amount    string
	hasMemo   bool
	memo      string
	recipient string
}

func expectedTransfers(request tempo.ChargeRequest) []expectedTransfer {
	transfers := make([]expectedTransfer, 0, len(request.MethodDetails.Splits)+1)
	primaryAmount, _ := new(big.Int).SetString(request.Amount, 10)
	if request.MethodDetails.Memo != "" {
		// memo assigned after split subtraction below
	} else {
		// attribution assigned after split subtraction below
	}
	for _, split := range request.MethodDetails.Splits {
		splitAmount, ok := new(big.Int).SetString(split.Amount, 10)
		if ok {
			primaryAmount.Sub(primaryAmount, splitAmount)
		}
		splitTransfer := expectedTransfer{
			amount:       split.Amount,
			recipient:    split.Recipient,
			allowAnyMemo: split.Memo == "",
			memo:         split.Memo,
		}
		transfers = append(transfers, splitTransfer)
	}
	primary := expectedTransfer{amount: primaryAmount.String(), recipient: request.Recipient}
	if request.MethodDetails.Memo != "" {
		primary.memo = request.MethodDetails.Memo
	} else {
		primary.requireAttribution = true
	}
	return append([]expectedTransfer{primary}, transfers...)
}

func decodeCallTransfer(data []byte) (decodedTransfer, bool) {
	dataHex := strings.TrimPrefix(strings.ToLower(common.Bytes2Hex(data)), "0x")
	if len(dataHex) < 8+64+64 {
		return decodedTransfer{}, false
	}
	decoded := decodedTransfer{
		recipient: common.HexToAddress("0x" + dataHex[8+24:8+64]).Hex(),
		amount:    new(big.Int).SetBytes(common.FromHex("0x" + dataHex[72:136])).String(),
	}
	switch dataHex[:8] {
	case tempo.TransferSelector:
		return decoded, true
	case tempo.TransferWithMemoSelector:
		if len(dataHex) < 8+64+64+64 {
			return decodedTransfer{}, false
		}
		decoded.hasMemo = true
		decoded.memo = "0x" + dataHex[136:200]
		return decoded, true
	default:
		return decodedTransfer{}, false
	}
}

func decodeLogTransfer(topics []any, entry map[string]any) (decodedTransfer, bool) {
	amount, err := tempo.ParseHexBigInt(asString(entry["data"]))
	if err != nil {
		return decodedTransfer{}, false
	}
	decoded := decodedTransfer{amount: amount.String()}
	switch topic0 := asString(topics[0]); {
	case strings.EqualFold(topic0, tempo.TransferTopic.Hex()):
		return decoded, true
	case strings.EqualFold(topic0, tempo.TransferWithMemoTopic.Hex()) && len(topics) >= 4:
		decoded.hasMemo = true
		decoded.memo = asString(topics[3])
		return decoded, true
	default:
		return decodedTransfer{}, false
	}
}

func matchTransfers(actual []decodedTransfer, expected []expectedTransfer, realm, challengeID string) bool {
	if len(actual) != len(expected) {
		return false
	}
	used := make([]bool, len(actual))
	ordered := append([]expectedTransfer(nil), expected...)
	sortExpectedTransfers(ordered)
	for _, want := range ordered {
		matched := false
		for index, got := range actual {
			if used[index] {
				continue
			}
			if !strings.EqualFold(got.recipient, want.recipient) || got.amount != want.amount {
				continue
			}
			if want.memo != "" {
				if got.hasMemo && strings.EqualFold(got.memo, want.memo) {
					used[index] = true
					matched = true
					break
				}
				continue
			}
			if want.requireAttribution {
				if got.hasMemo && tempo.VerifyAttributionServer(got.memo, realm) && tempo.VerifyAttributionChallenge(got.memo, challengeID) {
					used[index] = true
					matched = true
					break
				}
				continue
			}
			if want.allowAnyMemo || !got.hasMemo {
				used[index] = true
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	return true
}

func sortExpectedTransfers(transfers []expectedTransfer) {
	for i := 0; i < len(transfers)-1; i++ {
		for j := i + 1; j < len(transfers); j++ {
			if transferPriority(transfers[j]) < transferPriority(transfers[i]) {
				transfers[i], transfers[j] = transfers[j], transfers[i]
			}
		}
	}
}

func transferPriority(transfer expectedTransfer) int {
	if transfer.memo != "" || transfer.requireAttribution {
		return 0
	}
	if transfer.allowAnyMemo {
		return 1
	}
	return 2
}

func resolveChallengeChainID(ctx context.Context, rpc tempo.RPCClient, request tempo.ChargeRequest) (int64, error) {
	if request.MethodDetails.ChainID != nil {
		return *request.MethodDetails.ChainID, nil
	}
	chainID, err := rpc.GetChainID(ctx)
	if err != nil {
		return 0, mpp.ErrVerificationFailed("failed to resolve proof chain id")
	}
	return int64(chainID), nil
}

func recoverProofSigner(proofHash common.Hash, encoded string, source common.Address) (common.Address, error) {
	raw, err := hexutil.Decode(encoded)
	if err != nil {
		return common.Address{}, err
	}
	if keychain.IsKeychainSignature(raw) {
		_, rootAccount, innerSignature, err := keychain.ParseKeychainSignature(raw)
		if err != nil {
			return common.Address{}, err
		}
		if rootAccount != source {
			return common.Address{}, fmt.Errorf("keychain proof root mismatch")
		}
		payload := make([]byte, 0, 1+len(proofHash.Bytes())+len(rootAccount.Bytes()))
		payload = append(payload, keychain.KeychainSignatureType)
		payload = append(payload, proofHash.Bytes()...)
		payload = append(payload, rootAccount.Bytes()...)
		return temposigner.RecoverAddress(crypto.Keccak256Hash(payload), innerSignature)
	}
	if len(raw) != 65 {
		return common.Address{}, fmt.Errorf("unexpected proof signature length %d", len(raw))
	}
	v := raw[64]
	if v >= 27 {
		v -= 27
	}
	if v > 1 {
		return common.Address{}, fmt.Errorf("invalid recovery id")
	}
	return temposigner.RecoverAddress(proofHash, temposigner.NewSignature(
		new(big.Int).SetBytes(raw[:32]),
		new(big.Int).SetBytes(raw[32:64]),
		v,
	))
}

func isActiveAccessKey(ctx context.Context, rpc tempo.RPCClient, account, accessKey common.Address) (bool, error) {
	callData := keychain.GetKeySelector + addressToWord(account) + addressToWord(accessKey)
	response, err := rpc.SendRequest(ctx, "eth_call", map[string]any{
		"to":   keychain.GetKeychainAddress().Hex(),
		"data": callData,
	}, "latest")
	if err != nil {
		return false, err
	}
	if err := response.CheckError(); err != nil {
		return false, err
	}
	result, ok := response.Result.(string)
	if !ok {
		return false, fmt.Errorf("unexpected getKey result %T", response.Result)
	}
	resultBytes, err := hex.DecodeString(strings.TrimPrefix(result, "0x"))
	if err != nil {
		return false, err
	}
	if len(resultBytes) < 160 {
		return false, fmt.Errorf("getKey result too short")
	}
	keyID := common.BytesToAddress(resultBytes[44:64])
	if keyID != accessKey {
		return false, nil
	}
	expiry := new(big.Int).SetBytes(resultBytes[64:96])
	if expiry.Sign() > 0 && expiry.Cmp(big.NewInt(time.Now().Unix())) <= 0 {
		return false, nil
	}
	for _, value := range resultBytes[128:160] {
		if value != 0 {
			return false, nil
		}
	}
	return true, nil
}

func addressToWord(address common.Address) string {
	return strings.Repeat("0", 24) + strings.TrimPrefix(strings.ToLower(address.Hex()), "0x")
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
