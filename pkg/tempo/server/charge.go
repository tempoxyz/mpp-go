package chargeserver

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"math"
	"math/big"
	"net/url"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/rlp"
	temporpc "github.com/tempoxyz/tempo-go/pkg/client"
	"github.com/tempoxyz/tempo-go/pkg/keychain"
	temposigner "github.com/tempoxyz/tempo-go/pkg/signer"
	tempotx "github.com/tempoxyz/tempo-go/pkg/transaction"

	"github.com/tempoxyz/mpp-go/pkg/mpp"
	mppserver "github.com/tempoxyz/mpp-go/pkg/server"
	"github.com/tempoxyz/mpp-go/pkg/tempo"
	"github.com/tempoxyz/mpp-go/pkg/tempo/internal/rpccache"
)

const receiptRetryDelay = 500 * time.Millisecond
const receiptRetryAttempts = 20

// receiptWaitBudget bounds the post-broadcast receipt wait, which runs
// detached from the request context.
const receiptWaitBudget = receiptRetryAttempts*receiptRetryDelay + 5*time.Second

// replayClaimExpiryGrace extends replay keys past their challenge's expiry to
// absorb clock skew between verifiers and the store.
const replayClaimExpiryGrace = 10 * time.Minute

// Sponsor policy caps for fee-payer transactions. Tempo fee tokens are TIP-20
// assets, so the fee budget uses the chain's shared 18-decimal fee accounting.
var feePayerMaxGas = uint64(2_000_000)

var feePayerMaxFeePerGas = big.NewInt(100_000_000_000)

var feePayerMaxPriorityFeePerGas = big.NewInt(100_000_000_000)

var feePayerMaxTotalFee = big.NewInt(50_000_000_000_000_000)

var feeControllerAddress = common.HexToAddress("0xfeec000000000000000000000000000000000000")

// balanceOfSelector is the TIP-20 balanceOf(address) selector.
const balanceOfSelector = "70a08231"

const feePayerMaxValidityWindow = 15 * time.Minute
const feePayerMinValidityWindow = tempo.FeePayerMinRemainingValidity

func validateFeePayerDeadline(validBefore uint64, now time.Time) error {
	minimum := uint64(now.Add(feePayerMinValidityWindow).Unix())
	if now.Nanosecond() != 0 {
		minimum++
	}
	if validBefore < minimum {
		return mpp.ErrVerificationFailed("fee payer transaction needs at least 15 seconds of remaining validity")
	}
	return nil
}

type sourceDID struct {
	chainID int64
	address string
}

// FeePayerPolicy configures sponsored transaction limits for one TIP-20 fee token.
type FeePayerPolicy struct {
	MaxFeePerGas         *big.Int
	MaxPriorityFeePerGas *big.Int
	MaxTotalFee          *big.Int
	// RejectKeyAuthorization rejects sponsored transactions that install a new
	// access key (carry a keyAuthorization). The default allows them.
	RejectKeyAuthorization bool
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
	// AllowedFeeTokens lists the fee tokens this verifier will sponsor, as in
	// mppx. With a local fee payer, they are the fee tokens it may pay gas in,
	// tried in this order and independent of the charge currency. A hosted fee
	// payer chooses its own fee token, which must be listed when this list is
	// set; when nil, a hosted fee payer's choice is not restricted, as in
	// mppx. When nil, a local fee payer pays in pathUSD or, on mainnet,
	// USDC.e. A non-nil list must name at least one valid address.
	AllowedFeeTokens []string
	// FeePayerPolicy sets the sponsorship limits applied to every allowed fee
	// token, as mppx's feePayerPolicy does. Nil fields keep the defaults
	// (100 gwei max fee and priority fee per gas, 0.05 total fee); a nil
	// MaxPriorityFeePerGas is lowered to a MaxFeePerGas set below its default.
	FeePayerPolicy *FeePayerPolicy
	// FeeToken fixes the fee token a local fee payer pays gas in. When empty,
	// the local fee payer uses the first allowed fee token it holds a balance
	// of, or the first allowed fee token. It must be an allowed fee token (New
	// and NewIntent reject one missing from AllowedFeeTokens) and is ignored
	// for a hosted fee payer.
	FeeToken string
	// FeePayerURL sponsors charges through a hosted fee-payer service at this
	// JSON-RPC URL; it is shorthand for HostedFeePayer{URL: FeePayerURL}, as
	// mppx's feePayer: '<url>'. It cannot be combined with HostedFeePayer or
	// a local FeePayerSigner.
	FeePayerURL string
	// HostedFeePayer sponsors charges through a hosted fee-payer service, as
	// mppx's feePayer: {url, headers}; see HostedFeePayer. It cannot be
	// combined with FeePayerURL or a local FeePayerSigner.
	HostedFeePayer *HostedFeePayer
	// Store is required to verify payments. Use a persistent store shared by all
	// replicas, with atomic PutIfAbsent and no eviction of replay keys.
	// An explicit tempo.NewMemoryStore() is only suitable for single-process development.
	Store tempo.Store
	// ExpireReplayKeys lets replay keys expire ten minutes after their
	// challenge's expiry instead of keeping them forever. Store must implement
	// tempo.ExpiringStore, whose PutIfAbsentUntil then replaces PutIfAbsent
	// for replay claims. It is only safe when every credential reaches this
	// Intent through server.Mpp, which verifies the challenge ID's HMAC,
	// including its expiry, before settlement: Verify and Broadcast called
	// directly trust the credential's echoed expiry, so an expired key would
	// let the same hash, proof or transaction settle again. A hash or
	// transaction whose transfers carry no attribution memo for the challenge
	// (no memo, another memo or a server-set methodDetails.memo) is not bound
	// to it, so its replay keys are still kept forever.
	ExpireReplayKeys bool
}

// Intent verifies Tempo charge Credentials and returns Receipts.
type Intent struct {
	rpc            tempo.RPCClient
	rpcURL         string
	feePayerSigner *temposigner.Signer
	feePayerPolicy map[string]FeePayerPolicy
	// feeTokens lists configured allowed fee tokens in deterministic order;
	// nil selects the per-chain defaults.
	feeTokens []common.Address
	feeToken  common.Address
	// hostedFeePayer sponsors charges when no local feePayerSigner is set.
	hostedFeePayer *HostedFeePayer
	// sponsorPolicy is the sponsorship policy every fee token shares.
	sponsorPolicy FeePayerPolicy
	store         tempo.Store
	// expireReplayKeys claims replay keys with tempo.ExpiringStore; see
	// IntentConfig.ExpireReplayKeys.
	expireReplayKeys bool
	// rpcClients holds the RPC clients built from URLs, so each is built
	// once rather than per credential.
	rpcClients *rpccache.Cache
	// now is the clock for fee payer deadlines; nil means time.Now.
	now func() time.Time
}

var _ mppserver.BroadcastingIntent = (*Intent)(nil)

type validatedChargeCredential struct {
	payload tempo.ChargeCredentialPayload
	request tempo.ChargeRequest
	rpc     tempo.RPCClient
	sender  common.Address
	source  *sourceDID
	tx      *tempotx.Tx
	// feeToken is the token a local fee payer pays gas in.
	feeToken common.Address
	// altFeeTokens are the other allowed tokens a local fee payer without a
	// configured FeeToken may have paid gas in on an earlier attempt.
	altFeeTokens []common.Address
	// challengeBound is set when the credential cannot pay any other
	// challenge: a proof, or a payment carrying this challenge's attribution
	// memo. Only then may its replay keys expire; see replayClaimExpiry.
	challengeBound bool
}

func hasReplayStore(store tempo.Store) bool {
	if store == nil {
		return false
	}
	value := reflect.ValueOf(store)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return !value.IsNil()
	default:
		return true
	}
}

// NewIntent constructs a Tempo charge verifier.
func NewIntent(config IntentConfig) (*Intent, error) {
	if !hasReplayStore(config.Store) {
		return nil, fmt.Errorf("tempo server: a shared persistent replay Store is required; use MemoryStore only for single-process development")
	}
	return newIntent(config)
}

// newIntent also builds relay metadata; relay verification owns its replay store.
func newIntent(config IntentConfig) (*Intent, error) {
	feePayerPrivateKey := config.FeePayerPrivateKey
	feePayerSigner := config.FeePayerSigner
	if feePayerSigner == nil && feePayerPrivateKey != "" {
		resolved, err := temposigner.NewSigner(feePayerPrivateKey)
		if err != nil {
			return nil, err
		}
		feePayerSigner = resolved
	}
	store := config.Store
	if config.ExpireReplayKeys {
		if _, ok := store.(tempo.ExpiringStore); !ok {
			return nil, fmt.Errorf("tempo server: ExpireReplayKeys requires a Store that implements tempo.ExpiringStore")
		}
	}
	feePayerPolicy, feeTokens, err := resolveFeePayerPolicies(config)
	if err != nil {
		return nil, err
	}
	hosted := config.HostedFeePayer
	if config.FeePayerURL != "" {
		if hosted != nil {
			return nil, fmt.Errorf("tempo server: configure either FeePayerURL or HostedFeePayer, not both")
		}
		hosted = &HostedFeePayer{URL: config.FeePayerURL}
	}
	if hosted != nil {
		if hosted.URL == "" {
			return nil, fmt.Errorf("tempo server: HostedFeePayer requires a URL")
		}
		if parsed, err := url.Parse(hosted.URL); err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Host == "" {
			return nil, fmt.Errorf("tempo server: invalid hosted fee payer URL %q", hosted.URL)
		}
		if feePayerSigner != nil {
			return nil, fmt.Errorf("tempo server: configure either a local fee payer (FeePayerSigner or FeePayerPrivateKey) or a hosted fee payer (FeePayerURL or HostedFeePayer), not both")
		}
		copied := *hosted
		copied.Headers = maps.Clone(hosted.Headers)
		hosted = &copied
	}
	var feeToken common.Address
	if config.FeeToken != "" {
		if !common.IsHexAddress(config.FeeToken) {
			return nil, fmt.Errorf("tempo server: invalid fee token %q", config.FeeToken)
		}
		if feePayerSigner == nil {
			return nil, fmt.Errorf("tempo server: FeeToken requires a local fee payer signer")
		}
		feeToken = common.HexToAddress(config.FeeToken)
		// Every sponsored request would fail later with a FeeToken the
		// explicit allowlist does not name, so refuse it now.
		if config.AllowedFeeTokens != nil && !slices.Contains(feeTokens, feeToken) {
			return nil, fmt.Errorf("tempo server: FeeToken %s is not in AllowedFeeTokens", feeToken.Hex())
		}
	}
	return &Intent{
		rpc:              config.RPC,
		rpcURL:           config.RPCURL,
		rpcClients:       new(rpccache.Cache),
		feePayerSigner:   feePayerSigner,
		feePayerPolicy:   feePayerPolicy,
		feeTokens:        feeTokens,
		feeToken:         feeToken,
		hostedFeePayer:   hosted,
		sponsorPolicy:    resolveSponsorPolicy(config),
		store:            store,
		expireReplayKeys: config.ExpireReplayKeys,
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
		details["transfers"] = validationTransferDetails(validated.request, validated.payload.Type == tempo.CredentialTypeHash)
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

func validationTransferDetails(request tempo.ChargeRequest, push bool) []map[string]any {
	transfers := expectedTransfers(request)
	if push {
		transfers = expectedPushTransfers(request)
	}
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
	if !hasReplayStore(i.store) {
		return nil, fmt.Errorf("tempo server: configure a shared persistent replay Store before verifying payments; MemoryStore is only suitable for single-process development")
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
	// As in mppx, a zero-amount charge is paid only with a proof, sponsored
	// or not; a sponsored charge accepts a push (hash) credential, which the
	// client broadcast and paid fees for itself.
	if request.Amount == "0" && payload.Type != tempo.CredentialTypeProof {
		return nil, mpp.ErrInvalidPayload("zero-amount challenges require a proof credential")
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
	// A configured memo is shared by every payer on the route, so it cannot
	// tie an unsigned hash to this challenge: servers never offer push mode
	// with methodDetails.memo, and refuse hash credentials for it.
	if request.MethodDetails.Memo != "" {
		return mpp.ErrVerificationFailed("hash credentials are not accepted when methodDetails.memo is set")
	}
	if source == nil {
		return mpp.ErrInvalidPayload("hash credential must include a source")
	}
	// A challenge without a chainId is bound to the chain the RPC serves, as
	// in mppx.
	chainID, err := resolveChallengeChainID(ctx, validated.rpc, request)
	if err != nil {
		return err
	}
	if source.chainID != chainID {
		return mpp.ErrInvalidPayload("credential source chain id does not match the challenge")
	}
	// Replays are answered before the receipt lookup; Broadcast still claims
	// the hash atomically.
	if err := i.rejectUsedCredential(ctx, tempo.ChargeStoreKey(validated.payload.Hash), "transaction hash already used"); err != nil {
		return err
	}
	receiptMap, err := fetchReceipt(ctx, validated.rpc, validated.payload.Hash)
	if err != nil {
		return err
	}
	transfers, ok := receiptTransfers(receiptMap, request, source.address)
	if !ok || !matchTransfers(transfers, expectedPushTransfers(request), credential.Challenge.Realm, credential.Challenge.ID) {
		return mpp.ErrVerificationFailed("transaction receipt does not satisfy the charge request")
	}
	validated.challengeBound = boundToChallenge(transfers, credential.Challenge.Realm, credential.Challenge.ID)
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
	// Replays are answered before the signature is verified, as the
	// specification orders it; Broadcast still claims the proof atomically.
	if err := i.rejectUsedCredential(ctx, tempo.ChargeProofStoreKey(credential.Challenge.ID), "proof credential already used"); err != nil {
		return err
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
	// A proof's replay key is its challenge ID.
	validated.challengeBound = true
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
	transfers, ok := transactionTransfers(tx, request)
	if !ok || !matchTransfers(transfers, expectedTransfers(request), credential.Challenge.Realm, credential.Challenge.ID) {
		return mpp.ErrInvalidPayload("transaction does not contain a matching Tempo transfer")
	}
	validated.challengeBound = boundToChallenge(transfers, credential.Challenge.Realm, credential.Challenge.ID)

	sender, err := verifyTransactionSender(tx)
	if err != nil {
		return mpp.ErrInvalidPayload("transaction signature is invalid")
	}
	if validated.source != nil {
		// A challenge without a chainId is bound to the chain the RPC serves.
		chainID, err := resolveChallengeChainID(ctx, validated.rpc, request)
		if err != nil {
			return err
		}
		if validated.source.chainID != chainID {
			return mpp.ErrInvalidPayload("credential source chain id does not match the challenge")
		}
		if !strings.EqualFold(validated.source.address, sender.Hex()) {
			return mpp.ErrInvalidPayload("credential source does not match transaction signer")
		}
	}
	tx.From = sender

	if request.MethodDetails.FeePayer {
		var policy FeePayerPolicy
		if i.usesHostedFeePayer() {
			// A hosted fee payer picks the fee token; only an explicit
			// AllowedFeeTokens list restricts it, as in mppx.
			if feePayerForm && tx.FeeToken != (common.Address{}) && i.feeTokens != nil && !slices.Contains(i.feeTokens, tx.FeeToken) {
				return mpp.ErrInvalidPayload("fee payer transaction fee token is not supported")
			}
			policy, err = i.sponsorPolicyFor(tx.FeeToken)
			if err != nil {
				return err
			}
		} else {
			allowed, err := i.allowedFeeTokens(ctx, validated.rpc, request)
			if err != nil {
				return err
			}
			if feePayerForm && tx.FeeToken != (common.Address{}) && !slices.Contains(allowed, tx.FeeToken) {
				return mpp.ErrInvalidPayload("fee payer transaction fee token is not supported")
			}
			feeToken := allowed[0]
			if tx.FeeToken != (common.Address{}) {
				feeToken = tx.FeeToken
			}
			if i.feePayerSigner != nil {
				feeToken, err = i.resolveFeeToken(ctx, validated.rpc, allowed)
				if err != nil {
					return err
				}
				validated.feeToken = feeToken
				if i.feeToken == (common.Address{}) {
					for _, token := range allowed {
						if token != feeToken {
							validated.altFeeTokens = append(validated.altFeeTokens, token)
						}
					}
				}
			}
			policy, err = i.feePayerPolicyFor(feeToken.Hex())
			if err != nil {
				return err
			}
		}
		// tempo-go exposes the signing primitives already; this package keeps the
		// sponsor policy checks local until a shared helper exists upstream.
		if err := validateFeePayerTransaction(tx, credential.Challenge.Expires, policy); err != nil {
			return err
		}
		if !tx.AwaitingFeePayer {
			return mpp.ErrInvalidPayload("fee payer transaction must be marked as awaiting a fee payer")
		}
		if err := validateFeePayerDeadline(tx.ValidBefore, i.clock()); err != nil {
			return err
		}
		if tx.NonceKey == nil || tx.NonceKey.Cmp(tempo.ExpiringNonceKey) != 0 {
			return mpp.ErrInvalidPayload("fee payer transaction must use the expiring nonce key")
		}
		if !feePayerForm && tx.FeeToken != (common.Address{}) {
			return mpp.ErrInvalidPayload("fee payer transaction must omit fee token before co-signing")
		}
	} else {
		// Answer replays before the state-dependent simulation: once the first
		// payment has moved funds, a replayed simulation can fail and would
		// otherwise send a fresh 402 challenge. Broadcast still claims the hash
		// atomically.
		serialized, err := tempotx.Serialize(tx, nil)
		if err != nil {
			return mpp.ErrVerificationFailed("failed to serialize transaction")
		}
		// Hash the canonical envelope that broadcast claims, so the replay
		// key matches.
		serialized, err = canonicalBroadcastTransaction(serialized)
		if err != nil {
			return mpp.ErrVerificationFailed("failed to serialize transaction")
		}
		hash, err := tempotx.ComputeHash(serialized)
		if err != nil {
			return mpp.ErrVerificationFailed("failed to compute transaction hash")
		}
		if err := i.rejectUsedCredential(ctx, tempo.ChargeStoreKey(hash.Hex()), "transaction hash already used"); err != nil {
			return err
		}
		// A transaction already on chain pays only when this server
		// broadcast it for this challenge (see rejectIncludedTransaction).
		// Answer it before the simulation, which would fail on its spent
		// nonce with a less useful error, and skip the simulation for a
		// transaction Broadcast recovers from its receipt.
		included, err := i.rejectIncludedTransaction(ctx, validated.rpc, hash.Hex(), credential.Challenge.ID)
		if err != nil {
			return err
		}
		if included == nil {
			if err := simulateTransactionExecution(ctx, validated.rpc, tx); err != nil {
				return err
			}
		}
	}
	validated.sender = sender
	validated.tx = tx
	return nil
}

// rejectUsedCredential reads replay state without claiming it, so replays are
// rejected before any check that depends on chain state or signatures.
func (i *Intent) rejectUsedCredential(ctx context.Context, key, reason string) error {
	_, exists, err := i.store.Get(ctx, key)
	if err != nil {
		return err
	}
	if exists {
		return mpp.ErrCredentialReplayed(reason)
	}
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
		// Look up the funding currency before consuming the hash, so a slow or
		// failed lookup never leaves the hash used without a delivered receipt.
		options := []mpp.ReceiptOption{mpp.WithExternalID(validated.request.ExternalID)}
		if hashPaymentFundedByCurrency(ctx, credential, validated) {
			options = append(options, mpp.WithFundingCurrency(validated.request.Currency))
		}
		accepted, err := i.claimReplayKey(ctx, credential, validated, tempo.ChargeStoreKey(hash), hash)
		if err != nil {
			return nil, err
		}
		if !accepted {
			return nil, mpp.ErrCredentialReplayed("transaction hash already used")
		}
		return mpp.Success(tempo.MethodName, hash, options...), nil
	case tempo.CredentialTypeProof:
		challengeID := credential.Challenge.ID
		accepted, err := i.claimReplayKey(ctx, credential, validated, tempo.ChargeProofStoreKey(challengeID), challengeID)
		if err != nil {
			return nil, err
		}
		if !accepted {
			return nil, mpp.ErrCredentialReplayed("proof credential already used")
		}
		return mpp.Success(tempo.MethodName, challengeID, mpp.WithExternalID(validated.request.ExternalID)), nil
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
	// Only a successful settlement consumes the credential, as in mppx: a
	// signed transaction can be included at most once, so a retry after any
	// failure re-broadcasts it and recovers its receipt instead of being
	// rejected as a replay. Every replay claim taken below is released when
	// settlement fails, even after the broadcast, unless the transaction was
	// included: a reverted transaction, or one whose receipt does not satisfy
	// the charge, can never pay the challenge, so its credential stays used.
	var claimedKeys []string
	included := false
	// sponsorAddress is the fee payer that co-signed a sponsored transaction.
	var sponsorAddress common.Address
	senderSimulated := false
	// senderSigned is the sender-signed transaction before a local fee payer
	// co-signs it.
	var senderSigned *tempotx.Tx
	defer func() {
		if err == nil || included {
			return
		}
		// Release with a context the request cannot cancel, so a client
		// disconnect does not strand the claim.
		releaseCtx := context.WithoutCancel(ctx)
		for _, key := range claimedKeys {
			if releaseErr := i.store.Delete(releaseCtx, key); releaseErr != nil {
				receipt = nil
				err = withReplayReleaseError(err, releaseErr)
			}
		}
	}()
	// Store writes and the receipt wait after the send must not depend on the
	// request context: abandoning them on a client disconnect would leave the
	// payment's outcome unrecorded.
	storeCtx := context.WithoutCancel(ctx)
	// sponsoredClientHash is the sender-signed hash of a sponsored
	// transaction, which identifies it across fee payer signatures.
	var sponsoredClientHash string
	// settle completes a payment whose included receipt was found.
	settle := func(txHash string, receiptMap map[string]any) (*mpp.Receipt, error) {
		if !receiptMatches(receiptMap, credential, request, sender.Hex()) {
			return nil, mpp.ErrVerificationFailed("transaction receipt does not satisfy the charge request")
		}
		if request.MethodDetails.FeePayer {
			accepted, err := i.claimReplayKey(storeCtx, credential, validated, tempo.ChargeStoreKey(txHash), txHash)
			if err != nil {
				// The payment is on chain but unrecorded: release the
				// sponsored claim so a retry finds the broadcast record
				// and settles from the receipt.
				included = false
				return nil, err
			}
			if !accepted {
				return nil, mpp.ErrCredentialReplayed("transaction hash already used")
			}
		}
		i.forgetBroadcast(storeCtx, txHash, sponsoredClientHash)
		// transactionMatches accepted only the expected transfers of the
		// charge currency, so that currency funded the payment.
		return mpp.Success(
			tempo.MethodName,
			txHash,
			mpp.WithExternalID(request.ExternalID),
			mpp.WithFundingCurrency(request.Currency),
		), nil
	}
	// settleRecovered settles a transaction this server broadcast for this
	// challenge on an earlier attempt that landed without being settled:
	// after a failed receipt wait or replay store write, or an unknown
	// outcome. The replay key is still claimed atomically, so it settles
	// once.
	settleRecovered := func(txHash string, receiptMap map[string]any) (*mpp.Receipt, error) {
		if !request.MethodDetails.FeePayer {
			key := tempo.ChargeStoreKey(txHash)
			accepted, err := i.claimReplayKey(ctx, credential, validated, key, txHash)
			if err != nil {
				return nil, err
			}
			if !accepted {
				return nil, mpp.ErrCredentialReplayed("transaction hash already used")
			}
			claimedKeys = append(claimedKeys, key)
		}
		// An included transaction can never be broadcast again, whether or
		// not it succeeded, so its claims stay.
		included = true
		if asString(receiptMap["status"]) != "0x1" {
			return nil, mpp.ErrVerificationFailed("transaction reverted")
		}
		return settle(txHash, receiptMap)
	}

	if request.MethodDetails.FeePayer {
		// Preserve reservations from earlier versions while their credentials remain valid.
		if err := i.rejectUsedCredential(ctx, legacySponsoredChallengeStoreKey(credential.Challenge.ID), "fee payer challenge already used"); err != nil {
			return nil, err
		}
		clientTxHash, err := sponsoredTransactionHash(tx, sender)
		if err != nil {
			return nil, mpp.ErrMalformedCredential("failed to hash fee payer transaction")
		}
		sponsoredClientHash = clientTxHash
		sponsoredClaimKey := tempo.ChargeSponsoredTransactionStoreKey(clientTxHash)
		// Answer replays before state-dependent simulations: once the first
		// payment has moved funds, a replayed simulation can fail and would
		// otherwise send a fresh 402 challenge. The claim below stays atomic.
		if err := i.rejectUsedCredential(ctx, sponsoredClaimKey, "sponsored transaction already used"); err != nil {
			return nil, err
		}
		// A retry of a sponsored transaction this server broadcast for this
		// challenge, which landed without being settled, settles from its
		// receipt. Look it up before the sender simulation, which fails on
		// the spent nonce, and before a hosted fee payer re-signs it, which
		// may give another hash.
		recoveredHash, recoveredReceipt, err := i.recoverSponsoredBroadcast(ctx, rpc, clientTxHash, credential.Challenge.ID)
		if err != nil {
			return nil, err
		}
		if recoveredReceipt != nil {
			accepted, err := i.claimReplayKey(ctx, credential, validated, sponsoredClaimKey, clientTxHash)
			if err != nil {
				return nil, err
			}
			if !accepted {
				return nil, mpp.ErrCredentialReplayed("sponsored transaction already used")
			}
			claimedKeys = append(claimedKeys, sponsoredClaimKey)
			return settleRecovered(recoveredHash, recoveredReceipt)
		}
		// A local fee payer re-signs a retry identically, so its co-signed
		// transaction is simulated below only once it is known not to be on
		// chain yet. A remote fee payer may not, so keep screening the sender
		// before claiming and asking it to sign.
		if i.feePayerSigner == nil {
			if err := simulateSponsoredSenderExecution(ctx, rpc, tx); err != nil {
				return nil, err
			}
			senderSimulated = true
		}
		accepted, err := i.claimReplayKey(ctx, credential, validated, sponsoredClaimKey, clientTxHash)
		if err != nil {
			return nil, err
		}
		if !accepted {
			return nil, mpp.ErrCredentialReplayed("sponsored transaction already used")
		}
		claimedKeys = append(claimedKeys, sponsoredClaimKey)
		if err := validateFeePayerDeadline(tx.ValidBefore, i.clock()); err != nil {
			return nil, err
		}
		feeToken := validated.feeToken
		if i.feePayerSigner != nil {
			unsigned := *tx
			senderSigned = &unsigned
			tx.FeeToken = feeToken
			tx.AwaitingFeePayer = false
			if err := tempotx.AddFeePayerSignature(tx, i.feePayerSigner); err != nil {
				return nil, mpp.ErrVerificationFailed("failed to co-sign fee payer transaction")
			}
		} else if i.hostedFeePayer != nil {
			// As mppx's fillHostedFeePayerTransaction: only the sponsor's
			// signature and fee token are taken from its answer; every
			// sender-committed field stays the client's, and the sponsor is
			// recovered from the signature below.
			signature, hostedFeeToken, err := i.hostedFeePayer.fill(ctx, tx)
			if err != nil {
				return nil, err
			}
			if i.feeTokens != nil && !slices.Contains(i.feeTokens, hostedFeeToken) {
				return nil, mpp.ErrInvalidPayload("fee payer transaction fee token is not supported")
			}
			tx.FeeToken = hostedFeeToken
			tx.AwaitingFeePayer = false
			tx.FeePayerSignature = signature
			feeToken = hostedFeeToken
		} else {
			return nil, mpp.ErrVerificationFailed("fee payer challenge requires a configured fee payer (FeePayerSigner, FeePayerPrivateKey, FeePayerURL or HostedFeePayer)")
		}
		if !transactionMatches(tx, request, credential.Challenge.Realm, credential.Challenge.ID) {
			return nil, mpp.ErrVerificationFailed("co-signed transaction does not contain a matching Tempo transfer")
		}
		policy, err := i.sponsorPolicyFor(feeToken)
		if err != nil {
			return nil, err
		}
		if err := validateFeePayerTransaction(tx, credential.Challenge.Expires, policy); err != nil {
			return nil, err
		}
		if tx.AwaitingFeePayer {
			return nil, mpp.ErrVerificationFailed("co-signed transaction must clear the awaiting fee payer marker")
		}
		if tx.FeeToken != feeToken {
			return nil, mpp.ErrVerificationFailed("co-signed transaction fee token does not match the selected fee token")
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
		sponsorAddress = feePayerAddress
	}
	serialized, err := tempotx.Serialize(tx, nil)
	if err != nil {
		return nil, mpp.ErrVerificationFailed("failed to serialize transaction")
	}
	// Nodes normalize accepted signature encodings before deriving transaction
	// hashes. Broadcast the same canonical envelope so replay keys match the
	// node reference.
	serialized, err = canonicalBroadcastTransaction(serialized)
	if err != nil {
		return nil, mpp.ErrVerificationFailed("failed to serialize transaction")
	}

	computedHash, err := tempotx.ComputeHash(serialized)
	if err != nil {
		return nil, mpp.ErrVerificationFailed("failed to compute transaction hash")
	}
	signedHash := computedHash.Hex()

	// A transaction already on chain never pays this credential, even when
	// this server holds no replay record for it, unless this server
	// broadcast it for this challenge (see rejectIncludedTransaction): then
	// it settles from its receipt. A local fee payer signs
	// deterministically, but without a configured FeeToken it picks the fee
	// token by live balance, so an earlier attempt may have landed the
	// sender's transaction under another allowed fee token: check those
	// envelopes too.
	onChain, err := i.rejectIncludedTransaction(ctx, rpc, signedHash, credential.Challenge.ID)
	if err != nil {
		return nil, err
	}
	if onChain != nil {
		return settleRecovered(signedHash, onChain)
	}
	if senderSigned != nil {
		for _, token := range validated.altFeeTokens {
			_, _, altHash, err := i.coSignWithFeeToken(*senderSigned, token)
			if err != nil {
				continue
			}
			onChain, err := i.rejectIncludedTransaction(ctx, rpc, altHash, credential.Challenge.ID)
			if err != nil {
				return nil, err
			}
			if onChain != nil {
				return settleRecovered(altHash, onChain)
			}
		}
	}
	if request.MethodDetails.FeePayer {
		if !senderSimulated {
			if err := simulateSponsoredSenderExecution(ctx, rpc, tx); err != nil {
				return nil, err
			}
		}
		// The sender's simulation above is the gas-bounded check: it
		// runs at the broadcast gas limit. The node recovers an
		// eth_call's fee payer only from its feePayerSignature, so the
		// final simulation carries one: the hosted sponsor's as it
		// signed it, or for a local fee payer a simulation-only
		// signature made now over a copy with more gas, within the
		// sponsor policy. A sponsored eth_call charges more gas than
		// the mined transaction uses, so when the final simulation
		// could not get the full sponsoredSimulationGasBuffer (always
		// for a hosted fee payer) it tolerates running out of gas.
		simulated := tx
		if i.feePayerSigner != nil {
			policy, err := i.sponsorPolicyFor(tx.FeeToken)
			if err != nil {
				return nil, err
			}
			simulated, err = feePayerSimulationTransaction(tx, i.feePayerSigner, credential.Challenge.Expires, policy)
			if err != nil {
				return nil, mpp.ErrVerificationFailed("failed to co-sign fee payer simulation")
			}
		}
		tolerateOutOfGas := simulated.Gas-tx.Gas < sponsoredSimulationGasBuffer
		if err := simulateSponsoredTransactionExecution(ctx, rpc, simulated, tx, sponsorAddress, tolerateOutOfGas); err != nil {
			return nil, err
		}
	} else {
		// Match mppx's final pre-broadcast simulation to narrow the window
		// in which account or contract state can change after validation.
		if err := simulateTransactionExecution(ctx, rpc, tx); err != nil {
			return nil, err
		}
	}
	if err := validateChallengeExpiry(credential); err != nil {
		return nil, err
	}
	if request.MethodDetails.FeePayer {
		if err := validateFeePayerDeadline(tx.ValidBefore, i.clock()); err != nil {
			return nil, err
		}
	}

	if !request.MethodDetails.FeePayer {
		accepted, err := i.claimReplayKey(ctx, credential, validated, tempo.ChargeStoreKey(signedHash), signedHash)
		if err != nil {
			return nil, err
		}
		if !accepted {
			return nil, mpp.ErrCredentialReplayed("transaction hash already used")
		}
		claimedKeys = append(claimedKeys, tempo.ChargeStoreKey(signedHash))
	}

	var receiptMap map[string]any
	var txHash string
	// Record the broadcast before sending, so a retry can settle the
	// transaction from its receipt once it lands even when this attempt
	// fails afterwards (see rejectIncludedTransaction).
	recordedHash, recordedClient, err := i.recordBroadcast(storeCtx, credential, signedHash, sponsoredClientHash)
	if err != nil {
		return nil, err
	}
	// forgetRefused removes the records this attempt created for a
	// transaction the node refused; records an earlier attempt that did
	// broadcast it created stay.
	forgetRefused := func() {
		hash, client := "", ""
		if recordedHash {
			hash = signedHash
		}
		if recordedClient {
			client = sponsoredClientHash
		}
		i.forgetBroadcast(storeCtx, hash, client)
	}
	if txHash, err = rpc.SendRawTransaction(ctx, serialized); err != nil {
		// The node may have included the transaction before its RPC reply
		// failed, or hold it from a pending earlier broadcast. The check
		// above saw it not yet on chain, so a receipt found now is for a
		// transaction included after it.
		recoveredHash, recoveredReceipt, ok := lookupConfirmedReceipt(ctx, rpc, serialized, err)
		if !ok {
			// A node that already holds the transaction, such as one an
			// earlier attempt broadcast that is still pending, may include
			// it later.
			var rpcErr *temporpc.JSONRPCError
			if isDuplicateSubmission(err) || !errors.As(err, &rpcErr) {
				return nil, broadcastOutcomeUnknown(credential.Challenge.ID)
			}
			// The node refused the transaction, which may be because it is
			// already on chain from elsewhere: this attempt's record must
			// not let a retry settle it.
			forgetRefused()
			return nil, mpp.ErrVerificationFailed("transaction submission failed")
		}
		// An included transaction can never be broadcast again, whether or
		// not it succeeded, so its claims stay.
		included = true
		if asString(recoveredReceipt["status"]) != "0x1" {
			return nil, mpp.ErrVerificationFailed("transaction reverted")
		}
		txHash, receiptMap = recoveredHash, recoveredReceipt
	} else {
		if !strings.EqualFold(txHash, signedHash) {
			forgetRefused()
			return nil, mpp.ErrVerificationFailed("broadcast transaction hash does not match the signed transaction")
		}
		receiptCtx, cancel := context.WithTimeout(storeCtx, receiptWaitBudget)
		receiptMap, err = fetchReceipt(receiptCtx, rpc, txHash)
		cancel()
		if err != nil {
			if isTransactionReverted(err) {
				included = true
				return nil, err
			}
			return nil, broadcastOutcomeUnknown(credential.Challenge.ID)
		}
		included = true
	}
	return settle(txHash, receiptMap)
}

// hashPaymentFundedByCurrency reports whether a verified push payment was sent
// by the credential's payer and consists only of the expected transfers of the
// charge currency. Only then does the receipt report the funding currency; a
// lookup failure or any other call pattern leaves it unset without failing the
// already verified payment.
func hashPaymentFundedByCurrency(ctx context.Context, credential *mpp.Credential, validated *validatedChargeCredential) bool {
	response, err := validated.rpc.SendRequest(ctx, "eth_getTransactionByHash", validated.payload.Hash)
	if err != nil || response.CheckError() != nil {
		return false
	}
	transaction, ok := response.Result.(map[string]any)
	if !ok || !strings.EqualFold(asString(transaction["from"]), validated.sender.Hex()) {
		return false
	}
	var calls []any
	if rawCalls, ok := transaction["calls"].([]any); ok {
		calls = rawCalls
	} else if to := asString(transaction["to"]); transaction["to"] != nil && to != "" {
		calls = []any{map[string]any{"to": to, "input": transaction["input"], "value": transaction["value"]}}
	}
	request := validated.request
	expected := expectedPushTransfers(request)
	if len(calls) != len(expected) {
		return false
	}
	actual := make([]decodedTransfer, 0, len(calls))
	for _, rawCall := range calls {
		call, ok := rawCall.(map[string]any)
		if !ok || !strings.EqualFold(asString(call["to"]), request.Currency) {
			return false
		}
		if value, ok := call["value"].(string); ok && value != "" {
			if amount, err := tempo.ParseHexBigInt(value); err != nil || amount.Sign() != 0 {
				return false
			}
		}
		data, ok := call["data"].(string)
		if !ok {
			data, _ = call["input"].(string)
		}
		raw, err := hexutil.Decode(data)
		if err != nil {
			return false
		}
		decoded, ok := decodeCallTransfer(raw)
		if !ok {
			return false
		}
		actual = append(actual, decoded)
	}
	return matchTransfers(actual, expected, credential.Challenge.Realm, credential.Challenge.ID)
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

// keychainV1SignatureType is the legacy Keychain V1 signature type byte.
const keychainV1SignatureType = 0x03

// canonicalBroadcastTransaction rewrites a secp256k1 sender signature's
// recovery ID from 0/1 to 27/28, including the inner secp256k1 signature of
// a keychain envelope. Tempo nodes encode secp256k1 signatures that way when
// deriving the transaction hash, and so does mppx, while tempo-go serializes
// 0/1. Hashing and broadcasting the node's encoding keeps the replay key
// equal to the receipt reference.
func canonicalBroadcastTransaction(serialized string) (string, error) {
	raw, err := hexutil.Decode(serialized)
	if err != nil {
		return "", err
	}
	if len(raw) < 2 || raw[0] != 0x76 {
		return "", fmt.Errorf("expected a 0x76 Tempo transaction")
	}
	content, rest, err := rlp.SplitList(raw[1:])
	if err != nil {
		return "", err
	}
	if len(rest) != 0 {
		return "", fmt.Errorf("unexpected trailing bytes after transaction")
	}
	var (
		kind      rlp.Kind
		signature []byte
	)
	for len(content) > 0 {
		kind, signature, content, err = rlp.Split(content)
		if err != nil {
			return "", err
		}
	}
	if kind != rlp.String {
		return serialized, nil
	}
	switch {
	case len(signature) == 65:
	case len(signature) == keychain.KeychainSignatureLength &&
		(signature[0] == keychainV1SignatureType || signature[0] == keychain.KeychainSignatureType):
		// Keychain envelopes (type || root account || inner signature) wrap a
		// secp256k1 inner signature at this length, which the node re-encodes
		// the same way.
	default:
		return serialized, nil
	}
	// The signature is the transaction's last field, so its recovery ID is
	// the final byte of the envelope.
	if recoveryID := signature[len(signature)-1]; recoveryID <= 1 {
		signature[len(signature)-1] = recoveryID + 27
	}
	return hexutil.Encode(raw), nil
}

// isTransactionReverted reports whether fetchReceipt found a reverted receipt.
func isTransactionReverted(err error) bool {
	paymentErr, ok := err.(*mpp.PaymentError)
	return ok && paymentErr.Detail == "transaction reverted"
}

// broadcastOutcomeUnknown reports a receipt wait that failed after the node
// accepted the transaction without showing whether it was included. A retry
// with the same credential re-broadcasts the transaction and recovers its
// receipt while it is still pending, and settles it from its receipt once it
// is on chain, since this server recorded broadcasting it (see
// rejectIncludedTransaction).
func broadcastOutcomeUnknown(challengeID string) error {
	unknown := mpp.ErrPaymentOutcomeUnknown(challengeID)
	unknown.Detail += " Check the original transaction on chain before authorizing any further payment."
	return unknown
}

// claimReplayKey atomically records first use of a replay key. With
// IntentConfig.ExpireReplayKeys, the key of a credential bound to its
// challenge expires once the credential can no longer be accepted; see
// replayClaimExpiry. Otherwise it is kept forever.
func (i *Intent) claimReplayKey(ctx context.Context, credential *mpp.Credential, validated *validatedChargeCredential, key, value string) (bool, error) {
	if store, ok := i.store.(tempo.ExpiringStore); ok && i.expireReplayKeys && validated.challengeBound {
		if expiresAt, ok := replayClaimExpiry(credential); ok {
			return store.PutIfAbsentUntil(ctx, key, value, expiresAt)
		}
	}
	return i.store.PutIfAbsent(ctx, key, value)
}

// replayClaimExpiry returns when a replay key claimed for credential may
// expire: its challenge's expiry plus replayClaimExpiryGrace.
//
// It applies only to credentials bound to their challenge
// (validatedChargeCredential.challengeBound): a proof, whose key is the
// challenge ID, or a hash or transaction credential with a transfer carrying
// an attribution memo for the challenge's realm and ID, which every other
// challenge rejects. The attribution memo is optional, so a payment without
// one could pay a later challenge once its key expired: claimReplayKey keeps
// those keys forever. The challenge ID is an HMAC over the challenge's
// expiry, which server.Mpp verifies before settlement, and credentials are
// only accepted before that expiry, so after it no challenge can accept the
// same bound proof, transaction or hash again. The grace absorbs clock skew
// between verifier replicas and the store.
func replayClaimExpiry(credential *mpp.Credential) (time.Time, bool) {
	if credential == nil || credential.Challenge.Expires == "" {
		return time.Time{}, false
	}
	expires, err := parseExpires(credential.Challenge.Expires)
	if err != nil {
		return time.Time{}, false
	}
	return expires.Add(replayClaimExpiryGrace), true
}

func withReplayReleaseError(settlementErr, releaseErr error) error {
	detail := fmt.Sprintf("failed to release replay claim: %v", releaseErr)
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
		return i.rpcClients.Get(i.rpcURL), nil
	}
	// Without a chain ID, RPCURLForChain(0) is mainnet.
	var chainID int64
	if request.MethodDetails.ChainID != nil {
		chainID = *request.MethodDetails.ChainID
	}
	rpcURL, err := tempo.RPCURLForChain(chainID)
	if err != nil {
		return nil, fmt.Errorf("tempo server: %w; configure Intent.RPC or Intent.RPCURL explicitly", err)
	}
	return i.rpcClients.Get(rpcURL), nil
}

// sponsoredTransactionHash binds the verified payer to the canonical signing payload.
// Wire encodings, sponsor-selected fee tokens and alternate signatures must not
// create additional sponsorship authorizations for the same payer transaction.
func sponsoredTransactionHash(tx *tempotx.Tx, sender common.Address) (string, error) {
	payload, err := tempotx.GetSignPayload(tx)
	if err != nil {
		return "", err
	}
	return crypto.Keccak256Hash(sender.Bytes(), payload.Bytes()).Hex(), nil
}

// TODO(tempo-go): extract the Tempo transaction/receipt matching and fee-payer
// verification helpers below once tempo-go exposes a shared verifier surface for
// TIP-20 charge flows.

func transactionMatches(tx *tempotx.Tx, request tempo.ChargeRequest, realm, challengeID string) bool {
	actual, ok := transactionTransfers(tx, request)
	return ok && matchTransfers(actual, expectedTransfers(request), realm, challengeID)
}

// transactionTransfers decodes a transaction's calls, which must all be
// plain TIP-20 transfers of the charge currency, one per expected transfer.
func transactionTransfers(tx *tempotx.Tx, request tempo.ChargeRequest) ([]decodedTransfer, bool) {
	if len(tx.Calls) != len(request.MethodDetails.Splits)+1 || len(tx.AccessList) != 0 {
		return nil, false
	}
	actual := make([]decodedTransfer, 0, len(tx.Calls))
	for _, call := range tx.Calls {
		if call.To == nil || !strings.EqualFold(call.To.Hex(), request.Currency) {
			return nil, false
		}
		if call.Value != nil && call.Value.Sign() != 0 {
			return nil, false
		}
		decoded, ok := decodeCallTransfer(call.Data)
		if !ok {
			return nil, false
		}
		actual = append(actual, decoded)
	}
	return actual, true
}

// boundToChallenge reports whether a transfer carries an attribution memo
// for this challenge's realm and ID. matchTransfers rejects such a memo on
// every other challenge of the realm, including one that configures it as
// its methodDetails.memo or a split memo, so the payment can pay no other.
func boundToChallenge(transfers []decodedTransfer, realm, challengeID string) bool {
	for _, transfer := range transfers {
		if transfer.hasMemo && tempo.VerifyAttributionServer(transfer.memo, realm) && tempo.VerifyAttributionChallenge(transfer.memo, challengeID) {
			return true
		}
	}
	return false
}

// lookupConfirmedReceipt checks once, without rebroadcasting, whether a
// transaction whose submission returned an error was confirmed anyway.
func lookupConfirmedReceipt(ctx context.Context, rpc tempo.RPCClient, serialized string, sendErr error) (string, map[string]any, bool) {
	hash, err := tempotx.ComputeHash(serialized)
	if err != nil {
		return "", nil, false
	}
	// eth_sendRawTransaction is async, so a transaction the node accepted is
	// usually still pending when the reply fails: poll like fetchReceipt. A
	// JSON-RPC error means the node answered and rejected it, so check once,
	// unless it rejected a duplicate of a transaction it already holds.
	attempts := receiptRetryAttempts
	var rpcErr *temporpc.JSONRPCError
	if errors.As(sendErr, &rpcErr) && !isDuplicateSubmission(sendErr) {
		attempts = 1
	}
	// The send may have failed because ctx ended; the lookup still needs to run.
	lookupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), receiptWaitBudget)
	defer cancel()
	for attempt := 0; attempt < attempts; attempt++ {
		response, err := rpc.SendRequest(lookupCtx, "eth_getTransactionReceipt", hash.Hex())
		if err == nil && response.CheckError() == nil {
			// The caller rejects a reverted receipt but keeps its claims.
			if receipt, ok := response.Result.(map[string]any); ok && len(receipt) > 0 {
				return hash.Hex(), receipt, true
			}
		}
		if attempt < attempts-1 {
			timer := time.NewTimer(receiptRetryDelay)
			select {
			case <-lookupCtx.Done():
				timer.Stop()
				return "", nil, false
			case <-timer.C:
			}
		}
	}
	return "", nil, false
}

// isDuplicateSubmission reports whether eth_sendRawTransaction failed because
// the node already holds the transaction, as geth- and reth-based nodes answer
// a re-broadcast of a pending transaction, and as Tempo answers with
// ExpiringNonceReplay when an expiring-nonce transaction with this hash was
// already accepted, such as when a transport retries a send the node took.
func isDuplicateSubmission(err error) bool {
	var rpcErr *temporpc.JSONRPCError
	if !errors.As(err, &rpcErr) {
		return false
	}
	message := strings.ToLower(rpcErr.Message)
	for _, marker := range []string{"already known", "known transaction", "already imported", "alreadyknown", "expiringnoncereplay"} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}

// rejectIncludedTransaction looks up, once and without polling, the receipt
// of the transaction with hash before this server broadcasts it. It returns
// nil when the transaction is not on chain yet. One already on chain,
// reverted or not, fails with verification-failed: another server, or an
// earlier settlement whose replay record expired or is kept elsewhere, may
// already have counted it, so it never pays a credential, as in mppx. The
// exception is a transaction this server broadcast itself for challengeID
// (recordBroadcast) whose settlement failed afterwards, such as on a receipt
// timeout, a replay store write error or an unknown outcome: its receipt is
// returned so the retry settles it. A failed lookup fails too: only a
// transaction seen not yet on chain here may be recovered after its
// broadcast returns an error.
func (i *Intent) rejectIncludedTransaction(ctx context.Context, rpc tempo.RPCClient, hash, challengeID string) (map[string]any, error) {
	response, err := rpc.SendRequest(ctx, "eth_getTransactionReceipt", hash)
	if err != nil || response.CheckError() != nil {
		return nil, mpp.ErrVerificationFailed("failed to check whether the transaction is already on chain")
	}
	receipt, ok := response.Result.(map[string]any)
	if !ok || len(receipt) == 0 {
		return nil, nil
	}
	recorded, exists, err := i.store.Get(ctx, broadcastStoreKey(hash))
	if err != nil {
		return nil, err
	}
	if !exists || recorded != challengeID {
		return nil, mpp.ErrVerificationFailed("transaction is already on chain")
	}
	return receipt, nil
}

// recoverSponsoredBroadcast finds the transaction this server broadcast for
// challengeID from the sponsored transaction with sender-signed hash
// clientHash, and returns its hash and receipt once it is on chain. It
// returns no receipt when there is no such broadcast or it is not on chain
// yet.
func (i *Intent) recoverSponsoredBroadcast(ctx context.Context, rpc tempo.RPCClient, clientHash, challengeID string) (string, map[string]any, error) {
	recorded, exists, err := i.store.Get(ctx, broadcastSponsoredStoreKey(clientHash))
	if err != nil {
		return "", nil, err
	}
	if !exists {
		return "", nil, nil
	}
	recordedChallenge, hash, ok := strings.Cut(recorded, " ")
	if !ok || recordedChallenge != challengeID || hash == "" {
		return "", nil, nil
	}
	receipt, err := i.rejectIncludedTransaction(ctx, rpc, hash, challengeID)
	if err != nil || receipt == nil {
		return "", nil, err
	}
	return hash, receipt, nil
}

// recordBroadcast records, before this server sends the transaction with
// hash, that it broadcasts it for the credential's challenge, so a retry of
// that credential can settle it from its receipt once it lands
// (rejectIncludedTransaction). The record lives in the replay Store until
// the challenge can no longer be accepted, when the Store supports expiry.
// For a sponsored transaction, clientHash, its sender-signed hash, also
// records which broadcast hash a retry looks up first. A transaction this
// server already broadcast for another challenge, which a retry must not
// settle twice, is refused. It reports which records this call created.
func (i *Intent) recordBroadcast(ctx context.Context, credential *mpp.Credential, hash, clientHash string) (hashCreated, clientCreated bool, err error) {
	challengeID := credential.Challenge.ID
	key := broadcastStoreKey(hash)
	hashCreated, err = i.putUntilChallengeExpiry(ctx, credential, key, challengeID)
	if err != nil {
		return false, false, err
	}
	if !hashCreated {
		recorded, exists, err := i.store.Get(ctx, key)
		if err != nil {
			return false, false, err
		}
		if !exists {
			return false, false, mpp.ErrVerificationFailed("failed to record the transaction broadcast")
		}
		if recorded != challengeID {
			return false, false, mpp.ErrVerificationFailed("transaction was broadcast for another challenge")
		}
	}
	if clientHash == "" {
		return hashCreated, false, nil
	}
	// The first broadcast hash is kept: a retry looks it up first, and
	// rejectIncludedTransaction checks any other one when it is re-signed
	// identically.
	clientCreated, err = i.putUntilChallengeExpiry(ctx, credential, broadcastSponsoredStoreKey(clientHash), challengeID+" "+strings.ToLower(hash))
	if err != nil {
		if hashCreated {
			i.forgetBroadcast(ctx, hash, "")
		}
		return false, false, err
	}
	return hashCreated, clientCreated, nil
}

// forgetBroadcast removes broadcast records, for hash and clientHash when
// not empty, once their transaction settled or the node refused it.
// Failures are ignored: a record left after settlement lets only this
// challenge's credential reach that transaction, whose replay key then
// refuses it, and it expires with the challenge when the Store supports
// expiry.
func (i *Intent) forgetBroadcast(ctx context.Context, hash, clientHash string) {
	if hash != "" {
		_ = i.store.Delete(ctx, broadcastStoreKey(hash))
	}
	if clientHash != "" {
		_ = i.store.Delete(ctx, broadcastSponsoredStoreKey(clientHash))
	}
}

// putUntilChallengeExpiry stores value under key when the key is unused,
// until the credential's challenge can no longer be accepted when the Store
// supports expiry, and otherwise until it is deleted.
func (i *Intent) putUntilChallengeExpiry(ctx context.Context, credential *mpp.Credential, key, value string) (bool, error) {
	if store, ok := i.store.(tempo.ExpiringStore); ok {
		if expiresAt, ok := replayClaimExpiry(credential); ok {
			return store.PutIfAbsentUntil(ctx, key, value, expiresAt)
		}
	}
	return i.store.PutIfAbsent(ctx, key, value)
}

// broadcastStoreKey is the replay Store key recording that this server
// broadcast the transaction with hash.
func broadcastStoreKey(hash string) string {
	return tempo.ReplayKeyPrefix + "broadcast:" + strings.ToLower(hash)
}

// broadcastSponsoredStoreKey is the replay Store key recording the
// transaction this server broadcast for a sponsored transaction with
// sender-signed hash clientHash.
func broadcastSponsoredStoreKey(clientHash string) string {
	return tempo.ReplayKeyPrefix + "broadcast:sponsor:" + strings.ToLower(clientHash)
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
	actual, ok := receiptTransfers(receipt, request, sourceAddress)
	return ok && matchTransfers(actual, expectedTransfers(request), credential.Challenge.Realm, credential.Challenge.ID)
}

// receiptTransfers decodes a receipt's charge-currency transfers from
// sourceAddress, skipping fee-controller transfers and collapsing each
// TransferWithMemo with its paired Transfer log.
func receiptTransfers(receipt map[string]any, request tempo.ChargeRequest, sourceAddress string) ([]decodedTransfer, bool) {
	logs, ok := receipt["logs"].([]any)
	if !ok {
		return nil, false
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
	return canonicalReceiptTransfers(actual)
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
	amount       string
	allowAnyMemo bool
	memo         string
	recipient    string
	// requireAttribution requires the attribution memo for the challenge's
	// realm and ID.
	requireAttribution bool
}

type decodedTransfer struct {
	amount    string
	hasMemo   bool
	memo      string
	recipient string
}

// expectedPushTransfers is expectedTransfers for a push (hash) credential:
// the primary transfer must carry the attribution memo for the challenge's
// realm and ID. A hash credential proves only that some transfer reached the
// recipient, and its source is unsigned, so the attribution memo is what ties
// the transfer to this challenge; otherwise anyone could claim an unrelated
// transfer's hash. A configured methodDetails.memo, which every payer on the
// route carries, cannot do that, so validateHash refuses hash credentials for
// it and servers never offer push mode with one.
func expectedPushTransfers(request tempo.ChargeRequest) []expectedTransfer {
	transfers := expectedTransfers(request)
	transfers[0].allowAnyMemo = false
	transfers[0].memo = ""
	transfers[0].requireAttribution = true
	return transfers
}

// expectedTransfers returns the transfers a pull (transaction) credential's
// transaction must make. The server broadcasts the client's own signed
// transaction, which the chain refuses to include twice, so the attribution
// memo is optional there.
func expectedTransfers(request tempo.ChargeRequest) []expectedTransfer {
	transfers := make([]expectedTransfer, 0, len(request.MethodDetails.Splits)+1)
	primaryAmount, _ := new(big.Int).SetString(request.Amount, 10)
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
	// The primary transfer carries the server's methodDetails.memo exactly
	// when one is set. Otherwise its attribution memo is optional, as for a
	// split without a memo: it may carry none or any memo but an attribution
	// memo for another challenge or realm.
	primary := expectedTransfer{
		amount:       primaryAmount.String(),
		recipient:    request.Recipient,
		allowAnyMemo: request.MethodDetails.Memo == "",
		memo:         request.MethodDetails.Memo,
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
			if want.requireAttribution {
				if got.hasMemo && tempo.IsAttributionMemo(got.memo) &&
					tempo.VerifyAttributionServer(got.memo, realm) && tempo.VerifyAttributionChallenge(got.memo, challengeID) {
					used[index] = true
					matched = true
					break
				}
				continue
			}
			if want.memo != "" {
				// A configured memo that is this realm's attribution memo for
				// another challenge never matches: a payment carrying it is
				// bound to that challenge, whose replay key may expire (see
				// boundToChallenge). Another realm's attribution memo, such
				// as a partner's split memo, still matches.
				if tempo.IsAttributionMemo(want.memo) && tempo.VerifyAttributionServer(want.memo, realm) &&
					!tempo.VerifyAttributionChallenge(want.memo, challengeID) {
					continue
				}
				if got.hasMemo && strings.EqualFold(got.memo, want.memo) {
					used[index] = true
					matched = true
					break
				}
				continue
			}
			// A transfer without a configured memo accepts any memo except
			// an attribution memo for another realm or challenge. Otherwise
			// one payment could carry memos for two challenges and pay
			// both: here and, as a push credential, at the realm and
			// challenge its memo names, which keeps its own replay
			// records, or here again once the first replay marker expires.
			// Memos the server configured are matched exactly above.
			if want.allowAnyMemo && got.hasMemo && tempo.IsAttributionMemo(got.memo) &&
				(!tempo.VerifyAttributionServer(got.memo, realm) || !tempo.VerifyAttributionChallenge(got.memo, challengeID)) {
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

// recoverProofSigner recovers the key that signed a proof with tempo-go's
// signer.RecoverEnvelope: a secp256k1, P256 or WebAuthn signature over the
// proof digest, or a Keychain envelope naming source whose inner signature
// (any of those) signs the digest (Keychain V1, type 0x03) or
// keccak256(0x04 || digest || source) (Keychain V2, type 0x04), as mppx's
// recoverAuthorizedProofSigner does.
func recoverProofSigner(proofHash common.Hash, encoded string, source common.Address) (common.Address, error) {
	raw, err := hexutil.Decode(encoded)
	if err != nil {
		return common.Address{}, err
	}
	recovered, err := temposigner.RecoverEnvelope(proofHash, raw)
	if err != nil {
		return common.Address{}, err
	}
	if recovered.Keychain != 0 && recovered.Account != source {
		return common.Address{}, fmt.Errorf("keychain proof root mismatch")
	}
	return recovered.KeyID, nil
}

// recoverSecp256k1 recovers the signer of a 65-byte r || s || v signature,
// v being 0, 1, 27 or 28.
func recoverSecp256k1(hash common.Hash, raw []byte) (common.Address, error) {
	if len(raw) != 65 {
		return common.Address{}, fmt.Errorf("unexpected signature length %d", len(raw))
	}
	v := raw[64]
	if v >= 27 {
		v -= 27
	}
	if v > 1 {
		return common.Address{}, fmt.Errorf("invalid recovery id")
	}
	return temposigner.RecoverAddress(hash, temposigner.NewSignature(
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
	// A key is active only when the keychain stores it under its own,
	// non-zero key ID.
	keyID := common.BytesToAddress(resultBytes[44:64])
	if keyID == (common.Address{}) || keyID != accessKey {
		return false, nil
	}
	// As mppx (Actions.accessKey.getMetadata): a key is active while it is
	// not revoked and its expiry is in the future, so a stored expiry of 0
	// (an unknown key) is inactive.
	expiry := new(big.Int).SetBytes(resultBytes[64:96])
	if expiry.Cmp(big.NewInt(time.Now().Unix())) <= 0 {
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

// simulateSponsoredSenderExecution simulates a fee-sponsored transaction's
// calls from its sender, without any fee payer, at the transaction's own gas
// limit: the gas-bounded check that the broadcast cannot run out of gas. It
// carries the sender's nonce, nonce key, validity window and key
// authorization, which set the intrinsic gas, but no fee token or fee per
// gas, so the node charges no fee and (since Tempo v1.18, which checks the
// payer's fee token balance in eth_call) needs no fee token balance from a
// sender the sponsor pays for.
func simulateSponsoredSenderExecution(ctx context.Context, rpc tempo.RPCClient, tx *tempotx.Tx) error {
	callObject, err := transactionCallObject(tx)
	if err != nil {
		return mpp.ErrVerificationFailed("transaction preflight failed")
	}
	delete(callObject, "feeToken")
	delete(callObject, "maxFeePerGas")
	delete(callObject, "maxPriorityFeePerGas")
	response, err := rpc.SendRequest(ctx, "eth_call", callObject, "latest")
	if err != nil {
		return mpp.ErrVerificationFailed("transaction preflight failed")
	}
	if err := response.CheckError(); err != nil {
		return paymentSimulationFailed(&PaymentSimulationError{
			Phase:        SimulationPhaseSender,
			Sender:       tx.From,
			PaymentToken: simulationPaymentToken(tx),
			Err:          err,
		})
	}
	return nil
}

// sponsoredSimulationGasBuffer is the most gas a local fee payer's
// simulation-only signature adds over the transaction's gas limit; the fee
// payer signature binds the gas limit, so the final simulation signs a copy
// with up to this much more gas while the broadcast transaction keeps its own.
// eth_call skips fee collection, leaving the fee token's storage slots cold,
// so a sponsored eth_call needs exactly 6,000 more gas than the mined
// transaction when its calls touch the fee token and none otherwise. The
// remaining risk is a gas limit up to 6,000 short on calls that don't touch
// the fee token; a node fix that warms those slots in eth_call would remove
// the buffer.
const sponsoredSimulationGasBuffer = 6_000

// sponsoredSimulationGas returns the gas limit of tx's simulation-only copy:
// tx.Gas plus sponsoredSimulationGasBuffer, lowered to stay within the
// sponsor policy's gas cap and total fee budget (gas times maxFeePerGas), and
// never below tx.Gas.
func sponsoredSimulationGas(tx *tempotx.Tx, policy FeePayerPolicy) uint64 {
	gas := feePayerMaxGas
	if tx.Gas <= math.MaxUint64-sponsoredSimulationGasBuffer && tx.Gas+sponsoredSimulationGasBuffer < gas {
		gas = tx.Gas + sponsoredSimulationGasBuffer
	}
	if tx.MaxFeePerGas != nil && tx.MaxFeePerGas.Sign() > 0 && policy.MaxTotalFee != nil {
		budget := new(big.Int).Quo(policy.MaxTotalFee, tx.MaxFeePerGas)
		if budget.IsUint64() && budget.Uint64() < gas {
			gas = budget.Uint64()
		}
	}
	return max(gas, tx.Gas)
}

// feePayerSimulationTransaction returns a copy of the co-signed transaction
// tx for the final sponsored eth_call, its gas limit raised by up to
// sponsoredSimulationGasBuffer within policy (sponsoredSimulationGas), and
// co-signed by signer over the same fee payer payload as the broadcast
// co-signature (sender tx.From, fee token tx.FeeToken) only once the copy
// passes the same sponsor policy check as tx. tx itself is unchanged, and
// the copy is only ever simulated, never broadcast. Call it only after the
// sender's simulation passed.
func feePayerSimulationTransaction(tx *tempotx.Tx, signer *temposigner.Signer, challengeExpires string, policy FeePayerPolicy) (*tempotx.Tx, error) {
	simulation := tx.Clone()
	simulation.From = tx.From
	simulation.FeeToken = tx.FeeToken
	simulation.AwaitingFeePayer = false
	simulation.Gas = sponsoredSimulationGas(tx, policy)
	if err := validateFeePayerTransaction(simulation, challengeExpires, policy); err != nil {
		return nil, err
	}
	hash, err := tempotx.GetFeePayerSignPayload(simulation, tx.From)
	if err != nil {
		return nil, err
	}
	signature, err := signer.Sign(hash)
	if err != nil {
		return nil, err
	}
	simulation.FeePayerSignature = signature
	return simulation, nil
}

// simulateSponsoredTransactionExecution simulates the final fee-sponsored
// transaction tx from its sender with its fee payer signature, never the
// sender's. Since Tempo v1.18 an eth_call checks the fee payer's fee token
// balance (gas limit times maxFeePerGas), and the node recovers the fee payer
// only from feePayerSignature: a bare feePayer address is ignored and the
// sender is billed, which fails when the sender does not hold the sponsor's
// fee token. feePayer is sent too, as mppx does.
//
// With tolerateOutOfGas, an error that only says the call ran out of gas
// (isOutOfGasError) passes: a sponsored eth_call charges more gas than the
// mined transaction, and the caller already checked the calls at the
// broadcast gas limit with simulateSponsoredSenderExecution. Any other
// error, such as an insufficient fee token balance or a revert, fails.
func simulateSponsoredTransactionExecution(
	ctx context.Context,
	rpc tempo.RPCClient,
	tx *tempotx.Tx,
	senderTx *tempotx.Tx,
	feePayer common.Address,
	tolerateOutOfGas bool,
) error {
	callObject, err := transactionCallObject(tx, senderTx)
	if err != nil {
		return mpp.ErrVerificationFailed("transaction preflight failed")
	}
	callObject["feePayer"] = feePayer.Hex()
	if tx.FeePayerSignature != nil {
		callObject["feePayerSignature"] = rpcSignature(tx.FeePayerSignature)
	}
	response, err := rpc.SendRequest(ctx, "eth_call", callObject, "latest")
	if err != nil {
		return mpp.ErrVerificationFailed("transaction preflight failed")
	}
	if err := response.CheckError(); err != nil {
		if tolerateOutOfGas && isOutOfGasError(err) {
			return nil
		}
		return paymentSimulationFailed(&PaymentSimulationError{
			Phase:        SimulationPhaseSponsored,
			Sender:       tx.From,
			FeePayer:     &feePayer,
			PaymentToken: simulationPaymentToken(tx),
			Err:          err,
		})
	}
	return nil
}

// SimulationPhase names the preflight simulation of a fee-sponsored
// transaction that failed.
type SimulationPhase string

const (
	// SimulationPhaseSender is the simulation of the sender's calls before
	// sponsorship, without any fee payer.
	SimulationPhaseSender SimulationPhase = "sender"
	// SimulationPhaseSponsored is the simulation of the final sponsored
	// transaction, billed to its fee payer.
	SimulationPhaseSponsored SimulationPhase = "sponsored"
)

// PaymentSimulationError reports a fee-sponsored transaction whose preflight
// simulation the node rejected, such as a revert or an insufficient fee token
// balance, as mppx's PaymentSimulationError. It is the cause of the
// verification-failed "transaction preflight failed" error the charge returns,
// so the problem details sent to clients are unchanged; reach it with
// errors.As. Transport failures that never got the node's answer are not
// reported as one.
type PaymentSimulationError struct {
	// Phase is the simulation that failed.
	Phase SimulationPhase
	// Sender is the sender of the simulated transaction.
	Sender common.Address
	// FeePayer is the fee payer the sponsored simulation billed: set only in
	// SimulationPhaseSponsored.
	FeePayer *common.Address
	// PaymentToken is the TIP-20 token of the transaction's call when it is a
	// single transfer or transferWithMemo call, and nil otherwise.
	PaymentToken *common.Address
	// Err is the node's simulation error.
	Err error
}

func (e *PaymentSimulationError) Error() string {
	if e.Err == nil {
		return "payment simulation failed (" + string(e.Phase) + ")"
	}
	return "payment simulation failed (" + string(e.Phase) + "): " + e.Err.Error()
}

// Unwrap returns the node's simulation error.
func (e *PaymentSimulationError) Unwrap() error {
	return e.Err
}

// Is reports whether target is mpp.ErrVerification, so a tagged preflight
// failure still matches errors.Is(err, mpp.ErrVerification) as the untagged
// one did.
func (e *PaymentSimulationError) Is(target error) bool {
	return target == mpp.ErrVerification
}

// paymentSimulationFailed returns the verification-failed preflight error with
// simulation as its cause.
func paymentSimulationFailed(simulation *PaymentSimulationError) error {
	return mpp.ErrVerificationFailed("transaction preflight failed").WithCause(simulation)
}

// simulationPaymentToken returns the token of tx's call when tx is a single
// TIP-20 transfer or transferWithMemo call, as mppx's preflightSponsorship
// does, and nil otherwise.
func simulationPaymentToken(tx *tempotx.Tx) *common.Address {
	if len(tx.Calls) != 1 || tx.Calls[0].To == nil || len(tx.Calls[0].Data) < 4 {
		return nil
	}
	switch common.Bytes2Hex(tx.Calls[0].Data[:4]) {
	case tempo.TransferSelector, tempo.TransferWithMemoSelector:
		token := *tx.Calls[0].To
		return &token
	default:
		return nil
	}
}

// isOutOfGasError reports whether err is a JSON-RPC error from the node
// saying the call ran out of gas. Tempo nodes answer an eth_call that
// exhausts its gas limit with "out of gas: ..." (code -32003, shared with
// "insufficient fee token balance") or, below the intrinsic gas, "intrinsic
// gas too low" (code -32000), with no data, so only the message tells them
// apart. As in mppx's isOutOfGasError, only the terminal node error counts:
// the *temporpc.JSONRPCError in err's chain (it wraps nothing, so it is the
// innermost error), never a wrapper's message. The match is anchored to the
// start of the node's message, and an execution revert (code 3) or an error
// carrying revert data is never gas exhaustion, so a revert reason or wrapper
// text that merely mentions gas cannot hide another failure and let the
// payment broadcast.
func isOutOfGasError(err error) bool {
	var rpcErr *temporpc.JSONRPCError
	if !errors.As(err, &rpcErr) || rpcErr == nil || rpcErr.Code == 3 || hasRevertData(rpcErr.Data) {
		return false
	}
	return outOfGasMessage.MatchString(strings.TrimSpace(rpcErr.Message))
}

// outOfGasMessage matches the start of a node's out-of-gas message, as
// mppx's isOutOfGasError does.
var outOfGasMessage = regexp.MustCompile(`(?i)^(?:out of gas(?::|$)|intrinsic gas too low(?::|$))`)

// hasRevertData reports whether a JSON-RPC error's data field carries
// anything, such as revert data; an empty value or "0x" carries nothing.
func hasRevertData(data any) bool {
	switch value := data.(type) {
	case nil:
		return false
	case string:
		return value != "" && value != "0x"
	default:
		return true
	}
}

// rpcSignature renders a secp256k1 signature as JSON-RPC {r, s, yParity}
// hex quantities, a yParity of 0 as "0x0".
func rpcSignature(signature *temposigner.Signature) map[string]any {
	return map[string]any{
		"r":       hexutil.EncodeBig(signature.R),
		"s":       hexutil.EncodeBig(signature.S),
		"yParity": hexutil.EncodeUint64(uint64(signature.YParity)),
	}
}

func simulateTransactionExecution(ctx context.Context, rpc tempo.RPCClient, tx *tempotx.Tx) error {
	callObject, err := transactionCallObject(tx)
	if err != nil {
		return mpp.ErrVerificationFailed("transaction preflight failed")
	}
	return simulateCall(ctx, rpc, callObject)
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

// transactionCallObject renders tx as an eth_call request without its
// signatures (simulateSponsoredTransactionExecution adds the fee payer's), with its keyAuthorization as viem formats it (ox
// KeyAuthorization.toRpc), as mppx simulates a transaction.
func transactionCallObject(tx *tempotx.Tx, senderTransactions ...*tempotx.Tx) (map[string]any, error) {
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
	// A simulation-only gas adjustment invalidates the original signature.
	// Recover the key from the actual sender-signed transaction, never that copy.
	senderTx := tx
	if len(senderTransactions) > 0 {
		senderTx = senderTransactions[0]
	}
	keyID, err := transactionAccessKey(senderTx)
	if err != nil {
		return nil, err
	}
	if keyID != nil {
		callObject["keyId"] = keyID.Hex()
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
	if tx.KeyAuthorization != nil {
		keyAuthorization, err := keyAuthorizationRPC(tx.KeyAuthorization)
		if err != nil {
			return nil, err
		}
		callObject["keyAuthorization"] = keyAuthorization
	}
	return callObject, nil
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

// resolveFeePayerPolicies returns the per-token sponsorship policies and the
// ordered fee tokens a local fee payer may pay gas in (nil for the per-chain
// defaults) from AllowedFeeTokens and FeePayerPolicy.
func resolveFeePayerPolicies(config IntentConfig) (map[string]FeePayerPolicy, []common.Address, error) {
	policy := resolveSponsorPolicy(config)
	if config.AllowedFeeTokens == nil {
		configured := defaultFeePayerPolicies()
		for token := range configured {
			configured[token] = policy
		}
		policies, err := normalizeFeePayerPolicies(configured)
		return policies, nil, err
	}
	configured := make(map[string]FeePayerPolicy, len(config.AllowedFeeTokens))
	feeTokens := make([]common.Address, 0, len(config.AllowedFeeTokens))
	for _, token := range config.AllowedFeeTokens {
		if !common.IsHexAddress(token) {
			return nil, nil, fmt.Errorf("tempo server: invalid allowed fee token %q", token)
		}
		address := common.HexToAddress(token)
		if slices.Contains(feeTokens, address) {
			continue
		}
		feeTokens = append(feeTokens, address)
		configured[address.Hex()] = policy
	}
	if len(feeTokens) == 0 {
		return nil, nil, fmt.Errorf("tempo server: AllowedFeeTokens must contain at least one fee token")
	}
	policies, err := normalizeFeePayerPolicies(configured)
	if err != nil {
		return nil, nil, err
	}
	return policies, feeTokens, nil
}

// resolveSponsorPolicy returns the sponsorship policy FeePayerPolicy sets
// over the defaults; every fee token shares it.
func resolveSponsorPolicy(config IntentConfig) FeePayerPolicy {
	policy := defaultFeePayerPolicy()
	if config.FeePayerPolicy != nil {
		if config.FeePayerPolicy.MaxFeePerGas != nil {
			policy.MaxFeePerGas = new(big.Int).Set(config.FeePayerPolicy.MaxFeePerGas)
		}
		if config.FeePayerPolicy.MaxPriorityFeePerGas != nil {
			policy.MaxPriorityFeePerGas = new(big.Int).Set(config.FeePayerPolicy.MaxPriorityFeePerGas)
		} else if config.FeePayerPolicy.MaxFeePerGas != nil && policy.MaxPriorityFeePerGas.Cmp(config.FeePayerPolicy.MaxFeePerGas) > 0 {
			// A priority fee can never exceed the max fee, so lower the
			// default to a MaxFeePerGas set below it.
			policy.MaxPriorityFeePerGas = new(big.Int).Set(config.FeePayerPolicy.MaxFeePerGas)
		}
		if config.FeePayerPolicy.MaxTotalFee != nil {
			policy.MaxTotalFee = new(big.Int).Set(config.FeePayerPolicy.MaxTotalFee)
		}
		policy.RejectKeyAuthorization = config.FeePayerPolicy.RejectKeyAuthorization
	}
	return policy
}

// usesHostedFeePayer reports whether a hosted fee payer, not a local signer,
// sponsors this verifier's charges.
func (i *Intent) usesHostedFeePayer() bool {
	return i.feePayerSigner == nil && i.hostedFeePayer != nil
}

// sponsorPolicyFor returns the sponsorship policy for a fee token. A hosted
// fee payer without an AllowedFeeTokens list may pick any fee token, which
// then gets the shared policy, as does a transaction it has not filled yet.
func (i *Intent) sponsorPolicyFor(feeToken common.Address) (FeePayerPolicy, error) {
	// Before the hosted fee payer fills the fee token, every allowed token
	// shares this policy; the filled token is checked after co-signing.
	if i.usesHostedFeePayer() && (i.feeTokens == nil || feeToken == (common.Address{})) {
		policy := i.sponsorPolicy
		return FeePayerPolicy{
			MaxFeePerGas:           new(big.Int).Set(policy.MaxFeePerGas),
			MaxPriorityFeePerGas:   new(big.Int).Set(policy.MaxPriorityFeePerGas),
			MaxTotalFee:            new(big.Int).Set(policy.MaxTotalFee),
			RejectKeyAuthorization: policy.RejectKeyAuthorization,
		}, nil
	}
	return i.feePayerPolicyFor(feeToken.Hex())
}

// sponsors reports whether the verifier has a fee payer, local or hosted.
func (i *Intent) sponsors() bool {
	return i != nil && (i.feePayerSigner != nil || i.hostedFeePayer != nil)
}

func normalizeFeePayerPolicies(configured map[string]FeePayerPolicy) (map[string]FeePayerPolicy, error) {
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
			MaxFeePerGas:           new(big.Int).Set(policy.MaxFeePerGas),
			MaxPriorityFeePerGas:   new(big.Int).Set(policy.MaxPriorityFeePerGas),
			MaxTotalFee:            new(big.Int).Set(policy.MaxTotalFee),
			RejectKeyAuthorization: policy.RejectKeyAuthorization,
		}
	}
	return policies, nil
}

func defaultFeePayerPolicies() map[string]FeePayerPolicy {
	policy := defaultFeePayerPolicy()
	return map[string]FeePayerPolicy{
		"0x20c0000000000000000000000000000000000000": policy,
		tempotx.AlphaUSDAddress.Hex():                policy,
		tempo.USDCEAddress:                           policy,
	}
}

func defaultFeePayerPolicy() FeePayerPolicy {
	return FeePayerPolicy{
		MaxFeePerGas:         new(big.Int).Set(feePayerMaxFeePerGas),
		MaxPriorityFeePerGas: new(big.Int).Set(feePayerMaxPriorityFeePerGas),
		MaxTotalFee:          new(big.Int).Set(feePayerMaxTotalFee),
	}
}

// allowedFeeTokens returns the ordered fee tokens a local fee payer may pay
// gas in: the configured AllowedFeeTokens, or mppx's defaults of
// pathUSD plus the chain's default currency (USDC.e on mainnet), limited to
// tokens with a fee-payer policy.
func (i *Intent) allowedFeeTokens(ctx context.Context, rpc tempo.RPCClient, request tempo.ChargeRequest) ([]common.Address, error) {
	if i.feeTokens != nil {
		return i.feeTokens, nil
	}
	chainID, err := chargeChainID(ctx, rpc, request)
	if err != nil {
		return nil, err
	}
	candidates := []common.Address{common.HexToAddress(tempo.PathUSDAddress)}
	if chainID == tempotx.ChainIdMainnet {
		candidates = append(candidates, common.HexToAddress(tempo.USDCEAddress))
	}
	allowed := make([]common.Address, 0, len(candidates))
	for _, token := range candidates {
		if _, ok := i.feePayerPolicy[token.Hex()]; ok {
			allowed = append(allowed, token)
		}
	}
	if len(allowed) == 0 {
		return nil, mpp.ErrInvalidPayload("fee payer transaction fee token is not supported")
	}
	return allowed, nil
}

// resolveFeeToken picks the local fee payer's fee token: the configured
// FeeToken, else the first allowed token the fee payer holds a balance of,
// else the first allowed token. The result must be allowed.
func (i *Intent) resolveFeeToken(ctx context.Context, rpc tempo.RPCClient, allowed []common.Address) (common.Address, error) {
	if i.feeToken != (common.Address{}) {
		if !slices.Contains(allowed, i.feeToken) {
			return common.Address{}, mpp.ErrInvalidPayload("fee payer transaction fee token is not supported")
		}
		return i.feeToken, nil
	}
	account := i.feePayerSigner.Address()
	for _, token := range allowed {
		if hasTokenBalance(ctx, rpc, token, account) {
			return token, nil
		}
	}
	return allowed[0], nil
}

// coSignWithFeeToken co-signs a copy of the sender-signed transaction with
// the local fee payer, paying gas in feeToken, and returns it with its
// canonical broadcast encoding and hash.
func (i *Intent) coSignWithFeeToken(senderSigned tempotx.Tx, feeToken common.Address) (*tempotx.Tx, string, string, error) {
	tx := &senderSigned
	tx.FeeToken = feeToken
	tx.AwaitingFeePayer = false
	tx.FeePayerSignature = nil
	if err := tempotx.AddFeePayerSignature(tx, i.feePayerSigner); err != nil {
		return nil, "", "", err
	}
	serialized, err := tempotx.Serialize(tx, nil)
	if err != nil {
		return nil, "", "", err
	}
	serialized, err = canonicalBroadcastTransaction(serialized)
	if err != nil {
		return nil, "", "", err
	}
	hash, err := tempotx.ComputeHash(serialized)
	if err != nil {
		return nil, "", "", err
	}
	return tx, serialized, hash.Hex(), nil
}

// clock returns i.now() when set, else time.Now().
func (i *Intent) clock() time.Time {
	if i.now != nil {
		return i.now()
	}
	return time.Now()
}

// hasTokenBalance reports whether account holds a nonzero TIP-20 balance.
// Lookup failures count as no balance.
func hasTokenBalance(ctx context.Context, rpc tempo.RPCClient, token, account common.Address) bool {
	response, err := rpc.SendRequest(ctx, "eth_call", map[string]any{
		"to":   token.Hex(),
		"data": "0x" + balanceOfSelector + addressToWord(account),
	}, "latest")
	if err != nil || response.CheckError() != nil {
		return false
	}
	result, ok := response.Result.(string)
	if !ok {
		return false
	}
	balance, err := hexutil.DecodeBig(normalizeHexQuantity(result))
	return err == nil && balance.Sign() > 0
}

// normalizeHexQuantity strips leading zeros from a 32-byte eth_call word so
// it decodes as a hex quantity.
func normalizeHexQuantity(value string) string {
	trimmed := strings.TrimLeft(strings.TrimPrefix(strings.TrimPrefix(value, "0x"), "0X"), "0")
	if trimmed == "" {
		return "0x0"
	}
	return "0x" + trimmed
}

func chargeChainID(ctx context.Context, rpc tempo.RPCClient, request tempo.ChargeRequest) (int64, error) {
	if request.MethodDetails.ChainID != nil {
		return *request.MethodDetails.ChainID, nil
	}
	chainID, err := rpc.GetChainID(ctx)
	if err != nil {
		return 0, mpp.ErrVerificationFailed("failed to resolve chain id")
	}
	return int64(chainID), nil
}

func (i *Intent) feePayerPolicyFor(currency string) (FeePayerPolicy, error) {
	policy, ok := i.feePayerPolicy[common.HexToAddress(currency).Hex()]
	if !ok {
		return FeePayerPolicy{}, mpp.ErrInvalidPayload("fee payer transaction fee token is not supported")
	}
	return FeePayerPolicy{
		MaxFeePerGas:           new(big.Int).Set(policy.MaxFeePerGas),
		MaxPriorityFeePerGas:   new(big.Int).Set(policy.MaxPriorityFeePerGas),
		MaxTotalFee:            new(big.Int).Set(policy.MaxTotalFee),
		RejectKeyAuthorization: policy.RejectKeyAuthorization,
	}, nil
}

func validateFeePayerTransaction(tx *tempotx.Tx, challengeExpires string, policy FeePayerPolicy) error {
	if tx.KeyAuthorization != nil && policy.RejectKeyAuthorization {
		return mpp.ErrInvalidPayload("fee payer transaction keyAuthorization is not allowed")
	}
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
		if tx.ValidBefore > uint64(maxValidBefore) {
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
	return mpp.ParseExpires(value)
}

func asString(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	default:
		return fmt.Sprintf("%v", value)
	}
}

// legacySponsoredChallengeStoreKey returns the reservation key sponsored
// charges used before they were keyed by the payer-signed transaction
// (tempo.ChargeSponsoredTransactionStoreKey). Verification still rejects a
// Challenge reserved under it.
func legacySponsoredChallengeStoreKey(challengeID string) string {
	return tempo.ReplayKeyPrefix + "sponsor:" + challengeID
}
