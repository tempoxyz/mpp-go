package chargeclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"strconv"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
	temposigner "github.com/tempoxyz/tempo-go/pkg/signer"
	tempotx "github.com/tempoxyz/tempo-go/pkg/transaction"

	"github.com/tempoxyz/mpp-go/pkg/mpp"
	"github.com/tempoxyz/mpp-go/pkg/tempo"
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
// Challenge to the same Method's CreateCredential looks for the transaction's
// receipt again without withdrawing twice: once it shows the withdrawal, the
// call resumes it and waits for settlement; once it shows a revert, the
// challenge may be paid again; while it is missing, the call returns this
// error again. Find the transaction by TransactionHash on the Zone before
// paying the challenge another way.
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

// ZoneWithdrawalBouncedError reports a Zone withdrawal for a challenge that
// settled on Tempo but whose payout bounced: the portal's WithdrawalProcessed
// event reports callbackSuccess false, so the amount went back to the payer's
// Zone balance and the challenge is unpaid, while the withdrawal's fees are
// spent. The server would reject a credential naming it, so none is created.
// Paying the challenge again with the same Method withdraws again; callers
// choosing among several offers should not fall back to another one on this
// error, since each attempt spends another withdrawal's fees.
type ZoneWithdrawalBouncedError struct {
	// ChallengeID is the ID of the challenge the withdrawal paid.
	ChallengeID string
	// SenderTag identifies the withdrawal in the portal's
	// WithdrawalProcessed event.
	SenderTag string
	// ZoneID is the Zone the withdrawal left.
	ZoneID uint32
}

// Error implements error.
func (e *ZoneWithdrawalBouncedError) Error() string {
	return fmt.Sprintf("tempo client: Zone %d withdrawal %s for challenge %q bounced on Tempo: its amount was returned to the payer's Zone balance",
		e.ZoneID, e.SenderTag, e.ChallengeID)
}

// ErrZoneChallengeExpiresTooSoon reports a challenge that, once its Zone
// withdrawal is signed and ready to send, expires too soon for the withdrawal
// to settle on Tempo before it: the authorization, fee, gas price and nonce
// lookups took the time the check before them allowed. The method sends
// nothing, so the challenge may be paid another way.
var ErrZoneChallengeExpiresTooSoon = errors.New("tempo client: challenge expires too soon for a Zone withdrawal to settle")

// ErrZoneWithdrawalFeeTooHigh reports a Zone withdrawal fee above
// Zone.MaxWithdrawalFee: the method sends nothing, so the challenge may be
// paid another way.
var ErrZoneWithdrawalFeeTooHigh = errors.New("tempo client: Zone withdrawal fee too high")

// ErrZoneDeadlineTooSoon reports a request context whose deadline is too
// close for a Zone withdrawal to be sent and its receipt found: none is sent.
var ErrZoneDeadlineTooSoon = errors.New("tempo client: request deadline too soon to send a Zone withdrawal")

// zoneWithdrawal is a Zone withdrawal for a challenge. While it is being
// sent, done is open and calls for the same withdrawal key wait on it. Once
// done is closed, sent reports whether the transaction may have reached the
// Zone; if so, senderTag holds its sender tag once its receipt showed it, or
// err the error that left its outcome unknown. hash is the signed
// transaction's hash, kept so a retry can look for a receipt that was missing.
// settlementStart, guarded by Method.zoneMu, is the Tempo block the first
// settlement wait searched from, so a resumed wait searches from it too.
type zoneWithdrawal struct {
	done            chan struct{}
	sent            bool
	hash            string
	senderTag       common.Hash
	err             error
	settlementStart *uint64
}

// unknown reports whether a finished withdrawal's receipt was missing, so a
// retry may look for it again.
func (w *zoneWithdrawal) unknown() bool {
	var unknown *ZoneWithdrawalUnknownError
	return w.sent && w.hash != "" && errors.As(w.err, &unknown)
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
	if config.MaxWithdrawalFee != nil && config.MaxWithdrawalFee.Sign() < 0 {
		return nil, fmt.Errorf("tempo client: Zone.MaxWithdrawalFee must not be negative")
	}
	resolved := &zoneConfig{
		rpc:               config.RPC,
		chainID:           config.ChainID,
		id:                config.ZoneID,
		parentChainID:     config.ParentChainID,
		signer:            config.Signer,
		settlementTimeout: config.SettlementTimeout,
		nonceLock:         make(chan struct{}, 1),
	}
	if config.MaxWithdrawalFee != nil {
		resolved.maxWithdrawalFee = new(big.Int).Set(config.MaxWithdrawalFee)
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
		} else if resolved.parentChainID != parentChainID {
			return nil, fmt.Errorf("tempo client: Zone.ParentChainID %d does not match parent chain %d of Zone chain %d", resolved.parentChainID, parentChainID, config.ChainID)
		}
	}
	if resolved.id == 0 {
		return nil, fmt.Errorf("tempo client: cannot derive a Zone ID from chain ID %d; set Zone.ZoneID", config.ChainID)
	}
	if !zone.IsID(resolved.id) {
		return nil, fmt.Errorf("tempo client: invalid Zone ID %d", resolved.id)
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

// claimZoneWithdrawal returns the withdrawal for key, or claims it: under one
// lock, it either finds the withdrawal or records a new one being sent, so
// concurrent calls for challenges with one withdrawal key, such as ones that
// differ only in their description, never both send. A call that finds a
// withdrawal being sent waits for its send; if that sent nothing, the call
// claims the withdrawal itself. A call that finds a sent withdrawal whose
// receipt was missing claims it to look for the receipt again: the claimed
// withdrawal then carries the earlier one's hash, and concurrent calls wait
// on that one lookup. claimed reports that the caller must send the
// withdrawal, or look up its receipt when it has a hash, and then call
// finishZoneWithdrawal.
func (m *Method) claimZoneWithdrawal(ctx context.Context, key string) (withdrawal *zoneWithdrawal, claimed bool, err error) {
	for {
		m.zoneMu.Lock()
		withdrawal = m.zoneWithdrawals[key]
		if withdrawal != nil && finished(withdrawal) && withdrawal.unknown() {
			withdrawal = &zoneWithdrawal{done: make(chan struct{}), hash: withdrawal.hash, err: withdrawal.err, settlementStart: withdrawal.settlementStart}
			m.zoneWithdrawals[key] = withdrawal
			m.zoneMu.Unlock()
			return withdrawal, true, nil
		}
		if withdrawal == nil {
			withdrawal = &zoneWithdrawal{done: make(chan struct{})}
			if m.zoneWithdrawals == nil {
				m.zoneWithdrawals = map[string]*zoneWithdrawal{}
			}
			m.zoneWithdrawals[key] = withdrawal
			m.zoneMu.Unlock()
			return withdrawal, true, nil
		}
		m.zoneMu.Unlock()
		select {
		case <-withdrawal.done:
		case <-ctx.Done():
			return nil, false, ctx.Err()
		}
		if withdrawal.sent {
			return withdrawal, false, nil
		}
	}
}

// finished reports whether withdrawal's send or lookup has finished.
func finished(withdrawal *zoneWithdrawal) bool {
	select {
	case <-withdrawal.done:
		return true
	default:
		return false
	}
}

// finishZoneWithdrawal records the outcome of sending a claimed withdrawal
// and wakes the calls waiting on it. A withdrawal that sent nothing is
// forgotten, so the challenge may be paid again.
func (m *Method) finishZoneWithdrawal(key string, withdrawal *zoneWithdrawal) {
	m.zoneMu.Lock()
	defer m.zoneMu.Unlock()
	if !withdrawal.sent && m.zoneWithdrawals[key] == withdrawal {
		delete(m.zoneWithdrawals, key)
	}
	close(withdrawal.done)
}

// forgetZoneWithdrawal drops a settled withdrawal.
func (m *Method) forgetZoneWithdrawal(key string, withdrawal *zoneWithdrawal) {
	m.zoneMu.Lock()
	defer m.zoneMu.Unlock()
	if m.zoneWithdrawals[key] == withdrawal {
		delete(m.zoneWithdrawals, key)
	}
}

// createZoneCredential pays a challenge from the Zone balance: it requests a
// withdrawal to the recipient with the challenge-bound memo, or resumes the
// one already sent for the challenge, waits for it to settle on Tempo, and
// returns a zone credential naming it by sender tag and the Tempo
// transaction that settled it. Calls for challenges with one withdrawal key
// share one withdrawal, so they never send two for one challenge.
//
// authorize runs only before a new withdrawal is sent, while this call holds
// the challenge's claim; its error refuses the payment and sends nothing.
func (m *Method) createZoneCredential(ctx context.Context, rpc tempo.RPCClient, challenge *mpp.Challenge, request tempo.ChargeRequest, authorize func(context.Context) error) (*mpp.Credential, error) {
	// Refuse a credential too large to send before withdrawing: the
	// settlement hash and sender tag are 32 bytes each.
	placeholder := "0x" + strings.Repeat("0", 64)
	if _, err := mpp.FormatCredential(zoneCredential(challenge, placeholder, placeholder, m.zone.id)); err != nil {
		return nil, err
	}
	expected, err := m.zoneWithdrawalRequest(challenge, request)
	if err != nil {
		return nil, err
	}
	key := zoneWithdrawalKey(challenge)
	pending, claimed, err := m.claimZoneWithdrawal(ctx, key)
	if err != nil {
		return nil, err
	}
	if claimed {
		finished := false
		// Whether a withdrawal may have been sent by the time a panic
		// unwinds: one resumed already was, while one the policy or the
		// start-block lookup refused was not, and its challenge stays payable.
		sending := pending.hash != ""
		defer func() {
			if !finished {
				pending.sent, pending.err = sending, errZoneWithdrawalPanicked
				m.finishZoneWithdrawal(key, pending)
			}
		}()
		var (
			senderTag common.Hash
			hash      string
			sent      bool
			sendErr   error
		)
		if pending.hash != "" {
			// An earlier call sent this withdrawal but found no receipt:
			// look for it again instead of sending another.
			senderTag, hash, sent, sendErr = m.resumeZoneWithdrawal(ctx, challenge, expected, pending.hash, pending.err)
		} else if sendErr = authorize(ctx); sendErr == nil {
			// Settlement is searched for from the Tempo head before the
			// send, so a retry long after it still finds the settlement.
			var start uint64
			if start, sendErr = zone.SettlementStartBlock(ctx, rpc); sendErr == nil {
				m.zoneMu.Lock()
				pending.settlementStart = &start
				m.zoneMu.Unlock()
				sending = true
				senderTag, hash, sent, sendErr = m.requestZoneWithdrawal(ctx, challenge, expected)
			}
		}
		// Only a failure before the transaction was sent, or a revert, is
		// safe to retry. After that, the withdrawal may have gone through,
		// so retries of this challenge do not send it again.
		pending.sent, pending.hash, pending.senderTag, pending.err = sent || sendErr == nil, hash, senderTag, sendErr
		finished = true
		m.finishZoneWithdrawal(key, pending)
		if sendErr != nil {
			return nil, sendErr
		}
	}
	if pending.err != nil {
		return nil, pending.err
	}

	recipient := common.HexToAddress(request.Recipient)
	start, err := m.zoneSettlementStart(ctx, rpc, pending)
	if err == nil {
		var settlement zone.Settlement
		settlement, err = zone.WaitForSettlement(ctx, rpc, zone.PortalAddress(m.zone.id), pending.senderTag, &recipient, start, m.settlementTimeout(challenge.Expires))
		if err == nil && settlement.Bounced {
			// The payout bounced back to the payer's Zone balance and paid
			// nothing, so the server would reject its credential: fail
			// without one, and forget the withdrawal so paying the challenge
			// again withdraws again.
			m.forgetZoneWithdrawal(key, pending)
			return nil, &ZoneWithdrawalBouncedError{
				ChallengeID: challenge.ID,
				SenderTag:   pending.senderTag.Hex(),
				ZoneID:      m.zone.id,
			}
		}
		if err == nil {
			credential := zoneCredential(challenge, common.HexToHash(settlement.Hash).Hex(), pending.senderTag.Hex(), m.zone.id)
			if _, err := mpp.FormatCredential(credential); err != nil {
				return nil, err
			}
			m.forgetZoneWithdrawal(key, pending)
			return credential, nil
		}
	}
	return nil, &ZoneSettlementPendingError{
		Challenge:   challenge,
		ChallengeID: challenge.ID,
		SenderTag:   pending.senderTag.Hex(),
		ZoneID:      m.zone.id,
		Err:         err,
	}
}

// zoneSettlementStart returns the Tempo block settlement waits for withdrawal
// search from: the one its first wait chose, so a retry long after that still
// finds a settlement the first wait missed.
func (m *Method) zoneSettlementStart(ctx context.Context, rpc tempo.RPCClient, withdrawal *zoneWithdrawal) (uint64, error) {
	m.zoneMu.Lock()
	start := withdrawal.settlementStart
	m.zoneMu.Unlock()
	if start != nil {
		return *start, nil
	}
	block, err := zone.SettlementStartBlock(ctx, rpc)
	if err != nil {
		return 0, err
	}
	m.zoneMu.Lock()
	defer m.zoneMu.Unlock()
	if withdrawal.settlementStart == nil {
		withdrawal.settlementStart = &block
	}
	return *withdrawal.settlementStart, nil
}

var errZoneWithdrawalPanicked = errors.New("tempo client: Zone withdrawal panicked; its outcome is unknown")

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

// zoneWithdrawalRequest is the withdrawal a challenge's Zone payment
// requests: what the method signs and what the receipt's WithdrawalRequested
// event must show.
type zoneWithdrawalRequest struct {
	sender common.Address
	token  common.Address
	to     common.Address
	amount *big.Int
	memo   common.Hash
}

// zoneWithdrawalRequest returns the withdrawal that pays challenge from the
// Zone: the challenge amount of its currency to its recipient, from the Zone
// signer, bound to the challenge by its memo.
func (m *Method) zoneWithdrawalRequest(challenge *mpp.Challenge, request tempo.ChargeRequest) (zoneWithdrawalRequest, error) {
	amount, ok := new(big.Int).SetString(request.Amount, 10)
	if !ok {
		return zoneWithdrawalRequest{}, fmt.Errorf("tempo client: invalid amount %q", request.Amount)
	}
	return zoneWithdrawalRequest{
		sender: m.zone.signer.Address(),
		token:  common.HexToAddress(request.Currency),
		to:     common.HexToAddress(request.Recipient),
		amount: amount,
		memo:   common.HexToHash(tempo.EncodeAttribution(challenge.Realm, m.clientID, challenge.ID)),
	}, nil
}

// matches reports whether event is the withdrawal w requests.
func (w zoneWithdrawalRequest) matches(event zone.Requested) bool {
	return event.Sender == w.sender && event.Token == w.token && event.To == w.to &&
		event.Amount != nil && event.Amount.Cmp(w.amount) == 0 && event.Memo == w.memo
}

// requestZoneWithdrawal sends the withdrawal that pays challenge, bound to
// it by its memo, and returns the withdrawal's sender tag. sent reports
// whether the transaction may have reached the Zone: a failure after that
// leaves the withdrawal's outcome unknown, except a revert, which moved
// nothing.
func (m *Method) requestZoneWithdrawal(ctx context.Context, challenge *mpp.Challenge, expected zoneWithdrawalRequest) (common.Hash, string, bool, error) {
	rpc := m.zone.rpc
	signer := m.zone.signer
	amount := expected.amount
	currency := expected.token
	recipient := expected.to
	memo := expected.memo

	// The outbox pulls the amount plus its withdrawal fee.
	fee, err := zoneWithdrawalFee(ctx, rpc)
	if err != nil {
		return common.Hash{}, "", false, err
	}
	// PaymentPolicy authorized only the amount: refuse a fee above the
	// configured ceiling before signing, so nothing is sent.
	if m.zone.maxWithdrawalFee != nil && fee.Cmp(m.zone.maxWithdrawalFee) > 0 {
		return common.Hash{}, "", false, fmt.Errorf("%w: fee %s exceeds Zone.MaxWithdrawalFee %s", ErrZoneWithdrawalFeeTooHigh, fee, m.zone.maxWithdrawalFee)
	}
	approve := fmt.Sprintf("0x%s%064x%064x", approveSelector, zone.OutboxAddress.Big(), new(big.Int).Add(amount, fee))
	// A bounced payout refunds the payer, not the payee's Zone address.
	withdrawal, err := zone.OutboxABI.Pack("requestWithdrawal", currency, recipient, amount, [32]byte(memo), uint64(0), signer.Address(), []byte{}, []byte{})
	if err != nil {
		return common.Hash{}, "", false, fmt.Errorf("tempo client: encode Zone withdrawal: %w", err)
	}
	// Prepare and sign first: a failure there sent nothing, so the challenge
	// may be retried.
	gasPrice, err := m.gasPrice(ctx, rpc)
	if err != nil {
		return common.Hash{}, "", false, err
	}
	// Hold the signer's Zone nonce lock from the nonce lookup until the
	// send's outcome is known, so a concurrent payment of another challenge
	// reads the nonce after this transaction took it. Waiting for the lock
	// sent nothing, so a canceled wait may be retried.
	select {
	case m.zone.nonceLock <- struct{}{}:
	case <-ctx.Done():
		return common.Hash{}, "", false, ctx.Err()
	}
	defer func() { <-m.zone.nonceLock }()
	nonce, err := rpc.GetTransactionCount(ctx, signer.Address().Hex())
	if err != nil {
		return common.Hash{}, "", false, fmt.Errorf("tempo client: get Zone nonce: %w", err)
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
		return common.Hash{}, "", false, fmt.Errorf("tempo client: build Zone withdrawal: %w", err)
	}
	if err := tempotx.SignTransaction(tx, signer); err != nil {
		return common.Hash{}, "", false, fmt.Errorf("tempo client: sign Zone withdrawal: %w", err)
	}
	serialized, err := tempotx.Serialize(tx, nil)
	if err != nil {
		return common.Hash{}, "", false, fmt.Errorf("tempo client: serialize Zone withdrawal: %w", err)
	}
	serialized, err = canonicalSignedTransaction(serialized)
	if err != nil {
		return common.Hash{}, "", false, fmt.Errorf("tempo client: serialize Zone withdrawal: %w", err)
	}
	// The signed transaction fixes its hash, so a lost send response can
	// still be recovered from the Zone.
	hash := crypto.Keccak256Hash(common.FromHex(serialized)).Hex()

	// Preparation made RPCs, and may have waited for the nonce lock, after
	// paysFromZone checked the expiry: check it again, so a withdrawal that
	// can no longer settle before the challenge expires is never sent.
	if !m.settlesBeforeExpiry(challenge.Expires) {
		return common.Hash{}, "", false, fmt.Errorf("%w: challenge %q expires at %s, and a withdrawal needs more than %s",
			ErrZoneChallengeExpiresTooSoon, challenge.ID, challenge.Expires, m.zone.settlementTimeout+zoneSubmissionMargin)
	}
	// Nor is one sent when the caller's deadline leaves no time to find its
	// receipt.
	if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) <= zoneSubmissionMargin {
		return common.Hash{}, "", false, fmt.Errorf("%w: the request deadline leaves less than %s to send the withdrawal",
			ErrZoneDeadlineTooSoon, zoneSubmissionMargin)
	}

	// The receipt is looked up, and must match, by the hash of the signed
	// transaction, whatever hash the Zone answers the send with.
	var receipt map[string]any
	if sentHash, sendErr := rpc.SendRawTransaction(ctx, serialized); sendErr == nil && sentHash != "" && !strings.EqualFold(sentHash, hash) {
		err = fmt.Errorf("zone answered the send with transaction hash %s", sentHash)
	} else if sendErr != nil {
		// Whatever the error, another node behind a load-balanced or lagging
		// Zone RPC may have taken the transaction while this one refused it
		// and does not show it, so a send error never proves nothing was
		// withdrawn, as in mppx: wait for the receipt below, and leave the
		// outcome unknown without one, so a retry of the challenge looks for
		// the receipt again rather than withdrawing again.
		err = sendErr
	}
	if receipt == nil {
		receipt = m.waitForZoneReceipt(ctx, rpc, hash)
	}
	if receipt == nil {
		return common.Hash{}, hash, true, &ZoneWithdrawalUnknownError{
			Challenge:       challenge,
			TransactionHash: hash,
			ZoneID:          m.zone.id,
			Err:             err,
		}
	}
	return m.zoneWithdrawalOutcome(challenge, expected, receipt, hash)
}

// resumeZoneWithdrawal looks up the receipt of a withdrawal an earlier call
// sent but found no receipt for, once, and returns its sender tag as
// requestZoneWithdrawal does. A receipt still missing returns
// *ZoneWithdrawalUnknownError again, with the lookup's error or else the
// earlier one's.
func (m *Method) resumeZoneWithdrawal(ctx context.Context, challenge *mpp.Challenge, expected zoneWithdrawalRequest, hash string, previous error) (common.Hash, string, bool, error) {
	receipt, err := lookupZoneTransaction(ctx, m.zone.rpc, "eth_getTransactionReceipt", hash)
	if receipt == nil {
		var unknown *ZoneWithdrawalUnknownError
		if err == nil && errors.As(previous, &unknown) {
			err = unknown.Err
		}
		return common.Hash{}, hash, true, &ZoneWithdrawalUnknownError{
			Challenge:       challenge,
			TransactionHash: hash,
			ZoneID:          m.zone.id,
			Err:             err,
		}
	}
	return m.zoneWithdrawalOutcome(challenge, expected, receipt, hash)
}

// zoneWithdrawalOutcome returns what requestZoneWithdrawal returns for the
// withdrawal expected, signed as transaction hash, whose receipt is receipt.
// A receipt for another transaction, one whose status is neither 0 nor 1 or
// missing, or a successful one without a WithdrawalRequested event matching
// expected leaves the outcome unknown, so a retry of the challenge looks for
// the receipt again rather than withdrawing again.
func (m *Method) zoneWithdrawalOutcome(challenge *mpp.Challenge, expected zoneWithdrawalRequest, receipt map[string]any, hash string) (common.Hash, string, bool, error) {
	unknown := func(err error) (common.Hash, string, bool, error) {
		return common.Hash{}, hash, true, &ZoneWithdrawalUnknownError{
			Challenge:       challenge,
			TransactionHash: hash,
			ZoneID:          m.zone.id,
			Err:             err,
		}
	}
	if receiptHash := stringValue(receipt["transactionHash"]); !strings.EqualFold(receiptHash, hash) {
		return unknown(fmt.Errorf("receipt transaction hash %q is not %s", receiptHash, hash))
	}
	status, ok := receiptStatus(receipt["status"])
	if !ok || status > 1 {
		return unknown(fmt.Errorf("receipt status %v is not 0 or 1", receipt["status"]))
	}
	if status == 0 {
		// A reverted withdrawal moved nothing, so the challenge may be retried.
		return common.Hash{}, hash, false, fmt.Errorf("tempo client: Zone withdrawal transaction %s reverted", hash)
	}
	events := zone.RequestedEvents(receipt)
	if len(events) == 0 {
		return unknown(errors.New("receipt has no WithdrawalRequested event"))
	}
	for _, event := range events {
		if expected.matches(event) {
			return zone.SenderTag(event.Sender, common.HexToHash(hash), event.FallbackNonce), hash, true, nil
		}
	}
	return unknown(errors.New("receipt's WithdrawalRequested event does not match the signed withdrawal"))
}

// receiptStatus parses a receipt's status, a JSON-RPC quantity such as
// "0x1" or "0x01", or a JSON number. ok is false when it is missing or not
// a quantity.
func receiptStatus(value any) (uint64, bool) {
	switch status := value.(type) {
	case string:
		if len(status) < 3 || (status[:2] != "0x" && status[:2] != "0X") {
			return 0, false
		}
		parsed, err := strconv.ParseUint(status[2:], 16, 64)
		return parsed, err == nil
	case float64:
		if status < 0 || status != math.Trunc(status) || status > math.MaxUint32 {
			return 0, false
		}
		return uint64(status), true
	case json.Number:
		parsed, err := strconv.ParseUint(status.String(), 10, 64)
		return parsed, err == nil
	}
	return 0, false
}

// lookupZoneTransaction calls method (eth_getTransactionReceipt or
// eth_getTransactionByHash) once for a transaction, without polling, even
// when ctx has ended. It returns nil and no error when the Zone answers that
// it knows no such transaction, and an error when the lookup failed.
func lookupZoneTransaction(ctx context.Context, rpc tempo.RPCClient, method, hash string) (map[string]any, error) {
	lookupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), zoneReceiptLookupTimeout)
	defer cancel()
	response, err := rpc.SendRequest(lookupCtx, method, hash)
	if err != nil {
		return nil, err
	}
	if err := response.CheckError(); err != nil {
		return nil, err
	}
	if result, ok := response.Result.(map[string]any); ok && len(result) > 0 {
		return result, nil
	}
	return nil, nil
}

// waitForZoneReceipt polls the Zone for a transaction's receipt until
// zoneReceiptTimeout and returns nil when none appears.
func (m *Method) waitForZoneReceipt(ctx context.Context, rpc tempo.RPCClient, hash string) map[string]any {
	timeout := m.zoneReceiptTimeout
	if timeout == 0 {
		timeout = zoneReceiptTimeout
	}
	deadline := time.Now().Add(timeout)
	// Bound each lookup too, so a stalled Zone RPC cannot hold the wait past
	// its timeout; the last lookup gets zoneReceiptLookupTimeout to answer.
	ctx, cancel := context.WithDeadline(ctx, deadline.Add(zoneReceiptLookupTimeout))
	defer cancel()
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
