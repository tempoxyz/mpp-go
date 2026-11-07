package chargeclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
	temporpc "github.com/tempoxyz/tempo-go/pkg/client"
	temposigner "github.com/tempoxyz/tempo-go/pkg/signer"
	tempotx "github.com/tempoxyz/tempo-go/pkg/transaction"

	"github.com/tempoxyz/mpp-go/pkg/mpp"
	"github.com/tempoxyz/mpp-go/pkg/tempo"
	"github.com/tempoxyz/mpp-go/pkg/tempo/internal/txsend"
	"github.com/tempoxyz/mpp-go/pkg/tempo/internal/zone"
)

// zoneSubmissionMargin is the time the method allows for sending a Zone
// withdrawal before it waits for settlement.
const zoneSubmissionMargin = 15 * time.Second

// zoneReceiptTimeout bounds the wait for a Zone withdrawal's receipt,
// including after its send response fails.
const zoneReceiptTimeout = 15 * time.Second

// zoneReceiptPollInterval is the time between polls for a Zone receipt.
const zoneReceiptPollInterval = 500 * time.Millisecond

// zoneReceiptLookupTimeout bounds the one receipt lookup after the Zone
// answers a send with an error.
const zoneReceiptLookupTimeout = 5 * time.Second

// zoneWithdrawalGas is the gas limit of a Zone withdrawal transaction, as
// viem's Actions.zone.requestWithdrawal.
const zoneWithdrawalGas = 10_000_000

// approveSelector is the TIP-20 approve(address,uint256) selector.
const approveSelector = "095ea7b3"

// ZoneSettlementPendingError reports a Zone withdrawal that paid a challenge
// but did not settle on Tempo before the method stopped waiting. The
// withdrawal cannot be undone and may still settle: pass Challenge to the same
// Method's CreateCredential to resume waiting without withdrawing again, or
// find the settlement by SenderTag on the Zone's portal. A new request gets a
// new challenge, which the method would pay with a new withdrawal.
type ZoneSettlementPendingError struct {
	// Challenge is the challenge the withdrawal pays.
	Challenge *mpp.Challenge
	// ChallengeID is the ID of the challenge the withdrawal pays.
	ChallengeID string
	// SenderTag identifies the withdrawal in the portal's
	// WithdrawalProcessed event.
	SenderTag string
	// ZoneID is the Zone the withdrawal left.
	ZoneID uint32
	// Err is the error that ended the wait.
	Err error
}

// Error implements error.
func (e *ZoneSettlementPendingError) Error() string {
	return fmt.Sprintf("tempo client: Zone %d withdrawal %s for challenge %q has not settled on Tempo yet; retry the challenge to resume waiting instead of paying again",
		e.ZoneID, e.SenderTag, e.ChallengeID)
}

// Unwrap returns the error that ended the wait.
func (e *ZoneSettlementPendingError) Unwrap() error {
	return e.Err
}

// ZoneWithdrawalUnknownError reports a Zone withdrawal for a challenge that
// may have been sent but whose receipt the method could not find. Passing
// Challenge to the same Method's CreateCredential returns this error again
// instead of withdrawing twice; find the transaction by TransactionHash on the
// Zone before paying the challenge another way.
type ZoneWithdrawalUnknownError struct {
	// Challenge is the challenge the withdrawal would pay.
	Challenge *mpp.Challenge
	// TransactionHash is the hash of the signed withdrawal transaction.
	TransactionHash string
	// ZoneID is the Zone the withdrawal would leave.
	ZoneID uint32
	// Err is the send error, if any.
	Err error
}

// Error implements error.
func (e *ZoneWithdrawalUnknownError) Error() string {
	message := fmt.Sprintf("tempo client: Zone withdrawal outcome is unknown: transaction %s may have been sent; retrying this challenge does not withdraw again", e.TransactionHash)
	if e.Err != nil {
		message += ": " + e.Err.Error()
	}
	return message
}

// Unwrap returns the send error.
func (e *ZoneWithdrawalUnknownError) Unwrap() error {
	return e.Err
}

// zoneWithdrawal is a Zone withdrawal sent for a challenge: its sender tag
// once its receipt shows it, or the error that left its outcome unknown.
type zoneWithdrawal struct {
	senderTag common.Hash
	err       error
}

// resolveZone validates Config.Zone, rejecting options that would fail only
// after an irreversible withdrawal.
func resolveZone(config *Zone, signer *temposigner.Signer, accessKey bool) (*zoneConfig, error) {
	if config == nil {
		return nil, nil
	}
	if config.RPC == nil {
		return nil, fmt.Errorf("tempo client: Zone requires an RPC client")
	}
	if config.ChainID <= 0 {
		return nil, fmt.Errorf("tempo client: Zone requires its ChainID")
	}
	if config.SettlementTimeout < 0 {
		return nil, fmt.Errorf("tempo client: Zone.SettlementTimeout must not be negative")
	}
	resolved := &zoneConfig{
		rpc:               config.RPC,
		chainID:           config.ChainID,
		id:                config.ZoneID,
		parentChainID:     config.ParentChainID,
		signer:            config.Signer,
		settlementTimeout: config.SettlementTimeout,
	}
	if resolved.settlementTimeout == 0 {
		resolved.settlementTimeout = zone.DefaultSettlementTimeout
	}
	if resolved.signer == nil {
		if accessKey {
			return nil, fmt.Errorf("tempo client: Zone requires a Signer with AccessKey")
		}
		resolved.signer = signer
	}
	// Only chain IDs that follow a known Zone scheme name their Zone.
	parentChainID, known := zone.ParentChainIDFromChainID(config.ChainID)
	if known {
		derived, err := zone.IDFromChainID(config.ChainID)
		if err != nil {
			return nil, fmt.Errorf("tempo client: %w", err)
		}
		if resolved.id == 0 {
			resolved.id = derived
		} else if resolved.id != derived {
			return nil, fmt.Errorf("tempo client: Zone.ZoneID %d does not match Zone %d of chain %d", resolved.id, derived, config.ChainID)
		}
		if resolved.parentChainID == 0 {
			resolved.parentChainID = parentChainID
		}
	}
	if resolved.id == 0 {
		return nil, fmt.Errorf("tempo client: cannot derive a Zone ID from chain ID %d; set Zone.ZoneID", config.ChainID)
	}
	if resolved.parentChainID == 0 {
		return nil, fmt.Errorf("tempo client: cannot derive the parent chain of Zone chain %d; set Zone.ParentChainID", config.ChainID)
	}
	return resolved, nil
}

// paysFromZone reports whether to pay a nonzero charge from the configured
// Zone: the challenge is on the Zone's parent chain, allows push payments,
// accepts the Zone and has no splits; Config.CredentialType asks for no other
// mode; and a new withdrawal can settle before the challenge expires, since an
// expired challenge would invite another payment. A challenge with a
// withdrawal already sent always resumes it.
func (m *Method) paysFromZone(challenge *mpp.Challenge, request tempo.ChargeRequest, chainID int64) bool {
	if m.zone == nil || request.Amount == "0" || m.zone.parentChainID != chainID {
		return false
	}
	if m.credentialType != "" && m.credentialType != tempo.CredentialTypeHash {
		return false
	}
	if !request.Allows(tempo.CredentialTypeZone) ||
		!tempo.ZoneAccepted(request.MethodDetails.Zones, m.zone.id) ||
		len(request.MethodDetails.Splits) > 0 {
		return false
	}
	if m.pendingZoneWithdrawal(zoneWithdrawalKey(challenge)) != nil {
		return true
	}
	return m.settlesBeforeExpiry(challenge.Expires)
}

// settlesBeforeExpiry reports whether a Zone withdrawal can be sent and
// waited on for the whole settlement timeout before expires.
func (m *Method) settlesBeforeExpiry(expires string) bool {
	if expires == "" {
		return true
	}
	expiry, err := mpp.ParseExpires(expires)
	if err != nil {
		return false
	}
	return expiry.Sub(m.clock()) > m.zone.settlementTimeout+zoneSubmissionMargin
}

// settlementTimeout returns how long to wait for settlement: the configured
// timeout, cut short at expires.
func (m *Method) settlementTimeout(expires string) time.Duration {
	timeout := m.zone.settlementTimeout
	if expires == "" {
		return timeout
	}
	expiry, err := mpp.ParseExpires(expires)
	if err != nil {
		return 0
	}
	return max(0, min(timeout, expiry.Sub(m.clock())))
}

// zoneWithdrawalKey keys a Zone withdrawal by its challenge's echo: the ID
// and every field its HMAC binds, so a challenge from another realm or
// request with the same ID never matches, while fields outside the binding,
// such as the description, cannot make the same challenge miss its pending
// withdrawal and pay again.
func zoneWithdrawalKey(challenge *mpp.Challenge) string {
	encoded, err := json.Marshal(challenge.ToEcho())
	if err != nil {
		return ""
	}
	return string(encoded)
}

func (m *Method) pendingZoneWithdrawal(key string) *zoneWithdrawal {
	m.zoneMu.Lock()
	defer m.zoneMu.Unlock()
	return m.zoneWithdrawals[key]
}

func (m *Method) setZoneWithdrawal(key string, withdrawal *zoneWithdrawal) {
	m.zoneMu.Lock()
	defer m.zoneMu.Unlock()
	if withdrawal == nil {
		delete(m.zoneWithdrawals, key)
		return
	}
	if m.zoneWithdrawals == nil {
		m.zoneWithdrawals = map[string]*zoneWithdrawal{}
	}
	m.zoneWithdrawals[key] = withdrawal
}

// createZoneCredential pays a challenge from the Zone balance: it requests a
// withdrawal to the recipient with the challenge-bound memo, or resumes the
// one already sent for the challenge, waits for it to settle on Tempo, and
// returns a zone credential naming it by sender tag and the Tempo
// transaction that settled it. CreateCredential runs one call per challenge
// at a time, so calls never send two withdrawals for one challenge.
func (m *Method) createZoneCredential(ctx context.Context, rpc tempo.RPCClient, challenge *mpp.Challenge, request tempo.ChargeRequest) (*mpp.Credential, error) {
	key := zoneWithdrawalKey(challenge)
	pending := m.pendingZoneWithdrawal(key)
	if pending == nil {
		// Refuse a credential too large to send before withdrawing: the
		// settlement hash and sender tag are 32 bytes each.
		placeholder := "0x" + strings.Repeat("0", 64)
		if _, err := mpp.FormatCredential(zoneCredential(challenge, placeholder, placeholder, m.zone.id)); err != nil {
			return nil, err
		}
		senderTag, sent, err := m.requestZoneWithdrawal(ctx, challenge, request)
		if err != nil {
			// Only a failure before the transaction was sent, or a revert,
			// is safe to retry. After that, the withdrawal may have gone
			// through, so retries of this challenge keep failing.
			if sent {
				m.setZoneWithdrawal(key, &zoneWithdrawal{err: err})
			}
			return nil, err
		}
		pending = &zoneWithdrawal{senderTag: senderTag}
		m.setZoneWithdrawal(key, pending)
	}
	if pending.err != nil {
		return nil, pending.err
	}

	recipient := common.HexToAddress(request.Recipient)
	hash, err := zone.WaitForSettlement(ctx, rpc, zone.PortalAddress(m.zone.id), pending.senderTag, &recipient, m.settlementTimeout(challenge.Expires))
	if err != nil {
		return nil, &ZoneSettlementPendingError{
			Challenge:   challenge,
			ChallengeID: challenge.ID,
			SenderTag:   pending.senderTag.Hex(),
			ZoneID:      m.zone.id,
			Err:         err,
		}
	}
	credential := zoneCredential(challenge, common.HexToHash(hash).Hex(), pending.senderTag.Hex(), m.zone.id)
	if _, err := mpp.FormatCredential(credential); err != nil {
		return nil, err
	}
	m.setZoneWithdrawal(key, nil)
	return credential, nil
}

// zoneCredential returns the zone credential paying challenge.
func zoneCredential(challenge *mpp.Challenge, hash, senderTag string, zoneID uint32) *mpp.Credential {
	return &mpp.Credential{
		Challenge: challenge.ToEcho(),
		Payload: tempo.ChargeCredentialPayload{
			Type:      tempo.CredentialTypeZone,
			Hash:      hash,
			SenderTag: senderTag,
			ZoneID:    zoneID,
		}.Map(),
	}
}

// requestZoneWithdrawal withdraws the challenge amount from the Zone to its
// recipient, bound to the challenge by its memo, and returns the
// withdrawal's sender tag. sent reports whether the transaction may have
// reached the Zone: a failure after that leaves the withdrawal's outcome
// unknown, except a revert, which moved nothing.
func (m *Method) requestZoneWithdrawal(ctx context.Context, challenge *mpp.Challenge, request tempo.ChargeRequest) (common.Hash, bool, error) {
	rpc := m.zone.rpc
	signer := m.zone.signer
	amount, ok := new(big.Int).SetString(request.Amount, 10)
	if !ok {
		return common.Hash{}, false, fmt.Errorf("tempo client: invalid amount %q", request.Amount)
	}
	currency := common.HexToAddress(request.Currency)
	recipient := common.HexToAddress(request.Recipient)
	memo := common.HexToHash(tempo.EncodeAttribution(challenge.Realm, m.clientID, challenge.ID))

	// The outbox pulls the amount plus its withdrawal fee.
	fee, err := zoneWithdrawalFee(ctx, rpc)
	if err != nil {
		return common.Hash{}, false, err
	}
	approve := fmt.Sprintf("0x%s%064x%064x", approveSelector, zone.OutboxAddress.Big(), new(big.Int).Add(amount, fee))
	// A bounced payout refunds the payer, not the payee's Zone address.
	withdrawal, err := zone.OutboxABI.Pack("requestWithdrawal", currency, recipient, amount, [32]byte(memo), uint64(0), signer.Address(), []byte{}, []byte{})
	if err != nil {
		return common.Hash{}, false, fmt.Errorf("tempo client: encode Zone withdrawal: %w", err)
	}
	// Prepare and sign first: a failure there sent nothing, so the challenge
	// may be retried.
	gasPrice, err := m.gasPrice(ctx, rpc)
	if err != nil {
		return common.Hash{}, false, err
	}
	nonce, err := rpc.GetTransactionCount(ctx, signer.Address().Hex())
	if err != nil {
		return common.Hash{}, false, fmt.Errorf("tempo client: get Zone nonce: %w", err)
	}
	// Gas is paid in the configured fee token; the withdrawal itself always
	// approves and moves the charge currency.
	feeToken := currency
	if m.feeToken != nil {
		feeToken = *m.feeToken
	}
	tx, err := tempotx.NewBuilder(big.NewInt(m.zone.chainID)).
		SetMaxFeePerGas(gasPrice).
		SetMaxPriorityFeePerGas(new(big.Int).Set(gasPrice)).
		SetGas(zoneWithdrawalGas).
		SetNonceKey(big.NewInt(0)).
		SetNonce(nonce).
		SetFeeToken(feeToken).
		AddCall(currency, big.NewInt(0), common.FromHex(approve)).
		AddCall(zone.OutboxAddress, big.NewInt(0), withdrawal).
		BuildAndValidate()
	if err != nil {
		return common.Hash{}, false, fmt.Errorf("tempo client: build Zone withdrawal: %w", err)
	}
	if err := tempotx.SignTransaction(tx, signer); err != nil {
		return common.Hash{}, false, fmt.Errorf("tempo client: sign Zone withdrawal: %w", err)
	}
	serialized, err := tempotx.Serialize(tx, nil)
	if err != nil {
		return common.Hash{}, false, fmt.Errorf("tempo client: serialize Zone withdrawal: %w", err)
	}
	serialized, err = canonicalSignedTransaction(serialized)
	if err != nil {
		return common.Hash{}, false, fmt.Errorf("tempo client: serialize Zone withdrawal: %w", err)
	}
	// The signed transaction fixes its hash, so a lost send response can
	// still be recovered from the Zone.
	hash := crypto.Keccak256Hash(common.FromHex(serialized)).Hex()

	var receipt map[string]any
	if sentHash, sendErr := rpc.SendRawTransaction(ctx, serialized); sendErr == nil && sentHash != "" {
		hash = sentHash
	} else if sendErr != nil {
		if zoneSendRejected(sendErr) {
			// The Zone answered with a JSON-RPC error, but a node can answer
			// a send it already took with one too, such as "nonce too low"
			// once it included the transaction: look for its receipt once
			// before deciding nothing was withdrawn.
			receipt = lookupZoneReceipt(ctx, rpc, hash)
			if receipt == nil {
				return common.Hash{}, false, fmt.Errorf("tempo client: send Zone withdrawal: %w", sendErr)
			}
		}
		err = sendErr
	}
	if receipt == nil {
		receipt = m.waitForZoneReceipt(ctx, rpc, hash)
	}
	if receipt == nil {
		return common.Hash{}, true, &ZoneWithdrawalUnknownError{
			Challenge:       challenge,
			TransactionHash: hash,
			ZoneID:          m.zone.id,
			Err:             err,
		}
	}
	if !strings.EqualFold(stringValue(receipt["status"]), "0x1") {
		// A reverted withdrawal moved nothing, so the challenge may be retried.
		return common.Hash{}, false, fmt.Errorf("tempo client: Zone withdrawal transaction %s reverted", hash)
	}
	sender, fallbackNonce, ok := zone.RequestedEvent(receipt)
	if !ok {
		return common.Hash{}, true, fmt.Errorf("tempo client: Zone withdrawal request did not emit WithdrawalRequested")
	}
	transactionHash := hash
	if receiptHash := stringValue(receipt["transactionHash"]); receiptHash != "" {
		transactionHash = receiptHash
	}
	return zone.SenderTag(sender, common.HexToHash(transactionHash), fallbackNonce), true, nil
}

// zoneSendRejected reports whether a send error is the node's JSON-RPC
// rejection of the transaction, rather than a transport failure or timeout
// that leaves it unknown whether the node accepted it. A rejection as a
// duplicate means the node already holds the transaction.
func zoneSendRejected(err error) bool {
	var rpcErr *temporpc.JSONRPCError
	return errors.As(err, &rpcErr) && !txsend.IsDuplicateSubmission(err)
}

// lookupZoneReceipt checks once, without polling, for a transaction's
// receipt, even when ctx has ended, and returns nil when there is none.
func lookupZoneReceipt(ctx context.Context, rpc tempo.RPCClient, hash string) map[string]any {
	lookupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), zoneReceiptLookupTimeout)
	defer cancel()
	response, err := rpc.SendRequest(lookupCtx, "eth_getTransactionReceipt", hash)
	if err != nil || response.CheckError() != nil {
		return nil
	}
	if receipt, ok := response.Result.(map[string]any); ok && len(receipt) > 0 {
		return receipt
	}
	return nil
}

// waitForZoneReceipt polls the Zone for a transaction's receipt until
// zoneReceiptTimeout and returns nil when none appears.
func (m *Method) waitForZoneReceipt(ctx context.Context, rpc tempo.RPCClient, hash string) map[string]any {
	timeout := m.zoneReceiptTimeout
	if timeout == 0 {
		timeout = zoneReceiptTimeout
	}
	deadline := time.Now().Add(timeout)
	for {
		response, err := rpc.SendRequest(ctx, "eth_getTransactionReceipt", hash)
		if err == nil && response.CheckError() == nil {
			if receipt, ok := response.Result.(map[string]any); ok && len(receipt) > 0 {
				return receipt
			}
		}
		if !time.Now().Add(zoneReceiptPollInterval).Before(deadline) {
			return nil
		}
		timer := time.NewTimer(zoneReceiptPollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
	}
}

// zoneWithdrawalFee reads the ZoneOutbox fee of a withdrawal without a
// callback.
func zoneWithdrawalFee(ctx context.Context, rpc tempo.RPCClient) (*big.Int, error) {
	data, err := zone.OutboxABI.Pack("calculateWithdrawalFee", uint64(0))
	if err != nil {
		return nil, err
	}
	response, err := rpc.SendRequest(ctx, "eth_call", map[string]any{
		"to":   zone.OutboxAddress.Hex(),
		"data": hexutil.Encode(data),
	}, "latest")
	if err != nil {
		return nil, fmt.Errorf("tempo client: read Zone withdrawal fee: %w", err)
	}
	if err := response.CheckError(); err != nil {
		return nil, fmt.Errorf("tempo client: read Zone withdrawal fee: %w", err)
	}
	result, ok := response.Result.(string)
	if !ok {
		return nil, fmt.Errorf("tempo client: unexpected eth_call result %T", response.Result)
	}
	return tempo.ParseHexBigInt(result)
}

// canonicalSignedTransaction rewrites the recovery ID of a transaction's
// secp256k1 signature, its last byte, from 0/1 to 27/28. Tempo nodes encode
// signatures that way when deriving the transaction hash (see the charge
// server's canonicalBroadcastTransaction), so sending this encoding makes its
// keccak256 the hash the Zone reports.
func canonicalSignedTransaction(serialized string) (string, error) {
	raw, err := hexutil.Decode(serialized)
	if err != nil {
		return "", err
	}
	if len(raw) == 0 {
		return "", errors.New("empty transaction")
	}
	if last := raw[len(raw)-1]; last <= 1 {
		raw[len(raw)-1] = last + 27
	}
	return hexutil.Encode(raw), nil
}

func stringValue(value any) string {
	text, _ := value.(string)
	return text
}
