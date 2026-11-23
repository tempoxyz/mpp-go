// Package zone holds the Tempo Zone helpers the charge client and server
// share to pay and verify charges with Zone withdrawals, as mppx's
// tempo/internal/zone.ts: Zone IDs, portal addresses, sender tags and the
// ZonePortal and ZoneOutbox ABIs.
package zone

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
	temporpc "github.com/tempoxyz/tempo-go/pkg/client"

	"github.com/tempoxyz/mpp-go/pkg/tempo"
)

// DefaultSettlementTimeout is how long a payer waits by default for a Zone
// withdrawal to settle on Tempo.
const DefaultSettlementTimeout = 60 * time.Second

// RedemptionGrace is the longest a server accepts a zone credential after its
// challenge expires (its ZoneCredentialGrace), and so how long after the
// expiry a client still resumes the challenge's withdrawal, as mppx's
// Zone.redemptionGraceMs.
const RedemptionGrace = 5 * time.Minute

// lookbackBlocks is how many Tempo blocks before the current head
// SettlementStartBlock starts the search for a settled withdrawal.
const lookbackBlocks = 7_200

// logsRangeBlocks is the most Tempo blocks one eth_getLogs request of
// WaitForSettlement covers.
const logsRangeBlocks = lookbackBlocks + 1

// maxSenderTagCandidates is the most sender tags SenderTagCandidates derives
// for one withdrawal: the withdrawals its Zone block may hold. Each candidate
// is a topic in every settlement poll's eth_getLogs filter.
const maxSenderTagCandidates = 64

// pollingInterval is the time between polls for a settled withdrawal.
const pollingInterval = 500 * time.Millisecond

// OutboxAddress is the ZoneOutbox predeploy on every Zone chain.
var OutboxAddress = common.HexToAddress("0x1c00000000000000000000000000000000000002")

// Zone chain ID schemes on Tempo mainnet and Moderato, as ox's ZoneId.
const (
	mainnetChainIDBase = 421_700_000
	testnetChainIDBase = 1_424_310_000
)

// maxIDExclusive bounds Zone IDs, as mppx's maxIdExclusive: IDs from here up
// read as Zone chain IDs in viem's portal lookup, which would map them to
// another Zone's portal.
const maxIDExclusive = 421_700_000

// IsID reports whether id is a valid Zone ID: a positive integer below the
// Zone chain ID ranges, as mppx's isId.
func IsID(id uint32) bool {
	return id > 0 && id < maxIDExclusive
}

const portalABIJSON = `[
  {"type":"function","name":"processWithdrawals","stateMutability":"nonpayable","inputs":[
    {"type":"tuple[]","name":"withdrawals","components":[
      {"type":"address","name":"token"},
      {"type":"bytes32","name":"senderTag"},
      {"type":"address","name":"to"},
      {"type":"uint128","name":"amount"},
      {"type":"bytes32","name":"memo"},
      {"type":"uint64","name":"gasLimit"},
      {"type":"uint64","name":"fallbackNonce"},
      {"type":"bytes","name":"callbackData"},
      {"type":"bytes","name":"encryptedSender"}]},
    {"type":"bytes32","name":"remainingQueue"}],"outputs":[]},
  {"type":"event","name":"WithdrawalProcessed","inputs":[
    {"type":"address","name":"to","indexed":true},
    {"type":"bytes32","name":"senderTag","indexed":true},
    {"type":"address","name":"token"},
    {"type":"uint128","name":"amount"},
    {"type":"bool","name":"callbackSuccess"}]}
]`

const outboxABIJSON = `[
  {"type":"function","name":"calculateWithdrawalFee","stateMutability":"view","inputs":[
    {"type":"uint64","name":"gasLimit"}],"outputs":[{"type":"uint128","name":"fee"}]},
  {"type":"function","name":"lastFallbackNonce","stateMutability":"view","inputs":[],"outputs":[{"type":"uint64"}]},
  {"type":"function","name":"requestWithdrawal","stateMutability":"nonpayable","inputs":[
    {"type":"address","name":"token"},
    {"type":"address","name":"to"},
    {"type":"uint128","name":"amount"},
    {"type":"bytes32","name":"memo"},
    {"type":"uint64","name":"gasLimit"},
    {"type":"address","name":"zoneFallbackRecipient"},
    {"type":"bytes","name":"data"},
    {"type":"bytes","name":"revealTo"}],"outputs":[]},
  {"type":"event","name":"WithdrawalRequested","inputs":[
    {"type":"uint64","name":"withdrawalIndex","indexed":true},
    {"type":"address","name":"sender","indexed":true},
    {"type":"address","name":"token"},
    {"type":"address","name":"to"},
    {"type":"uint128","name":"amount"},
    {"type":"uint128","name":"fee"},
    {"type":"bytes32","name":"memo"},
    {"type":"uint64","name":"gasLimit"},
    {"type":"uint64","name":"fallbackNonce"},
    {"type":"bytes","name":"data"},
    {"type":"bytes","name":"revealTo"}]}
]`

var (
	// PortalABI is the subset of the ZonePortal ABI the charge flow uses.
	PortalABI = mustParseABI(portalABIJSON)
	// OutboxABI is the subset of the ZoneOutbox ABI the charge flow uses.
	OutboxABI = mustParseABI(outboxABIJSON)
	// ProcessedTopic is the ZonePortal WithdrawalProcessed event topic.
	ProcessedTopic = PortalABI.Events["WithdrawalProcessed"].ID
	// RequestedTopic is the ZoneOutbox WithdrawalRequested event topic.
	RequestedTopic = OutboxABI.Events["WithdrawalRequested"].ID
)

func mustParseABI(definition string) abi.ABI {
	parsed, err := abi.JSON(strings.NewReader(definition))
	if err != nil {
		panic(err)
	}
	return parsed
}

// Withdrawal is a withdrawal as ZonePortal.processWithdrawals receives it on
// Tempo.
type Withdrawal struct {
	Token           common.Address
	SenderTag       [32]byte
	To              common.Address
	Amount          *big.Int
	Memo            [32]byte
	GasLimit        uint64
	FallbackNonce   uint64
	CallbackData    []byte
	EncryptedSender []byte
}

// ProcessedEvent is a ZonePortal WithdrawalProcessed event.
type ProcessedEvent struct {
	To              common.Address
	SenderTag       common.Hash
	Token           common.Address
	Amount          *big.Int
	CallbackSuccess bool
}

// PortalAddress returns the Tempo portal address of a Zone, as viem's
// Addresses.zonePortal, and Zones 6 and 7 keep their legacy portals. The ID
// is read literally: unlike viem, a Zone chain ID is not mapped to its Zone
// (IDFromChainID does that), and callers accept only IDs that pass IsID.
func PortalAddress(zoneID uint32) common.Address {
	id := uint64(zoneID)
	// TODO: drop the legacy portals once viem does.
	switch id {
	case 6:
		return common.HexToAddress("0x7069DeC4E64Fd07334A0933eDe836C17259c9B23")
	case 7:
		return common.HexToAddress("0x3F5296303400B56271b476F5A0B9cBF74350D6Ac")
	}
	return common.HexToAddress(fmt.Sprintf("0x5ad000000000000000000000%016x", id))
}

// portalPrefix is what every Zone portal address but the legacy ones starts
// with: PortalAddress(0) without its last 8 bytes, which hold the Zone ID.
var portalPrefix = PortalAddress(0).Bytes()[:common.AddressLength-8]

// IsPortalAddress reports whether address is a Zone portal: it has the portal
// prefix, or is the legacy portal of Zone 6 or 7.
func IsPortalAddress(address common.Address) bool {
	return bytes.HasPrefix(address.Bytes(), portalPrefix) ||
		address == PortalAddress(6) || address == PortalAddress(7)
}

// IDFromChainID derives a Zone ID from a Zone chain ID. Zones on Tempo
// mainnet and Moderato use reserved bases (421700000 and 1424310000); Zones
// on any other parent chain use (parentChainId << 32) | zoneId.
func IDFromChainID(chainID int64) (uint32, error) {
	switch {
	case chainID >= 1<<32:
		return uint32(chainID % (1 << 32)), nil
	case chainID > testnetChainIDBase && chainID < testnetChainIDBase+10_000:
		return uint32(chainID - testnetChainIDBase), nil
	case chainID > mainnetChainIDBase && chainID < mainnetChainIDBase+10_000:
		return uint32(chainID - mainnetChainIDBase), nil
	}
	return 0, fmt.Errorf("cannot derive a Zone ID from chain ID %d; set ZoneID", chainID)
}

// ParentChainIDFromChainID derives the parent Tempo chain ID of a Zone chain
// ID. It reports false when the chain ID follows no known Zone scheme.
func ParentChainIDFromChainID(chainID int64) (int64, bool) {
	switch {
	case chainID >= 1<<32:
		return chainID / (1 << 32), true
	case chainID > testnetChainIDBase && chainID < testnetChainIDBase+10_000:
		return tempo.ChainIDModerato, true
	case chainID > mainnetChainIDBase && chainID < mainnetChainIDBase+10_000:
		return tempo.ChainIDMainnet, true
	}
	return 0, false
}

// SenderTag derives a withdrawal's sender tag, which identifies it in the
// WithdrawalProcessed event on Tempo:
// keccak256(abi.encodePacked(sender, txHash, uint64 fallbackNonce)).
func SenderTag(sender common.Address, transactionHash common.Hash, fallbackNonce uint64) common.Hash {
	var nonce [8]byte
	binary.BigEndian.PutUint64(nonce[:], fallbackNonce)
	return crypto.Keccak256Hash(sender.Bytes(), transactionHash.Bytes(), nonce[:])
}

// FindProcessedEvent returns the WithdrawalProcessed event portal emitted
// for senderTag among a receipt's logs.
func FindProcessedEvent(receipt map[string]any, portal common.Address, senderTag common.Hash) (*ProcessedEvent, bool) {
	logs, _ := receipt["logs"].([]any)
	event := PortalABI.Events["WithdrawalProcessed"]
	for _, rawLog := range logs {
		entry, ok := rawLog.(map[string]any)
		if !ok || !strings.EqualFold(asString(entry["address"]), portal.Hex()) {
			continue
		}
		topics, ok := entry["topics"].([]any)
		if !ok || len(topics) != 3 ||
			!strings.EqualFold(asString(topics[0]), ProcessedTopic.Hex()) ||
			!strings.EqualFold(asString(topics[2]), senderTag.Hex()) {
			continue
		}
		data, err := hexutil.Decode(asString(entry["data"]))
		if err != nil {
			continue
		}
		values, err := event.Inputs.NonIndexed().Unpack(data)
		if err != nil || len(values) != 3 {
			continue
		}
		token, okToken := values[0].(common.Address)
		amount, okAmount := values[1].(*big.Int)
		callbackSuccess, okSuccess := values[2].(bool)
		if !okToken || !okAmount || !okSuccess {
			continue
		}
		return &ProcessedEvent{
			To:              tempoTopicAddress(asString(topics[1])),
			SenderTag:       senderTag,
			Token:           token,
			Amount:          amount,
			CallbackSuccess: callbackSuccess,
		}, true
	}
	return nil, false
}

// FindWithdrawal finds the withdrawal for senderTag in the calldata of a
// settlement transaction's top-level processWithdrawals calls to portal.
// Each call is a JSON-RPC call object with "to" and "data" or "input".
func FindWithdrawal(calls []any, portal common.Address, senderTag common.Hash) (*Withdrawal, bool) {
	method := PortalABI.Methods["processWithdrawals"]
	for _, rawCall := range calls {
		call, ok := rawCall.(map[string]any)
		if !ok || !strings.EqualFold(asString(call["to"]), portal.Hex()) {
			continue
		}
		dataHex, ok := call["data"].(string)
		if !ok {
			dataHex, _ = call["input"].(string)
		}
		data, err := hexutil.Decode(dataHex)
		if err != nil || len(data) < 4 || !strings.EqualFold(hexutil.Encode(data[:4]), hexutil.Encode(method.ID)) {
			continue
		}
		values, err := method.Inputs.Unpack(data[4:])
		if err != nil || len(values) == 0 {
			continue
		}
		withdrawals, ok := convertWithdrawals(values[0])
		if !ok {
			continue
		}
		for i := range withdrawals {
			if withdrawals[i].SenderTag == senderTag {
				return &withdrawals[i], true
			}
		}
	}
	return nil, false
}

// convertWithdrawals converts the unpacked processWithdrawals argument to
// Withdrawals. abi.ConvertType panics on a type it cannot convert.
func convertWithdrawals(value any) (withdrawals []Withdrawal, ok bool) {
	defer func() {
		if recover() != nil {
			withdrawals, ok = nil, false
		}
	}()
	converted, ok := abi.ConvertType(value, new([]Withdrawal)).(*[]Withdrawal)
	if !ok {
		return nil, false
	}
	return *converted, true
}

// Requested is a ZoneOutbox WithdrawalRequested event.
type Requested struct {
	Sender        common.Address
	Token         common.Address
	To            common.Address
	Amount        *big.Int
	Memo          common.Hash
	FallbackNonce uint64
}

// RequestedEvents returns the ZoneOutbox WithdrawalRequested events in a Zone
// transaction receipt, skipping logs that do not decode as one.
func RequestedEvents(receipt map[string]any) []Requested {
	logs, _ := receipt["logs"].([]any)
	event := OutboxABI.Events["WithdrawalRequested"]
	var events []Requested
	for _, rawLog := range logs {
		entry, isMap := rawLog.(map[string]any)
		if !isMap || !strings.EqualFold(asString(entry["address"]), OutboxAddress.Hex()) {
			continue
		}
		topics, isList := entry["topics"].([]any)
		if !isList || len(topics) != 3 || !strings.EqualFold(asString(topics[0]), RequestedTopic.Hex()) {
			continue
		}
		data, err := hexutil.Decode(asString(entry["data"]))
		if err != nil {
			continue
		}
		values, err := event.Inputs.NonIndexed().Unpack(data)
		if err != nil || len(values) != 9 {
			continue
		}
		token, isToken := values[0].(common.Address)
		to, isTo := values[1].(common.Address)
		amount, isAmount := values[2].(*big.Int)
		memo, isMemo := values[4].([32]byte)
		nonce, isNonce := values[6].(uint64)
		if !isToken || !isTo || !isAmount || !isMemo || !isNonce {
			continue
		}
		events = append(events, Requested{
			Sender:        tempoTopicAddress(asString(topics[2])),
			Token:         token,
			To:            to,
			Amount:        amount,
			Memo:          memo,
			FallbackNonce: nonce,
		})
	}
	return events
}

// HasRequestedLog reports whether a Zone transaction receipt has a ZoneOutbox
// log with the WithdrawalRequested topic, whether or not it decodes. A Zone
// RPC that redacts other contracts' logs leaves it out of the payer's receipt.
func HasRequestedLog(receipt map[string]any) bool {
	logs, _ := receipt["logs"].([]any)
	for _, rawLog := range logs {
		entry, isMap := rawLog.(map[string]any)
		if !isMap || !strings.EqualFold(asString(entry["address"]), OutboxAddress.Hex()) {
			continue
		}
		if topics, isList := entry["topics"].([]any); isList && len(topics) > 0 &&
			strings.EqualFold(asString(topics[0]), RequestedTopic.Hex()) {
			return true
		}
	}
	return false
}

// Requester is the JSON-RPC subset WaitForSettlement and SenderTagCandidates
// use.
type Requester interface {
	SendRequest(ctx context.Context, method string, params ...interface{}) (*temporpc.JSONRPCResponse, error)
}

// SenderTagCandidates derives the sender tags a withdrawal transaction may
// carry when its receipt has no WithdrawalRequested event, as on a Zone RPC
// that redacts logs, as mppx's Zone.senderTagCandidates.
//
// The outbox numbers each withdrawal with the next lastFallbackNonce, so the
// transaction's withdrawal took one of the nonces its block added. Every
// candidate hashes the payer's address and transaction, so only the payer's
// withdrawal can match one.
func SenderTagCandidates(ctx context.Context, rpc Requester, blockNumber uint64, sender common.Address, transactionHash common.Hash) ([]common.Hash, error) {
	// No transaction is in the genesis block.
	if blockNumber == 0 {
		return nil, fmt.Errorf("zone withdrawal transaction %s did not request a withdrawal from the outbox", transactionHash.Hex())
	}
	before, err := lastFallbackNonce(ctx, rpc, blockNumber-1)
	if err != nil {
		return nil, err
	}
	after, err := lastFallbackNonce(ctx, rpc, blockNumber)
	if err != nil {
		return nil, err
	}
	if after <= before {
		return nil, fmt.Errorf("zone withdrawal transaction %s did not request a withdrawal from the outbox", transactionHash.Hex())
	}
	// The nonces come from the Zone RPC, so a range no block could hold is
	// refused rather than hashed.
	if after-before > maxSenderTagCandidates {
		return nil, fmt.Errorf("zone withdrawal transaction %s has %d candidate sender tags, more than %d", transactionHash.Hex(), after-before, maxSenderTagCandidates)
	}
	tags := make([]common.Hash, 0, after-before)
	for nonce := before + 1; nonce <= after; nonce++ {
		tags = append(tags, SenderTag(sender, transactionHash, nonce))
	}
	return tags, nil
}

// lastFallbackNonce reads the ZoneOutbox's lastFallbackNonce at a Zone block.
func lastFallbackNonce(ctx context.Context, rpc Requester, block uint64) (uint64, error) {
	data, err := OutboxABI.Pack("lastFallbackNonce")
	if err != nil {
		return 0, err
	}
	response, err := rpc.SendRequest(ctx, "eth_call", map[string]any{
		"to":   OutboxAddress.Hex(),
		"data": hexutil.Encode(data),
	}, hexutil.EncodeUint64(block))
	if err != nil {
		return 0, fmt.Errorf("read Zone outbox lastFallbackNonce at block %d: %w", block, err)
	}
	if err := response.CheckError(); err != nil {
		return 0, fmt.Errorf("read Zone outbox lastFallbackNonce at block %d: %w", block, err)
	}
	result, err := hexutil.Decode(asString(response.Result))
	if err != nil {
		return 0, fmt.Errorf("read Zone outbox lastFallbackNonce at block %d: %w", block, err)
	}
	values, err := OutboxABI.Unpack("lastFallbackNonce", result)
	if err != nil || len(values) != 1 {
		return 0, fmt.Errorf("read Zone outbox lastFallbackNonce at block %d: unexpected result %s", block, hexutil.Encode(result))
	}
	nonce, ok := values[0].(uint64)
	if !ok {
		return 0, fmt.Errorf("read Zone outbox lastFallbackNonce at block %d: unexpected result %s", block, hexutil.Encode(result))
	}
	return nonce, nil
}

// SettlementStartBlock returns the Tempo block a first WaitForSettlement for
// a withdrawal searches from: lookbackBlocks before the current head.
func SettlementStartBlock(ctx context.Context, rpc Requester) (uint64, error) {
	latest, err := blockNumber(ctx, rpc)
	if err != nil {
		return 0, err
	}
	if latest > lookbackBlocks {
		return latest - lookbackBlocks, nil
	}
	return 0, nil
}

// Settlement is a WithdrawalProcessed event WaitForSettlement found on Tempo.
type Settlement struct {
	// Hash is the hash of the Tempo transaction that emitted it.
	Hash string
	// SenderTag is the sender tag of the settled withdrawal: the one of the
	// tags waited for that the event carries.
	SenderTag common.Hash
	// Bounced reports that the event's callbackSuccess is false: the payout
	// failed and its amount went back to the sender's Zone balance. An event
	// whose data does not decode is not reported as a bounce; the server
	// verifies the settlement either way.
	Bounced bool
}

// rpcGrace is how long past its timeout WaitForSettlement lets the RPC calls
// under way finish.
const rpcGrace = 5 * time.Second

// WaitForSettlement waits until portal emits WithdrawalProcessed for
// senderTag (and, when to is set, that recipient) on Tempo, and returns the
// Tempo transaction that settled it and whether its payout bounced. It searches from fromBlock
// to the head once, logsRangeBlocks at a time, then only blocks added since
// the previous poll, and gives up after timeout, cutting off RPC calls that
// run rpcGrace past it. A wait that resumes an
// earlier one passes the earlier one's fromBlock, so it still finds a
// settlement however far back the head has moved since.
func WaitForSettlement(ctx context.Context, rpc Requester, portal common.Address, senderTag common.Hash, to *common.Address, fromBlock uint64, timeout time.Duration) (Settlement, error) {
	return WaitForSettledWithdrawal(ctx, rpc, portal, []common.Hash{senderTag}, to, fromBlock, timeout)
}

// WaitForSettledWithdrawal is WaitForSettlement for a withdrawal that may
// carry any of several sender tags, as mppx's Zone.waitForSettledWithdrawal:
// it waits until portal emits WithdrawalProcessed for one of senderTags, and
// the Settlement it returns names that tag.
func WaitForSettledWithdrawal(ctx context.Context, rpc Requester, portal common.Address, senderTags []common.Hash, to *common.Address, fromBlock uint64, timeout time.Duration) (Settlement, error) {
	if timeout < 0 {
		return Settlement{}, fmt.Errorf("zone settlement timeout must be non-negative")
	}
	if len(senderTags) == 0 {
		return Settlement{}, fmt.Errorf("zone settlement needs at least one sender tag")
	}
	deadline := time.Now().Add(timeout)
	// Bound every RPC call by the deadline too, so a stalled Tempo RPC cannot
	// hold the wait past its timeout; the calls under way when it passes get
	// rpcGrace to answer.
	ctx, cancel := context.WithDeadline(ctx, deadline.Add(rpcGrace))
	defer cancel()
	latest, err := blockNumber(ctx, rpc)
	if err != nil {
		return Settlement{}, err
	}
	var toTopic any
	if to != nil {
		toTopic = common.BytesToHash(to.Bytes()).Hex()
	}
	for {
		for fromBlock <= latest {
			toBlock := latest
			if latest-fromBlock >= logsRangeBlocks {
				toBlock = fromBlock + logsRangeBlocks - 1
			}
			settlement, err := settlementLog(ctx, rpc, portal, toTopic, senderTags, fromBlock, toBlock)
			if err != nil || settlement.Hash != "" {
				return settlement, err
			}
			fromBlock = toBlock + 1
		}
		if time.Now().Add(pollingInterval).After(deadline) {
			return Settlement{}, fmt.Errorf("zone withdrawal %s did not settle on Tempo within %s", joinTags(senderTags), timeout)
		}
		timer := time.NewTimer(pollingInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return Settlement{}, ctx.Err()
		case <-timer.C:
		}
		if latest, err = blockNumber(ctx, rpc); err != nil {
			return Settlement{}, err
		}
	}
}

// settlementLog returns the settlement in blocks fromBlock to toBlock in
// which portal emitted WithdrawalProcessed for one of senderTags, or one with
// an empty Hash when there is none.
func settlementLog(ctx context.Context, rpc Requester, portal common.Address, toTopic any, senderTags []common.Hash, fromBlock, toBlock uint64) (Settlement, error) {
	// One tag is a single topic; several are an OR list of topics.
	var tagTopic any = senderTags[0].Hex()
	if len(senderTags) > 1 {
		tags := make([]string, len(senderTags))
		for i, tag := range senderTags {
			tags[i] = tag.Hex()
		}
		tagTopic = tags
	}
	response, err := rpc.SendRequest(ctx, "eth_getLogs", map[string]any{
		"address":   portal.Hex(),
		"topics":    []any{ProcessedTopic.Hex(), toTopic, tagTopic},
		"fromBlock": hexutil.EncodeUint64(fromBlock),
		"toBlock":   hexutil.EncodeUint64(toBlock),
	})
	if err != nil {
		return Settlement{}, fmt.Errorf("get Zone settlement logs: %w", err)
	}
	if err := response.CheckError(); err != nil {
		return Settlement{}, fmt.Errorf("get Zone settlement logs: %w", err)
	}
	if logs, ok := response.Result.([]any); ok {
		for _, rawLog := range logs {
			entry, ok := rawLog.(map[string]any)
			if !ok {
				continue
			}
			hash := asString(entry["transactionHash"])
			tag, ok := processedSenderTag(entry, senderTags)
			if hash != "" && ok {
				return Settlement{Hash: hash, SenderTag: tag, Bounced: processedBounced(entry)}, nil
			}
		}
	}
	return Settlement{}, nil
}

// processedSenderTag returns the sender tag of a WithdrawalProcessed log: its
// senderTag topic when it is one of senderTags, or the only tag the filter
// asked for when the log leaves its topics out. ok is false otherwise.
func processedSenderTag(entry map[string]any, senderTags []common.Hash) (common.Hash, bool) {
	topics, _ := entry["topics"].([]any)
	if len(topics) < 3 {
		if len(senderTags) == 1 {
			return senderTags[0], true
		}
		return common.Hash{}, false
	}
	for _, tag := range senderTags {
		if strings.EqualFold(asString(topics[2]), tag.Hex()) {
			return tag, true
		}
	}
	return common.Hash{}, false
}

// joinTags formats sender tags as "A or B".
func joinTags(senderTags []common.Hash) string {
	tags := make([]string, len(senderTags))
	for i, tag := range senderTags {
		tags[i] = tag.Hex()
	}
	return strings.Join(tags, " or ")
}

// processedBounced reports whether a WithdrawalProcessed log's data decodes
// to callbackSuccess false, as FindProcessedEvent decodes it.
func processedBounced(entry map[string]any) bool {
	data, err := hexutil.Decode(asString(entry["data"]))
	if err != nil {
		return false
	}
	values, err := PortalABI.Events["WithdrawalProcessed"].Inputs.NonIndexed().Unpack(data)
	if err != nil || len(values) != 3 {
		return false
	}
	callbackSuccess, ok := values[2].(bool)
	return ok && !callbackSuccess
}

func blockNumber(ctx context.Context, rpc Requester) (uint64, error) {
	response, err := rpc.SendRequest(ctx, "eth_blockNumber")
	if err != nil {
		return 0, fmt.Errorf("get block number: %w", err)
	}
	if err := response.CheckError(); err != nil {
		return 0, fmt.Errorf("get block number: %w", err)
	}
	value, ok := response.Result.(string)
	if !ok {
		return 0, fmt.Errorf("unexpected eth_blockNumber result %T", response.Result)
	}
	return tempo.ParseHexUint64(value)
}

func tempoTopicAddress(topic string) common.Address {
	return common.HexToAddress(tempo.ParseTopicAddress(topic))
}

func asString(value any) string {
	text, _ := value.(string)
	return text
}
