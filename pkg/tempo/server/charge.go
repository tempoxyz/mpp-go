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
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/tempoxyz/mpp-go/pkg/mpp"
	mppserver "github.com/tempoxyz/mpp-go/pkg/server"
	"github.com/tempoxyz/mpp-go/pkg/tempo"
	"github.com/tempoxyz/tempo-go/pkg/keychain"
	temposigner "github.com/tempoxyz/tempo-go/pkg/signer"
	tempotx "github.com/tempoxyz/tempo-go/pkg/transaction"
)

const receiptRetryDelay = 500 * time.Millisecond
const receiptRetryAttempts = 20

// Sponsor policy caps for fee-payer transactions. Tempo fee tokens are TIP-20
// assets, so the fee budget uses the chain's shared 18-decimal fee accounting.
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

// FeePayerPolicy configures sponsored transaction limits for one TIP-20 fee token.
type FeePayerPolicy struct {
	MaxFeePerGas         *big.Int
	MaxPriorityFeePerGas *big.Int
	MaxTotalFee          *big.Int
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
	// FeePayerPolicies allowlists the fee tokens this verifier will sponsor.
	FeePayerPolicies map[string]FeePayerPolicy
	// Store persists replay-protection keys for hash and proof credentials.
	Store tempo.Store
}

// Intent verifies Tempo charge Credentials and returns Receipts.
type Intent struct {
	rpc            tempo.RPCClient
	rpcURL         string
	feePayerSigner *temposigner.Signer
	feePayerPolicy map[string]FeePayerPolicy
	store          tempo.Store
}

var _ mppserver.BroadcastingIntent = (*Intent)(nil)

type validatedChargeCredential struct {
	payload tempo.ChargeCredentialPayload
	request tempo.ChargeRequest
	rpc     tempo.RPCClient
	sender  common.Address
	source  *sourceDID
	tx      *tempotx.Tx
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
	feePayerPolicy, err := normalizeFeePayerPolicies(config.FeePayerPolicies)
	if err != nil {
		return nil, err
	}
	return &Intent{
		rpc:            config.RPC,
		rpcURL:         config.RPCURL,
		feePayerSigner: feePayerSigner,
		feePayerPolicy: feePayerPolicy,
		store:          store,
	}, nil
}

// Name returns the intent token handled by this verifier.
func (i *Intent) Name() string {
	return tempo.IntentCharge
}

// Validate checks a Tempo charge Credential without consuming replay state,
// signing a sponsored transaction, or broadcasting.
func (i *Intent) Validate(
	ctx context.Context,
	credential *mpp.Credential,
	requestMap map[string]any,
) (*mppserver.Validation, error) {
	validated, err := i.validateCredential(ctx, credential, requestMap)
	if err != nil {
		return nil, err
	}
	mode := string(tempo.ChargeModePull)
	if validated.payload.Type == tempo.CredentialTypeHash {
		mode = string(tempo.ChargeModePush)
	} else if validated.payload.Type == tempo.CredentialTypeProof {
		mode = string(tempo.CredentialTypeProof)
	}
	details := map[string]any{"mode": mode}
	if validated.sender != (common.Address{}) {
		details["sender"] = validated.sender.Hex()
	}
	if validated.payload.Type == tempo.CredentialTypeHash || validated.payload.Type == tempo.CredentialTypeTransaction {
		details["transfers"] = validationTransferDetails(validated.request)
	}
	if validated.payload.Type == tempo.CredentialTypeTransaction {
		details["serializedTransaction"] = validated.payload.Signature
	}
	return &mppserver.Validation{
		Challenge:  credential.Challenge,
		Credential: credential,
		Details:    details,
		Intent:     tempo.IntentCharge,
		Method:     tempo.MethodName,
		Request:    requestMap,
		Source:     credential.Source,
	}, nil
}

func validationTransferDetails(request tempo.ChargeRequest) []map[string]any {
	transfers := expectedTransfers(request)
	details := make([]map[string]any, 0, len(transfers))
	for _, transfer := range transfers {
		detail := map[string]any{
			"amount":    transfer.amount,
			"recipient": transfer.recipient,
		}
		if transfer.memo != "" {
			detail["memo"] = transfer.memo
		} else if transfer.requireAttribution {
			detail["requireAttribution"] = true
		} else if transfer.allowAnyMemo {
			detail["allowAnyMemo"] = true
		}
		details = append(details, detail)
	}
	return details
}

// Broadcast revalidates and settles a Tempo charge Credential.
func (i *Intent) Broadcast(
	ctx context.Context,
	credential *mpp.Credential,
	requestMap map[string]any,
) (*mpp.Receipt, error) {
	validated, err := i.validateCredential(ctx, credential, requestMap)
	if err != nil {
		return nil, err
	}
	if err := validateChallengeExpiry(credential); err != nil {
		return nil, err
	}
	return i.broadcastCredential(ctx, credential, validated)
}

// Verify is the backwards-compatible combined validation and broadcast path.
func (i *Intent) Verify(
	ctx context.Context,
	credential *mpp.Credential,
	requestMap map[string]any,
) (*mpp.Receipt, error) {
	return i.Broadcast(ctx, credential, requestMap)
}

func (i *Intent) validateCredential(
	ctx context.Context,
	credential *mpp.Credential,
	requestMap map[string]any,
) (*validatedChargeCredential, error) {
	if credential == nil {
		return nil, mpp.ErrMalformedCredential("credential is required")
	}
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

	validated := &validatedChargeCredential{
		payload: payload,
		request: request,
		rpc:     rpc,
		source:  source,
	}
	switch payload.Type {
	case tempo.CredentialTypeHash:
		if err := i.validateHash(ctx, credential, validated); err != nil {
			return nil, err
		}
	case tempo.CredentialTypeProof:
		if err := i.validateProof(ctx, credential, validated); err != nil {
			return nil, err
		}
	case tempo.CredentialTypeTransaction:
		if err := i.validateTransaction(ctx, credential, validated); err != nil {
			return nil, err
		}
	default:
		return nil, mpp.ErrInvalidPayload(fmt.Sprintf("unsupported credential type %q", payload.Type))
	}
	return validated, nil
}

func (i *Intent) validateHash(
	ctx context.Context,
	credential *mpp.Credential,
	validated *validatedChargeCredential,
) error {
	request := validated.request
	source := validated.source
	if request.MethodDetails.Memo != "" {
		return mpp.ErrInvalidPayload("hash credentials are not supported when the primary transfer uses an explicit memo")
	}
	if source == nil {
		return mpp.ErrInvalidPayload("hash credential must include a source")
	}
	receiptMap, err := fetchReceipt(ctx, validated.rpc, validated.payload.Hash)
	if err != nil {
		return err
	}
	if !receiptMatches(receiptMap, credential, request, source.address) {
		return mpp.ErrVerificationFailed("transaction receipt does not satisfy the charge request")
	}
	validated.sender = common.HexToAddress(source.address)
	return nil
}

func (i *Intent) validateProof(
	ctx context.Context,
	credential *mpp.Credential,
	validated *validatedChargeCredential,
) error {
	request := validated.request
	source := validated.source
	if request.Amount != "0" {
		return mpp.ErrInvalidPayload("proof credentials are only valid for zero-amount challenges")
	}
	if source == nil {
		return mpp.ErrInvalidPayload("proof credential must include a source")
	}
	chainID, err := resolveChallengeChainID(ctx, validated.rpc, request)
	if err != nil {
		return err
	}
	if source.chainID != chainID {
		return mpp.ErrInvalidPayload("credential source chain id does not match the challenge")
	}
	// Bind the digest to the claimed payer so a proof cannot be replayed
	// against a different account (e.g. via a shared access key).
	proofHash, err := tempo.ProofTypedDataHash(chainID, common.HexToAddress(source.address), credential.Challenge.ID, credential.Challenge.Realm)
	if err != nil {
		return mpp.ErrVerificationFailed("failed to construct proof payload")
	}
	proofSigner, err := recoverProofSigner(proofHash, validated.payload.Signature, common.HexToAddress(source.address))
	if err != nil {
		return mpp.ErrInvalidPayload("proof signature is invalid")
	}
	if !strings.EqualFold(proofSigner.Hex(), source.address) {
		active, err := isActiveAccessKey(ctx, validated.rpc, common.HexToAddress(source.address), proofSigner)
		if err != nil || !active {
			return mpp.ErrInvalidPayload("proof signature does not match source")
		}
	}
	validated.sender = common.HexToAddress(source.address)
	return nil
}

func (i *Intent) validateTransaction(
	ctx context.Context,
	credential *mpp.Credential,
	validated *validatedChargeCredential,
) error {
	request := validated.request
	tx, feePayerForm, err := deserializeTransactionCredential(validated.payload.Signature)
	if err != nil {
		return mpp.ErrInvalidPayload("failed to deserialize transaction payload")
	}
	if !transactionMatches(tx, request, credential.Challenge.Realm, credential.Challenge.ID) {
		return mpp.ErrInvalidPayload("transaction does not contain a matching Tempo transfer")
	}

	sender, err := verifyTransactionSender(tx)
	if err != nil {
		return mpp.ErrInvalidPayload("transaction signature is invalid")
	}
	if validated.source != nil && !strings.EqualFold(validated.source.address, sender.Hex()) {
		return mpp.ErrInvalidPayload("credential source does not match transaction signer")
	}
	tx.From = sender

	if request.MethodDetails.FeePayer {
		policy, err := i.feePayerPolicyFor(request.Currency)
		if err != nil {
			return err
		}
		// tempo-go exposes the signing primitives already; this package keeps the
		// sponsor policy checks local until a shared helper exists upstream.
		if err := validateFeePayerTransaction(tx, credential.Challenge.Expires, policy); err != nil {
			return err
		}
		if !tx.AwaitingFeePayer {
			return mpp.ErrInvalidPayload("fee payer transaction must be marked as awaiting a fee payer")
		}
		if tx.ValidBefore == 0 || time.Now().Unix() >= int64(tx.ValidBefore) {
			return mpp.ErrVerificationFailed("fee payer transaction has expired")
		}
		if tx.NonceKey == nil || tx.NonceKey.Cmp(tempo.ExpiringNonceKey) != 0 {
			return mpp.ErrInvalidPayload("fee payer transaction must use the expiring nonce key")
		}
		requestFeeToken := common.HexToAddress(request.Currency)
		if !feePayerForm && tx.FeeToken != (common.Address{}) {
			return mpp.ErrInvalidPayload("fee payer transaction must omit fee token before co-signing")
		}
		if feePayerForm && tx.FeeToken != (common.Address{}) && tx.FeeToken != requestFeeToken {
			return mpp.ErrInvalidPayload("fee payer transaction fee token does not match the charge request")
		}
	} else if err := simulateTransactionExecution(ctx, validated.rpc, tx); err != nil {
		return err
	}
	validated.sender = sender
	validated.tx = tx
	return nil
}

func (i *Intent) broadcastCredential(
	ctx context.Context,
	credential *mpp.Credential,
	validated *validatedChargeCredential,
) (*mpp.Receipt, error) {
	switch validated.payload.Type {
	case tempo.CredentialTypeHash:
		hash := validated.payload.Hash
		accepted, err := i.store.PutIfAbsent(ctx, tempo.ChargeStoreKey(hash), hash)
		if err != nil {
			return nil, err
		}
		if !accepted {
			return nil, mpp.ErrVerificationFailed("transaction hash already used")
		}
		return mpp.Success(hash, mpp.WithReceiptMethod(tempo.MethodName), mpp.WithExternalID(validated.request.ExternalID)), nil
	case tempo.CredentialTypeProof:
		challengeID := credential.Challenge.ID
		accepted, err := i.store.PutIfAbsent(ctx, tempo.ChargeProofStoreKey(challengeID), challengeID)
		if err != nil {
			return nil, err
		}
		if !accepted {
			return nil, mpp.ErrVerificationFailed("proof credential already used")
		}
		return mpp.Success(challengeID, mpp.WithReceiptMethod(tempo.MethodName), mpp.WithExternalID(validated.request.ExternalID)), nil
	case tempo.CredentialTypeTransaction:
		return i.broadcastTransaction(ctx, credential, validated)
	default:
		return nil, mpp.ErrInvalidPayload(fmt.Sprintf("unsupported credential type %q", validated.payload.Type))
	}
}

func (i *Intent) broadcastTransaction(
	ctx context.Context,
	credential *mpp.Credential,
	validated *validatedChargeCredential,
) (receipt *mpp.Receipt, err error) {
	request := validated.request
	rpc := validated.rpc
	sender := validated.sender
	tx := validated.tx
	sponsoredClaimKey := ""
	releaseSponsoredClaim := false
	defer func() {
		if releaseSponsoredClaim {
			if releaseErr := i.store.Delete(context.WithoutCancel(ctx), sponsoredClaimKey); releaseErr != nil {
				receipt = nil
				err = withReplayReleaseError(err, releaseErr)
			}
		}
	}()

	if request.MethodDetails.FeePayer {
		if err := simulateSponsoredSenderExecution(ctx, rpc, tx); err != nil {
			return nil, err
		}
		sponsoredClaimKey = tempo.ChargeSponsoredChallengeStoreKey(credential.Challenge.ID)
		accepted, err := i.store.PutIfAbsent(
			ctx,
			sponsoredClaimKey,
			credential.Challenge.ID,
		)
		if err != nil {
			return nil, err
		}
		if !accepted {
			return nil, mpp.ErrVerificationFailed("fee payer challenge already used")
		}
		releaseSponsoredClaim = true
		requestFeeToken := common.HexToAddress(request.Currency)
		if i.feePayerSigner != nil {
			tx.FeeToken = requestFeeToken
			tx.AwaitingFeePayer = false
			if err := tempotx.AddFeePayerSignature(tx, i.feePayerSigner); err != nil {
				return nil, mpp.ErrVerificationFailed("failed to co-sign fee payer transaction")
			}
		} else if request.MethodDetails.FeePayerURL != "" {
			coSignedRaw, err := signWithRemoteFeePayer(ctx, request.MethodDetails.FeePayerURL, validated.payload.Signature)
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
		policy, err := i.feePayerPolicyFor(request.Currency)
		if err != nil {
			return nil, err
		}
		if err := validateFeePayerTransaction(tx, credential.Challenge.Expires, policy); err != nil {
			return nil, err
		}
		if tx.AwaitingFeePayer {
			return nil, mpp.ErrVerificationFailed("co-signed transaction must clear the awaiting fee payer marker")
		}
		if tx.FeeToken != requestFeeToken {
			return nil, mpp.ErrVerificationFailed("co-signed transaction fee token does not match the charge request")
		}
		coSignedSender, err := verifyTransactionSender(tx)
		if err != nil {
			return nil, mpp.ErrVerificationFailed("co-signed transaction failed signature verification")
		}
		if coSignedSender != sender {
			return nil, mpp.ErrVerificationFailed("co-signed transaction sender does not match the credential signer")
		}
		feePayerAddress, err := tempotx.VerifyFeePayerSignature(tx, coSignedSender)
		if err != nil {
			return nil, mpp.ErrVerificationFailed("co-signed transaction failed signature verification")
		}
		tx.From = coSignedSender
		if err := simulateSponsoredTransactionExecution(ctx, rpc, tx, feePayerAddress); err != nil {
			return nil, err
		}
	} else {
		// Match mppx's final pre-broadcast simulation to narrow the window in
		// which account or contract state can change after validation.
		if err := simulateTransactionExecution(ctx, rpc, tx); err != nil {
			return nil, err
		}
	}
	if err := validateChallengeExpiry(credential); err != nil {
		return nil, err
	}

	serialized, err := tempotx.Serialize(tx, nil)
	if err != nil {
		return nil, mpp.ErrVerificationFailed("failed to serialize transaction")
	}

	reservedHash := ""
	txHash := ""
	shouldBroadcast := true
	if !request.MethodDetails.FeePayer {
		computedHash, err := tempotx.ComputeHash(serialized)
		if err != nil {
			return nil, mpp.ErrVerificationFailed("failed to compute transaction hash")
		}
		reservedHash = computedHash.Hex()
		txHash = reservedHash
		accepted, err := i.store.PutIfAbsent(ctx, tempo.ChargeStoreKey(reservedHash), reservedHash)
		if err != nil {
			return nil, err
		}
		if !accepted {
			shouldBroadcast = false
		}
	}

	if shouldBroadcast {
		if request.MethodDetails.FeePayer {
			releaseSponsoredClaim = false
		}
		var err error
		txHash, err = rpc.SendRawTransaction(ctx, serialized)
		if err != nil {
			if reservedHash != "" {
				_ = i.store.Delete(ctx, tempo.ChargeStoreKey(reservedHash))
			}
			return nil, mpp.ErrVerificationFailed("transaction submission failed")
		}
	}

	receiptMap, err := fetchReceipt(ctx, rpc, txHash)
	if err != nil {
		return nil, err
	}
	if !receiptMatches(receiptMap, credential, request, sender.Hex()) {
		return nil, mpp.ErrVerificationFailed("transaction receipt does not satisfy the charge request")
	}
	if request.MethodDetails.FeePayer {
		accepted, err := i.store.PutIfAbsent(ctx, tempo.ChargeStoreKey(txHash), txHash)
		if err != nil {
			return nil, err
		}
		if !accepted {
			return nil, mpp.ErrVerificationFailed("transaction hash already used")
		}
	}
	return mpp.Success(
		txHash,
		mpp.WithReceiptMethod(tempo.MethodName),
		mpp.WithExternalID(request.ExternalID),
	), nil
}

func validateChallengeExpiry(credential *mpp.Credential) error {
	if credential == nil {
		return mpp.ErrMalformedCredential("credential is required")
	}
	expiresValue := credential.Challenge.Expires
	if expiresValue == "" {
		return mpp.ErrInvalidChallenge(credential.Challenge.ID, "missing required expires")
	}
	expires, err := parseExpires(expiresValue)
	if err != nil {
		return mpp.ErrInvalidChallenge(credential.Challenge.ID, "invalid expires format")
	}
	if !time.Now().UTC().Before(expires) {
		return mpp.ErrPaymentExpired(expiresValue)
	}
	return nil
}

func withReplayReleaseError(settlementErr, releaseErr error) error {
	detail := fmt.Sprintf("failed to release sponsored replay claim: %v", releaseErr)
	if settlementErr == nil {
		return mpp.ErrVerificationFailed(detail)
	}
	if paymentErr, ok := settlementErr.(*mpp.PaymentError); ok {
		combined := *paymentErr
		combined.Detail += "; " + detail
		return &combined
	}
	return fmt.Errorf("%w; %s", settlementErr, detail)
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

// deserializeTransactionCredential accepts both the broadcast transaction
// envelope (0x76) and the fee-payer signing form (0x78). tempo-go v0.5 only
// decodes 0x76, so the 0x78 body is validated and converted to the equivalent
// awaiting-fee-payer shape before delegating the remaining decoding.
func deserializeTransactionCredential(serialized string) (*tempotx.Tx, bool, error) {
	hexValue := serialized
	if strings.HasPrefix(hexValue, "0x") || strings.HasPrefix(hexValue, "0X") {
		hexValue = hexValue[2:]
	}
	encoded, err := hex.DecodeString(hexValue)
	if err != nil {
		return nil, false, fmt.Errorf("decode transaction: %w", err)
	}
	if len(encoded) < 2 {
		return nil, false, fmt.Errorf("transaction is too short")
	}

	switch encoded[0] {
	case 0x76:
		tx, err := tempotx.Deserialize("0x" + hex.EncodeToString(encoded))
		return tx, false, err
	case 0x78:
		var fields []rlp.RawValue
		if err := rlp.DecodeBytes(encoded[1:], &fields); err != nil {
			return nil, false, fmt.Errorf("decode fee-payer transaction body: %w", err)
		}
		if len(fields) != 13 && len(fields) != 14 && len(fields) != 15 {
			return nil, false, fmt.Errorf("invalid fee-payer transaction field count %d", len(fields))
		}

		var senderBytes []byte
		if err := rlp.DecodeBytes(fields[11], &senderBytes); err != nil {
			return nil, false, fmt.Errorf("decode fee-payer transaction sender: %w", err)
		}
		if len(senderBytes) != common.AddressLength {
			return nil, false, fmt.Errorf("invalid fee-payer transaction sender length %d", len(senderBytes))
		}
		sender := common.BytesToAddress(senderBytes)
		if sender == (common.Address{}) {
			return nil, false, fmt.Errorf("fee-payer transaction sender is required")
		}

		awaitingFeePayer, err := rlp.EncodeToBytes([]byte{0})
		if err != nil {
			return nil, false, fmt.Errorf("encode fee-payer marker: %w", err)
		}
		fields[11] = awaitingFeePayer
		body, err := rlp.EncodeToBytes(fields)
		if err != nil {
			return nil, false, fmt.Errorf("encode transaction body: %w", err)
		}

		tx, err := tempotx.Deserialize("0x76" + hex.EncodeToString(body))
		if err != nil {
			return nil, false, err
		}
		tx.AwaitingFeePayer = true
		tx.From = sender
		return tx, true, nil
	default:
		return nil, false, fmt.Errorf("unsupported transaction prefix 0x%02x", encoded[0])
	}
}

// verifyTransactionSender verifies a transaction's sender signature and
// returns the authorizing account. Keychain signatures are made by an access
// key but authorize the root account embedded in the envelope.
func verifyTransactionSender(tx *tempotx.Tx) (common.Address, error) {
	if tx.Signature == nil {
		return common.Address{}, fmt.Errorf("transaction has no sender signature")
	}

	if tx.Signature.Type != "keychain" {
		sender, err := tempotx.VerifySignature(tx)
		if err != nil {
			return common.Address{}, err
		}
		if tx.From != (common.Address{}) && tx.From != sender {
			return common.Address{}, fmt.Errorf("transaction sender does not match signature")
		}
		return sender, nil
	}

	_, rootAccount, innerSignature, err := keychain.ParseKeychainSignature(tx.Signature.Raw)
	if err != nil {
		return common.Address{}, err
	}
	switch innerSignature.YParity {
	case 0, 1:
	case 27, 28:
		innerSignature.YParity -= 27
	default:
		return common.Address{}, fmt.Errorf("invalid keychain signature y parity %d", innerSignature.YParity)
	}

	txForVerify := *tx
	signatureForVerify := *tx.Signature
	signatureForVerify.Raw = keychain.BuildKeychainSignature(innerSignature, rootAccount)
	txForVerify.Signature = &signatureForVerify
	_, verifiedRoot, err := keychain.VerifyAccessKeySignature(&txForVerify)
	if err != nil {
		return common.Address{}, err
	}
	if verifiedRoot != rootAccount {
		return common.Address{}, fmt.Errorf("keychain signature root account mismatch")
	}
	if tx.From != (common.Address{}) && tx.From != rootAccount {
		return common.Address{}, fmt.Errorf("transaction sender does not match keychain root account")
	}
	return rootAccount, nil
}

// TODO(tempo-go): extract the Tempo transaction/receipt matching and fee-payer
// verification helpers below once tempo-go exposes a shared verifier surface for
// TIP-20 charge flows.

func transactionMatches(tx *tempotx.Tx, request tempo.ChargeRequest, realm, challengeID string) bool {
	expected := expectedTransfers(request)
	if len(tx.Calls) != len(expected) || len(tx.AccessList) != 0 {
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
	actual, ok = canonicalReceiptTransfers(actual)
	if !ok {
		return false
	}
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
func canonicalReceiptTransfers(transfers []decodedTransfer) ([]decodedTransfer, bool) {
	canonical := append([]decodedTransfer(nil), transfers...)
	skipped := make([]bool, len(canonical))
	for index, transfer := range canonical {
		if !transfer.hasMemo {
			continue
		}
		if paired := pairedTransferIndex(canonical, index, skipped); paired >= 0 {
			skipped[paired] = true
		} else {
			return nil, false
		}
	}
	result := make([]decodedTransfer, 0, len(canonical))
	for index, transfer := range canonical {
		if skipped[index] {
			continue
		}
		result = append(result, transfer)
	}
	return result, true
}

func pairedTransferIndex(transfers []decodedTransfer, memoIndex int, skipped []bool) int {
	withMemo := transfers[memoIndex]
	for index, transfer := range transfers {
		if index == memoIndex || transfer.hasMemo || skipped[index] {
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
	if len(dataHex) < 8 {
		return decodedTransfer{}, false
	}
	selector := dataHex[:8]
	switch selector {
	case tempo.TransferSelector:
		if len(dataHex) != tempo.TransferCalldataLength*2 {
			return decodedTransfer{}, false
		}
	case tempo.TransferWithMemoSelector:
		if len(dataHex) != tempo.TransferWithMemoCalldataLength*2 {
			return decodedTransfer{}, false
		}
	default:
		return decodedTransfer{}, false
	}
	decoded := decodedTransfer{
		recipient: common.HexToAddress("0x" + dataHex[8+24:8+64]).Hex(),
		amount:    new(big.Int).SetBytes(common.FromHex("0x" + dataHex[72:136])).String(),
	}
	switch selector {
	case tempo.TransferSelector:
		return decoded, true
	case tempo.TransferWithMemoSelector:
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
		// Normalize the inner recovery id to 0/1, as RecoverAddress requires
		// (the encoded byte may be a legacy 27/28 v value).
		if innerSignature.YParity >= 27 {
			innerSignature.YParity -= 27
		}
		if innerSignature.YParity > 1 {
			return common.Address{}, fmt.Errorf("invalid recovery id")
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

func simulateSponsoredSenderExecution(ctx context.Context, rpc tempo.RPCClient, tx *tempotx.Tx) error {
	return simulateCall(ctx, rpc, map[string]any{
		"calls": callsCallObject(tx.Calls),
		"from":  tx.From.Hex(),
	})
}

func simulateSponsoredTransactionExecution(
	ctx context.Context,
	rpc tempo.RPCClient,
	tx *tempotx.Tx,
	feePayer common.Address,
) error {
	callObject := transactionCallObject(tx)
	callObject["feePayer"] = feePayer.Hex()
	return simulateCall(ctx, rpc, callObject)
}

func simulateTransactionExecution(ctx context.Context, rpc tempo.RPCClient, tx *tempotx.Tx) error {
	return simulateCall(ctx, rpc, transactionCallObject(tx))
}

func simulateCall(ctx context.Context, rpc tempo.RPCClient, callObject map[string]any) error {
	response, err := rpc.SendRequest(ctx, "eth_call", callObject, "latest")
	if err != nil {
		return mpp.ErrVerificationFailed("transaction preflight failed")
	}
	if err := response.CheckError(); err != nil {
		return mpp.ErrVerificationFailed("transaction preflight failed")
	}
	return nil
}

func transactionCallObject(tx *tempotx.Tx) map[string]any {
	callObject := map[string]any{
		"accessList":           accessListCallObject(tx.AccessList),
		"calls":                callsCallObject(tx.Calls),
		"chainId":              hexBig(tx.ChainID),
		"from":                 tx.From.Hex(),
		"gas":                  hexutil.EncodeUint64(tx.Gas),
		"maxFeePerGas":         hexBig(tx.MaxFeePerGas),
		"maxPriorityFeePerGas": hexBig(tx.MaxPriorityFeePerGas),
		"nonce":                hexutil.EncodeUint64(tx.Nonce),
	}
	if tx.NonceKey != nil {
		callObject["nonceKey"] = hexBig(tx.NonceKey)
	}
	if tx.ValidBefore != 0 {
		callObject["validBefore"] = hexutil.EncodeUint64(tx.ValidBefore)
	}
	if tx.ValidAfter != 0 {
		callObject["validAfter"] = hexutil.EncodeUint64(tx.ValidAfter)
	}
	if tx.FeeToken != (common.Address{}) {
		callObject["feeToken"] = tx.FeeToken.Hex()
	}
	return callObject
}

func callsCallObject(calls []tempotx.Call) []map[string]any {
	encoded := make([]map[string]any, 0, len(calls))
	for _, call := range calls {
		entry := map[string]any{
			"data":  hexutil.Encode(call.Data),
			"value": hexBig(call.Value),
		}
		if call.To != nil {
			entry["to"] = call.To.Hex()
		}
		encoded = append(encoded, entry)
	}
	return encoded
}

func accessListCallObject(accessList tempotx.AccessList) []map[string]any {
	encoded := make([]map[string]any, 0, len(accessList))
	for _, tuple := range accessList {
		storageKeys := make([]string, 0, len(tuple.StorageKeys))
		for _, key := range tuple.StorageKeys {
			storageKeys = append(storageKeys, key.Hex())
		}
		encoded = append(encoded, map[string]any{
			"address":     tuple.Address.Hex(),
			"storageKeys": storageKeys,
		})
	}
	return encoded
}

func hexBig(value *big.Int) string {
	if value == nil {
		return hexutil.EncodeBig(big.NewInt(0))
	}
	return hexutil.EncodeBig(value)
}

func normalizeFeePayerPolicies(configured map[string]FeePayerPolicy) (map[string]FeePayerPolicy, error) {
	if len(configured) == 0 {
		configured = defaultFeePayerPolicies()
	}
	policies := make(map[string]FeePayerPolicy, len(configured))
	for currency, policy := range configured {
		if !common.IsHexAddress(currency) {
			return nil, fmt.Errorf("tempo server: invalid fee payer policy currency %q", currency)
		}
		if policy.MaxFeePerGas == nil || policy.MaxFeePerGas.Sign() <= 0 {
			return nil, fmt.Errorf("tempo server: invalid max fee per gas for %s", currency)
		}
		if policy.MaxPriorityFeePerGas == nil || policy.MaxPriorityFeePerGas.Sign() <= 0 {
			return nil, fmt.Errorf("tempo server: invalid max priority fee per gas for %s", currency)
		}
		if policy.MaxPriorityFeePerGas.Cmp(policy.MaxFeePerGas) > 0 {
			return nil, fmt.Errorf("tempo server: max priority fee per gas exceeds max fee per gas for %s", currency)
		}
		if policy.MaxTotalFee == nil || policy.MaxTotalFee.Sign() <= 0 {
			return nil, fmt.Errorf("tempo server: invalid max total fee for %s", currency)
		}
		policies[common.HexToAddress(currency).Hex()] = FeePayerPolicy{
			MaxFeePerGas:         new(big.Int).Set(policy.MaxFeePerGas),
			MaxPriorityFeePerGas: new(big.Int).Set(policy.MaxPriorityFeePerGas),
			MaxTotalFee:          new(big.Int).Set(policy.MaxTotalFee),
		}
	}
	return policies, nil
}

func defaultFeePayerPolicies() map[string]FeePayerPolicy {
	policy := defaultFeePayerPolicy()
	return map[string]FeePayerPolicy{
		"0x20c0000000000000000000000000000000000000": policy,
		tempotx.AlphaUSDAddress.Hex():                policy,
		tempo.MainnetUSDCAddress:                     policy,
	}
}

func defaultFeePayerPolicy() FeePayerPolicy {
	return FeePayerPolicy{
		MaxFeePerGas:         new(big.Int).Set(feePayerMaxFeePerGas),
		MaxPriorityFeePerGas: new(big.Int).Set(feePayerMaxPriorityFeePerGas),
		MaxTotalFee:          new(big.Int).Set(feePayerMaxTotalFee),
	}
}

func (i *Intent) feePayerPolicyFor(currency string) (FeePayerPolicy, error) {
	policy, ok := i.feePayerPolicy[common.HexToAddress(currency).Hex()]
	if !ok {
		return FeePayerPolicy{}, mpp.ErrInvalidPayload("fee payer transaction fee token is not supported")
	}
	return FeePayerPolicy{
		MaxFeePerGas:         new(big.Int).Set(policy.MaxFeePerGas),
		MaxPriorityFeePerGas: new(big.Int).Set(policy.MaxPriorityFeePerGas),
		MaxTotalFee:          new(big.Int).Set(policy.MaxTotalFee),
	}, nil
}

func validateFeePayerTransaction(tx *tempotx.Tx, challengeExpires string, policy FeePayerPolicy) error {
	if tx.Gas == 0 {
		return mpp.ErrInvalidPayload("fee payer transaction must declare gas")
	}
	if tx.Gas > feePayerMaxGas {
		return mpp.ErrInvalidPayload("fee payer transaction gas exceeds sponsor policy")
	}
	if tx.MaxFeePerGas == nil || tx.MaxFeePerGas.Sign() <= 0 {
		return mpp.ErrInvalidPayload("fee payer transaction must declare max fee per gas")
	}
	if tx.MaxFeePerGas.Cmp(policy.MaxFeePerGas) > 0 {
		return mpp.ErrInvalidPayload("fee payer transaction max fee per gas exceeds sponsor policy")
	}
	if tx.MaxPriorityFeePerGas != nil {
		if tx.MaxPriorityFeePerGas.Cmp(tx.MaxFeePerGas) > 0 {
			return mpp.ErrInvalidPayload("fee payer transaction max priority fee exceeds max fee")
		}
		if tx.MaxPriorityFeePerGas.Cmp(policy.MaxPriorityFeePerGas) > 0 {
			return mpp.ErrInvalidPayload("fee payer transaction max priority fee exceeds sponsor policy")
		}
	}
	maxTotalFee := new(big.Int).Mul(new(big.Int).SetUint64(tx.Gas), tx.MaxFeePerGas)
	if maxTotalFee.Cmp(policy.MaxTotalFee) > 0 {
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
