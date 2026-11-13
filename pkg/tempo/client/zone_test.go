package chargeclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	temporpc "github.com/tempoxyz/tempo-go/pkg/client"
	temposigner "github.com/tempoxyz/tempo-go/pkg/signer"
	tempotx "github.com/tempoxyz/tempo-go/pkg/transaction"

	"github.com/tempoxyz/mpp-go/pkg/mpp"
	"github.com/tempoxyz/mpp-go/pkg/tempo"
	"github.com/tempoxyz/mpp-go/pkg/tempo/internal/zone"
)

const (
	zoneTestChainID    = int64(1_424_310_003)
	zoneTestSettlement = "0xabababababababababababababababababababababababababababababababab"
	zoneTestFee        = 1_000
)

// zoneTestRPC is a Zone RPC. In "fail" mode every call fails with
// "zone used", to show whether the method used the Zone; "ok" withdraws;
// "prepare" fails before sending; "send" loses the send response and never
// shows a receipt; "reject" has the Zone reject the send with a JSON-RPC
// error; "internal" has it answer the send with an unrecognized JSON-RPC
// error and never show the transaction; "nonce" and "imported" have it
// answer a send it took with a JSON-RPC error; "pending" has it refuse a
// send for insufficient funds while the transaction is pending, showing its
// receipt only on a later poll, and "stuck" likewise but never shows a
// receipt; "revert" reverts the withdrawal. When sending is set, a send
// signals it and then waits for release; when looking is set, a receipt
// lookup does the same.
type zoneTestRPC struct {
	mode     string
	sending  chan struct{}
	release  chan struct{}
	looking  chan struct{}
	mu       sync.Mutex
	calls    []string
	sent     []string
	receipts int
	// status, when set, replaces the status of a successful withdrawal's
	// receipt; noStatus leaves it out.
	status   any
	noStatus bool
	// event, when set, spoils the WithdrawalRequested event of a successful
	// withdrawal's receipt: "none" leaves it out, "garbled" makes its data
	// undecodable, and "sender", "token", "to", "amount" or "memo" changes
	// that field; "hash" gives the receipt another transaction hash.
	event string
	// answerHash, when set, is the hash a successful send answers with.
	answerHash string
	// onNonce, when set, runs at the nonce lookup.
	onNonce func()
	// trackNonce has the Zone answer the nonce lookup with the number of
	// transactions sent, as a pending nonce, and refuse a send whose nonce
	// was already sent with "nonce too low".
	trackNonce bool
	nonces     map[uint64]bool
}

func (r *zoneTestRPC) record(method string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, method)
}

func (r *zoneTestRPC) callCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.calls)
}

func (r *zoneTestRPC) GetChainID(context.Context) (uint64, error) {
	r.record("eth_chainId")
	return uint64(zoneTestChainID), nil
}

func (r *zoneTestRPC) GetTransactionCount(context.Context, string) (uint64, error) {
	r.record("eth_getTransactionCount")
	if r.onNonce != nil {
		r.onNonce()
	}
	if r.mode == "fail" {
		return 0, errors.New("zone used")
	}
	if r.trackNonce {
		r.mu.Lock()
		defer r.mu.Unlock()
		return uint64(len(r.nonces)), nil
	}
	return 0, nil
}

func (r *zoneTestRPC) SendRawTransaction(_ context.Context, serialized string) (string, error) {
	r.record("eth_sendRawTransaction")
	if r.trackNonce {
		tx, err := tempotx.Deserialize(serialized)
		if err != nil {
			return "", err
		}
		r.mu.Lock()
		used := r.nonces[tx.Nonce]
		if !used {
			if r.nonces == nil {
				r.nonces = map[uint64]bool{}
			}
			r.nonces[tx.Nonce] = true
		}
		r.mu.Unlock()
		if used {
			return "", fmt.Errorf("eth_sendRawTransaction: %w", &temporpc.JSONRPCError{Code: -32003, Message: "nonce too low"})
		}
	}
	r.mu.Lock()
	r.sent = append(r.sent, serialized)
	r.mu.Unlock()
	if r.sending != nil {
		r.sending <- struct{}{}
		<-r.release
	}
	switch r.mode {
	case "fail", "send":
		return "", errors.New("connection lost")
	case "reject":
		return "", fmt.Errorf("eth_sendRawTransaction: %w", &temporpc.JSONRPCError{Code: -32000, Message: "insufficient funds for gas"})
	case "internal":
		return "", fmt.Errorf("eth_sendRawTransaction: %w", &temporpc.JSONRPCError{Code: -32000, Message: "internal error"})
	case "nonce":
		return "", fmt.Errorf("eth_sendRawTransaction: %w", &temporpc.JSONRPCError{Code: -32003, Message: "nonce too low"})
	case "imported":
		return "", fmt.Errorf("eth_sendRawTransaction: %w", &temporpc.JSONRPCError{Code: -32000, Message: "transaction already imported"})
	case "pending", "stuck":
		return "", fmt.Errorf("eth_sendRawTransaction: %w", &temporpc.JSONRPCError{Code: -32000, Message: "insufficient funds for gas"})
	}
	if r.answerHash != "" {
		return r.answerHash, nil
	}
	return crypto.Keccak256Hash(common.FromHex(serialized)).Hex(), nil
}

func (r *zoneTestRPC) SendRequest(_ context.Context, method string, params ...interface{}) (*temporpc.JSONRPCResponse, error) {
	r.record(method)
	if r.mode == "fail" {
		time.Sleep(10 * time.Millisecond)
		return nil, errors.New("zone used")
	}
	switch method {
	case "eth_call":
		if r.mode == "prepare" {
			return nil, errors.New("connection lost")
		}
		return &temporpc.JSONRPCResponse{Result: fmt.Sprintf("0x%064x", zoneTestFee)}, nil
	case "eth_gasPrice":
		return &temporpc.JSONRPCResponse{Result: "0x1"}, nil
	case "eth_getTransactionReceipt":
		r.mu.Lock()
		r.receipts++
		receipts := r.receipts
		looking := r.looking
		r.mu.Unlock()
		if looking != nil {
			looking <- struct{}{}
			<-r.release
		}
		if r.mode == "send" || r.mode == "reject" || r.mode == "internal" || r.mode == "stuck" || (r.mode == "pending" && receipts == 1) {
			return &temporpc.JSONRPCResponse{Result: nil}, nil
		}
		return &temporpc.JSONRPCResponse{Result: r.receipt(params[0].(string))}, nil
	case "eth_getTransactionByHash":
		if r.mode == "pending" || r.mode == "stuck" {
			return &temporpc.JSONRPCResponse{Result: map[string]any{"hash": params[0]}}, nil
		}
		return &temporpc.JSONRPCResponse{Result: nil}, nil
	}
	return nil, fmt.Errorf("zone: unexpected %s", method)
}

// receipt returns the receipt of the withdrawal sent as hash, or else of the
// last withdrawal sent.
func (r *zoneTestRPC) receipt(hash string) map[string]any {
	if r.mode == "revert" {
		return map[string]any{"transactionHash": hash, "status": "0x0", "logs": []any{}}
	}
	r.mu.Lock()
	serialized := r.sent[len(r.sent)-1]
	for _, sent := range r.sent {
		if crypto.Keccak256Hash(common.FromHex(sent)).Hex() == hash {
			serialized = sent
		}
	}
	r.mu.Unlock()
	tx, err := tempotx.Deserialize(serialized)
	if err != nil {
		panic(err)
	}
	sender, err := tempotx.VerifySignature(tx)
	if err != nil {
		panic(err)
	}
	// The event shows the withdrawal the transaction requested.
	values, err := zone.OutboxABI.Methods["requestWithdrawal"].Inputs.Unpack(tx.Calls[1].Data[4:])
	if err != nil {
		panic(err)
	}
	token, to, amount, memo := values[0].(common.Address), values[1].(common.Address), values[2].(*big.Int), values[3].([32]byte)
	r.mu.Lock()
	defer r.mu.Unlock()
	switch r.event {
	case "sender":
		sender = common.HexToAddress("0x00000000000000000000000000000000000000aa")
	case "token":
		token = common.HexToAddress("0x00000000000000000000000000000000000000bb")
	case "to":
		to = common.HexToAddress("0x00000000000000000000000000000000000000cc")
	case "amount":
		amount = new(big.Int).Add(amount, big.NewInt(1))
	case "memo":
		memo[31] ^= 1
	case "hash":
		hash = common.HexToHash("0x01").Hex()
	}
	data, err := zone.OutboxABI.Events["WithdrawalRequested"].Inputs.NonIndexed().Pack(
		token, to, amount, big.NewInt(zoneTestFee), memo, uint64(0), uint64(1), []byte{}, []byte{})
	if err != nil {
		panic(err)
	}
	if r.event == "garbled" {
		data = data[:len(data)-1]
	}
	logs := []any{map[string]any{
		"address": zone.OutboxAddress.Hex(),
		"topics":  []any{zone.RequestedTopic.Hex(), common.BigToHash(big.NewInt(0)).Hex(), common.BytesToHash(sender.Bytes()).Hex()},
		"data":    hexutil.Encode(data),
	}}
	if r.event == "none" {
		logs = []any{}
	}
	receipt := map[string]any{
		"transactionHash": hash,
		"status":          "0x1",
		"logs":            logs,
	}
	if r.status != nil {
		receipt["status"] = r.status
	}
	if r.noStatus {
		delete(receipt, "status")
	}
	return receipt
}

// zoneTestTempoRPC is the Tempo RPC; it shows a settlement once settled, in
// any block range, or when settledAt is set only in ranges that include it.
// Its head is block 0x10 unless head is set.
type zoneTestTempoRPC struct {
	mu      sync.Mutex
	settled bool
	// bounced has the settlement's WithdrawalProcessed event report
	// callbackSuccess false.
	bounced   bool
	settledAt uint64
	head      uint64
	topics    []any
	ranges    [][2]uint64
}

func (r *zoneTestTempoRPC) GetChainID(context.Context) (uint64, error) { return 42431, nil }

func (r *zoneTestTempoRPC) GetTransactionCount(context.Context, string) (uint64, error) {
	return 0, errors.New("tempo: unexpected nonce")
}

func (r *zoneTestTempoRPC) SendRawTransaction(context.Context, string) (string, error) {
	return "", errors.New("tempo: unexpected send")
}

func (r *zoneTestTempoRPC) SendRequest(_ context.Context, method string, params ...interface{}) (*temporpc.JSONRPCResponse, error) {
	switch method {
	case "eth_blockNumber":
		r.mu.Lock()
		defer r.mu.Unlock()
		if r.head != 0 {
			return &temporpc.JSONRPCResponse{Result: hexutil.EncodeUint64(r.head)}, nil
		}
		return &temporpc.JSONRPCResponse{Result: "0x10"}, nil
	case "eth_getLogs":
		r.mu.Lock()
		defer r.mu.Unlock()
		filter := params[0].(map[string]any)
		r.topics = filter["topics"].([]any)
		from := hexutil.MustDecodeUint64(filter["fromBlock"].(string))
		to := hexutil.MustDecodeUint64(filter["toBlock"].(string))
		r.ranges = append(r.ranges, [2]uint64{from, to})
		if !r.settled || (r.settledAt != 0 && (r.settledAt < from || r.settledAt > to)) {
			return &temporpc.JSONRPCResponse{Result: []any{}}, nil
		}
		entry := map[string]any{"transactionHash": zoneTestSettlement}
		if r.bounced {
			data, err := zone.PortalABI.Events["WithdrawalProcessed"].Inputs.NonIndexed().Pack(common.HexToAddress(testCurrency), big.NewInt(1_000_000), false)
			if err != nil {
				return nil, err
			}
			entry["data"] = hexutil.Encode(data)
		}
		return &temporpc.JSONRPCResponse{Result: []any{entry}}, nil
	}
	return nil, fmt.Errorf("tempo: %s", method)
}

func allowAll(context.Context, tempo.ChargeRequest) error { return nil }

func newZoneMethod(t *testing.T, zoneRPC *zoneTestRPC, chainID int64, configure func(*Config)) (*Method, *zoneTestTempoRPC) {
	t.Helper()
	tempoRPC := &zoneTestTempoRPC{settled: true}
	config := Config{
		PrivateKey:    testPrivateKey,
		RPC:           tempoRPC,
		PaymentPolicy: allowAll,
		Zone:          &Zone{RPC: zoneRPC, ChainID: chainID},
	}
	if configure != nil {
		configure(&config)
	}
	method, err := New(config)
	require.NoError(t, err)
	method.zoneReceiptTimeout = 50 * time.Millisecond
	return method, tempoRPC
}

func zoneChallenge(t *testing.T, configure func(*tempo.ChargeRequestParams)) *mpp.Challenge {
	t.Helper()
	params := tempo.ChargeRequestParams{
		Amount:    "1",
		Currency:  testCurrency,
		Recipient: testRecipient,
		ChainID:   42431,
		Zones:     &tempo.Zones{IDs: []uint32{3}},
	}
	if configure != nil {
		configure(&params)
	}
	return buildChallenge(t, params)
}

func TestZonePaysFromZoneOnChallengeChain(t *testing.T) {
	t.Parallel()
	for name, challenge := range map[string]func(t *testing.T) *mpp.Challenge{
		"listed zone": func(t *testing.T) *mpp.Challenge { return zoneChallenge(t, nil) },
		"any zone": func(t *testing.T) *mpp.Challenge {
			return zoneChallenge(t, func(p *tempo.ChargeRequestParams) { p.Zones = &tempo.Zones{Any: true} })
		},
		// MCP challenges are not HTTP headers, so their fields may hold line breaks.
		"line breaks in description": func(t *testing.T) *mpp.Challenge {
			c := zoneChallenge(t, nil)
			c.Description = "line one\nline two"
			return c
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			zoneRPC := &zoneTestRPC{mode: "fail"}
			method, _ := newZoneMethod(t, zoneRPC, zoneTestChainID, nil)
			_, err := method.CreateCredential(context.Background(), challenge(t))
			assert.ErrorContains(t, err, "zone used")
		})
	}
}

func TestZonePaysOnTempoWhenZoneIsNotUsable(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		zoneChainID int64
		configure   func(*Config)
		request     func(*tempo.ChargeRequestParams)
		expires     time.Duration
	}{
		"challenge does not accept the Zone":   {zoneChainID: 1_424_310_004},
		"Zone settles on another parent chain": {zoneChainID: 421_700_003},
		"challenge only accepts pull payments": {request: func(p *tempo.ChargeRequestParams) {
			p.SupportedModes = []tempo.ChargeMode{tempo.ChargeModePull}
		}},
		"client asks for pull mode": {configure: func(c *Config) { c.CredentialType = tempo.CredentialTypeTransaction }},
		"challenge has splits": {request: func(p *tempo.ChargeRequestParams) {
			p.Splits = []tempo.SplitParams{{Amount: "0.1", Recipient: testRecipient}}
		}},
		"challenge expires before the withdrawal could settle": {
			configure: func(c *Config) { c.Zone.SettlementTimeout = time.Minute },
			expires:   30 * time.Second,
		},
		"challenge leaves no time to send the withdrawal": {
			configure: func(c *Config) { c.Zone.SettlementTimeout = time.Minute },
			expires:   65 * time.Second,
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			zoneRPC := &zoneTestRPC{mode: "fail"}
			chainID := tc.zoneChainID
			if chainID == 0 {
				chainID = zoneTestChainID
			}
			method, _ := newZoneMethod(t, zoneRPC, chainID, tc.configure)
			challenge := zoneChallenge(t, tc.request)
			if tc.expires != 0 {
				challenge.Expires = mpp.ExpiresIn(tc.expires)
			}
			_, err := method.CreateCredential(context.Background(), challenge)
			require.Error(t, err)
			assert.NotContains(t, err.Error(), "zone used")
			assert.Zero(t, zoneRPC.callCount())
		})
	}
}

func TestZoneConfigValidation(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		zone *Zone
		want string
	}{
		"negative settlement timeout":        {&Zone{RPC: &zoneTestRPC{}, ChainID: zoneTestChainID, SettlementTimeout: -time.Second}, "SettlementTimeout"},
		"zone ID of another chain":           {&Zone{RPC: &zoneTestRPC{}, ChainID: zoneTestChainID, ZoneID: 4}, "does not match Zone 3"},
		"missing RPC":                        {&Zone{ChainID: zoneTestChainID}, "RPC"},
		"missing chain ID":                   {&Zone{RPC: &zoneTestRPC{}}, "ChainID"},
		"unknown chain scheme":               {&Zone{RPC: &zoneTestRPC{}, ChainID: 42431}, "Zone ID"},
		"unknown parent chain":               {&Zone{RPC: &zoneTestRPC{}, ChainID: 42431, ZoneID: 3}, "ParentChainID"},
		"zone ID in chain ID range":          {&Zone{RPC: &zoneTestRPC{}, ChainID: 42431, ZoneID: 421_700_003, ParentChainID: 42431}, "invalid Zone ID"},
		"parent chain of another Zone chain": {&Zone{RPC: &zoneTestRPC{}, ChainID: zoneTestChainID, ParentChainID: tempo.ChainIDMainnet}, "does not match parent chain 42431"},
	} {
		_, err := New(Config{PrivateKey: testPrivateKey, Zone: tc.zone})
		assert.ErrorContains(t, err, tc.want, name)
	}
	_, err := New(Config{PrivateKey: testPrivateKey, Zone: &Zone{RPC: &zoneTestRPC{}, ChainID: 42431, ZoneID: 3, ParentChainID: 42431}})
	assert.NoError(t, err)
	_, err = New(Config{PrivateKey: testPrivateKey, Zone: &Zone{RPC: &zoneTestRPC{}, ChainID: zoneTestChainID, ParentChainID: tempo.ChainIDModerato}})
	assert.NoError(t, err)
}

func TestZoneCreatesCredentialFromSettledWithdrawal(t *testing.T) {
	t.Parallel()
	zoneRPC := &zoneTestRPC{mode: "ok"}
	method, tempoRPC := newZoneMethod(t, zoneRPC, zoneTestChainID, nil)
	challenge := zoneChallenge(t, nil)
	credential, err := method.CreateCredential(context.Background(), challenge)
	require.NoError(t, err)

	payload, err := tempo.ParseChargeCredentialPayload(credential.Payload)
	require.NoError(t, err)
	require.Len(t, zoneRPC.sent, 1)
	signer, err := temposigner.NewSigner(testPrivateKey)
	require.NoError(t, err)
	txHash := crypto.Keccak256Hash(common.FromHex(zoneRPC.sent[0]))
	assert.Equal(t, tempo.ChargeCredentialPayload{
		Type:      tempo.CredentialTypeZone,
		Hash:      zoneTestSettlement,
		SenderTag: zone.SenderTag(signer.Address(), txHash, 1).Hex(),
		ZoneID:    3,
	}, payload)
	assert.Empty(t, credential.Source)
	// The method waited for the portal's event for this recipient and tag.
	assert.Equal(t, common.BytesToHash(common.HexToAddress(testRecipient).Bytes()).Hex(), tempoRPC.topics[1])
	assert.Equal(t, payload.SenderTag, tempoRPC.topics[2])

	// The withdrawal approves the amount plus fee and withdraws the amount to
	// the recipient with the challenge's memo, without a callback.
	raw := zoneRPC.sent[0]
	assert.Contains(t, []byte{27, 28}, common.FromHex(raw)[len(common.FromHex(raw))-1])
	tx, err := tempotx.Deserialize(raw)
	require.NoError(t, err)
	assert.Equal(t, big.NewInt(zoneTestChainID), tx.ChainID)
	require.Len(t, tx.Calls, 2)
	assert.Equal(t, common.HexToAddress(testCurrency), *tx.Calls[0].To)
	assert.Equal(t, fmt.Sprintf("0x095ea7b3%064x%064x", zone.OutboxAddress.Big(), 1_000_000+zoneTestFee), hexutil.Encode(tx.Calls[0].Data))
	assert.Equal(t, zone.OutboxAddress, *tx.Calls[1].To)
	values, err := zone.OutboxABI.Methods["requestWithdrawal"].Inputs.Unpack(tx.Calls[1].Data[4:])
	require.NoError(t, err)
	assert.Equal(t, common.HexToAddress(testCurrency), values[0])
	assert.Equal(t, common.HexToAddress(testRecipient), values[1])
	assert.Equal(t, "1000000", values[2].(*big.Int).String())
	memo := values[3].([32]byte)
	assert.True(t, tempo.VerifyAttributionChallenge(hexutil.Encode(memo[:]), challenge.ID))
	assert.True(t, tempo.VerifyAttributionServer(hexutil.Encode(memo[:]), challenge.Realm))
	assert.Equal(t, uint64(0), values[4])
	assert.Equal(t, signer.Address(), values[5])
}

func TestZoneResumesSettlementWaitWithoutWithdrawingAgain(t *testing.T) {
	t.Parallel()
	zoneRPC := &zoneTestRPC{mode: "ok"}
	method, tempoRPC := newZoneMethod(t, zoneRPC, zoneTestChainID, func(c *Config) { c.Zone.SettlementTimeout = time.Nanosecond })
	tempoRPC.settled = false
	challenge := zoneChallenge(t, nil)

	_, err := method.CreateCredential(context.Background(), challenge)
	var pending *ZoneSettlementPendingError
	require.ErrorAs(t, err, &pending)
	assert.Equal(t, challenge.ID, pending.ChallengeID)
	assert.Same(t, challenge, pending.Challenge)
	assert.Equal(t, uint32(3), pending.ZoneID)
	assert.NotEmpty(t, pending.SenderTag)

	tempoRPC.mu.Lock()
	tempoRPC.settled = true
	tempoRPC.mu.Unlock()
	credential, err := method.CreateCredential(context.Background(), challenge)
	require.NoError(t, err)
	assert.Equal(t, pending.SenderTag, credential.Payload["senderTag"])
	assert.Len(t, zoneRPC.sent, 1)
}

// A retry of a challenge whose withdrawal settled after the first wait gave
// up searches from the first wait's start block, even after the Tempo head
// moved further than the lookback window.
func TestZoneResumedSettlementWaitSearchesFromFirstStartBlock(t *testing.T) {
	t.Parallel()
	zoneRPC := &zoneTestRPC{mode: "ok"}
	method, tempoRPC := newZoneMethod(t, zoneRPC, zoneTestChainID, func(c *Config) { c.Zone.SettlementTimeout = time.Nanosecond })
	tempoRPC.settled = false
	tempoRPC.head = 10_000
	challenge := zoneChallenge(t, nil)

	_, err := method.CreateCredential(context.Background(), challenge)
	var pending *ZoneSettlementPendingError
	require.ErrorAs(t, err, &pending)

	tempoRPC.mu.Lock()
	tempoRPC.settled = true
	tempoRPC.settledAt = 10_001
	tempoRPC.head = 10_001 + 7_200 + 7_200
	tempoRPC.ranges = nil
	tempoRPC.mu.Unlock()
	credential, err := method.CreateCredential(context.Background(), challenge)
	require.NoError(t, err)
	assert.Equal(t, zoneTestSettlement, credential.Payload["hash"])
	assert.Len(t, zoneRPC.sent, 1)
	tempoRPC.mu.Lock()
	defer tempoRPC.mu.Unlock()
	assert.Equal(t, [][2]uint64{{10_000 - 7_200, 10_000}, {10_001, 17_201}}, tempoRPC.ranges)
}

// A receipt status of 1 in any form is a withdrawal that went through.
func TestZoneAcceptsSuccessReceiptStatusForms(t *testing.T) {
	t.Parallel()
	for _, status := range []any{"0x01", "0X1", float64(1), json.Number("1")} {
		zoneRPC := &zoneTestRPC{mode: "ok", status: status}
		method, _ := newZoneMethod(t, zoneRPC, zoneTestChainID, nil)
		credential, err := method.CreateCredential(context.Background(), zoneChallenge(t, nil))
		require.NoError(t, err, "%v", status)
		assert.NotEmpty(t, credential.Payload["senderTag"], "%v", status)
	}
}

// A receipt without a status, or with one that is not 0 or 1, leaves the
// withdrawal's outcome unknown: a retry looks for the receipt again instead
// of withdrawing again.
func TestZoneDoesNotWithdrawAgainAfterReceiptWithUnknownStatus(t *testing.T) {
	t.Parallel()
	for _, configure := range []func(*zoneTestRPC){
		func(r *zoneTestRPC) { r.noStatus = true },
		func(r *zoneTestRPC) { r.status = "success" },
		func(r *zoneTestRPC) { r.status = "0x2" },
		func(r *zoneTestRPC) { r.status = "" },
	} {
		zoneRPC := &zoneTestRPC{mode: "ok"}
		configure(zoneRPC)
		method, _ := newZoneMethod(t, zoneRPC, zoneTestChainID, nil)
		challenge := zoneChallenge(t, nil)
		_, err := method.CreateCredential(context.Background(), challenge)
		var unknown *ZoneWithdrawalUnknownError
		require.ErrorAs(t, err, &unknown)
		assert.ErrorContains(t, err, "receipt status")
		_, err = method.CreateCredential(context.Background(), challenge)
		require.ErrorAs(t, err, &unknown)
		assert.Len(t, zoneRPC.sent, 1)

		zoneRPC.mu.Lock()
		zoneRPC.status, zoneRPC.noStatus = "0x1", false
		zoneRPC.mu.Unlock()
		_, err = method.CreateCredential(context.Background(), challenge)
		require.NoError(t, err)
		assert.Len(t, zoneRPC.sent, 1)
	}
}

func TestReceiptStatus(t *testing.T) {
	t.Parallel()
	for value, want := range map[any]uint64{"0x0": 0, "0x00": 0, "0x1": 1, "0x01": 1, float64(0): 0, float64(1): 1, json.Number("0"): 0} {
		status, ok := receiptStatus(value)
		assert.True(t, ok, "%v", value)
		assert.Equal(t, want, status, "%v", value)
	}
	for _, value := range []any{nil, "", "0x", "1", "0xzz", float64(-1), 1.5, true, json.Number("-1")} {
		_, ok := receiptStatus(value)
		assert.False(t, ok, "%v", value)
	}
}

func TestZoneDoesNotWithdrawAgainAfterUnknownOutcome(t *testing.T) {
	t.Parallel()
	zoneRPC := &zoneTestRPC{mode: "send"}
	method, _ := newZoneMethod(t, zoneRPC, zoneTestChainID, nil)
	challenge := zoneChallenge(t, nil)
	_, err := method.CreateCredential(context.Background(), challenge)
	assert.ErrorContains(t, err, "Zone withdrawal outcome is unknown: transaction 0x")
	var unknown *ZoneWithdrawalUnknownError
	require.ErrorAs(t, err, &unknown)
	assert.Same(t, challenge, unknown.Challenge)
	assert.Contains(t, zoneRPC.calls, "eth_sendRawTransaction")
	// The method looked for the receipt of the signed transaction before it gave up.
	assert.Contains(t, zoneRPC.calls, "eth_getTransactionReceipt")
	// A retry looks for the receipt once more, without sending again.
	calls := zoneRPC.callCount()
	_, err = method.CreateCredential(context.Background(), challenge)
	assert.ErrorContains(t, err, "Zone withdrawal outcome is unknown")
	assert.ErrorContains(t, err, "connection lost")
	assert.Equal(t, calls+1, zoneRPC.callCount())
	assert.Len(t, zoneRPC.sent, 1)
}

// A withdrawal mined after the method stopped waiting for its receipt is
// resumed by a retry of the challenge: the retry finds the receipt and
// returns a credential without withdrawing again.
func TestZoneResumesWithdrawalWhoseReceiptAppearsLater(t *testing.T) {
	t.Parallel()
	zoneRPC := &zoneTestRPC{mode: "send"}
	method, _ := newZoneMethod(t, zoneRPC, zoneTestChainID, nil)
	challenge := zoneChallenge(t, nil)
	_, err := method.CreateCredential(context.Background(), challenge)
	var unknown *ZoneWithdrawalUnknownError
	require.ErrorAs(t, err, &unknown)

	zoneRPC.mu.Lock()
	zoneRPC.mode = "ok"
	zoneRPC.mu.Unlock()
	credential, err := method.CreateCredential(context.Background(), challenge)
	require.NoError(t, err)
	assert.Equal(t, "zone", credential.Payload["type"])
	assert.NotEmpty(t, credential.Payload["senderTag"])
	assert.Len(t, zoneRPC.sent, 1)
	assert.Nil(t, method.pendingZoneWithdrawal(zoneWithdrawalKey(challenge)))
}

// A withdrawal whose late receipt shows a revert moved nothing, so a retry
// after that may withdraw again.
func TestZoneRetriesUnknownWithdrawalThatReverted(t *testing.T) {
	t.Parallel()
	zoneRPC := &zoneTestRPC{mode: "send"}
	method, _ := newZoneMethod(t, zoneRPC, zoneTestChainID, nil)
	challenge := zoneChallenge(t, nil)
	_, err := method.CreateCredential(context.Background(), challenge)
	var unknown *ZoneWithdrawalUnknownError
	require.ErrorAs(t, err, &unknown)

	zoneRPC.mu.Lock()
	zoneRPC.mode = "revert"
	zoneRPC.mu.Unlock()
	_, err = method.CreateCredential(context.Background(), challenge)
	assert.ErrorContains(t, err, "reverted")
	assert.Len(t, zoneRPC.sent, 1)

	zoneRPC.mu.Lock()
	zoneRPC.mode = "ok"
	zoneRPC.mu.Unlock()
	_, err = method.CreateCredential(context.Background(), challenge)
	require.NoError(t, err)
	assert.Len(t, zoneRPC.sent, 2)
}

// Concurrent retries of an unknown withdrawal share one receipt lookup: the
// others wait for it and resume the same withdrawal.
func TestZoneConcurrentRetriesOfUnknownWithdrawalLookUpOnce(t *testing.T) {
	t.Parallel()
	zoneRPC := &zoneTestRPC{mode: "send"}
	method, _ := newZoneMethod(t, zoneRPC, zoneTestChainID, nil)
	challenge := zoneChallenge(t, nil)
	_, err := method.CreateCredential(context.Background(), challenge)
	var unknown *ZoneWithdrawalUnknownError
	require.ErrorAs(t, err, &unknown)

	zoneRPC.mu.Lock()
	zoneRPC.mode = "ok"
	zoneRPC.looking = make(chan struct{}, 4)
	zoneRPC.release = make(chan struct{})
	receipts := zoneRPC.receipts
	zoneRPC.mu.Unlock()
	var wg sync.WaitGroup
	credentials := make([]*mpp.Credential, 3)
	errs := make([]error, 3)
	wg.Go(func() { credentials[0], errs[0] = method.CreateCredential(context.Background(), challenge) })
	<-zoneRPC.looking
	for i := 1; i < len(errs); i++ {
		wg.Go(func() { credentials[i], errs[i] = method.CreateCredential(context.Background(), challenge) })
	}
	// Give the other calls time to look up the receipt if they were going to.
	time.Sleep(100 * time.Millisecond)
	close(zoneRPC.release)
	wg.Wait()
	for i, err := range errs {
		require.NoError(t, err)
		assert.Equal(t, credentials[0].Payload["senderTag"], credentials[i].Payload["senderTag"])
	}
	zoneRPC.mu.Lock()
	defer zoneRPC.mu.Unlock()
	assert.Equal(t, 1, zoneRPC.receipts-receipts)
	assert.Len(t, zoneRPC.sent, 1)
}

// The pending withdrawal is keyed on the challenge's HMAC-bound fields, so
// the same challenge with another description, which nothing binds, does not
// withdraw again.
func TestZoneDoesNotWithdrawAgainForChallengeWithOtherDescription(t *testing.T) {
	t.Parallel()
	zoneRPC := &zoneTestRPC{mode: "send"}
	method, _ := newZoneMethod(t, zoneRPC, zoneTestChainID, nil)
	challenge := zoneChallenge(t, nil)
	_, err := method.CreateCredential(context.Background(), challenge)
	require.ErrorContains(t, err, "Zone withdrawal outcome is unknown")
	calls := zoneRPC.callCount()
	described := *challenge
	described.Description = "another description"
	_, err = method.CreateCredential(context.Background(), &described)
	assert.ErrorContains(t, err, "Zone withdrawal outcome is unknown")
	// Only one receipt lookup, no new send.
	assert.Equal(t, calls+1, zoneRPC.callCount())
	assert.Len(t, zoneRPC.sent, 1)
}

func TestZoneRetriesRevertedWithdrawal(t *testing.T) {
	t.Parallel()
	zoneRPC := &zoneTestRPC{mode: "revert"}
	method, _ := newZoneMethod(t, zoneRPC, zoneTestChainID, nil)
	challenge := zoneChallenge(t, nil)
	for range 2 {
		_, err := method.CreateCredential(context.Background(), challenge)
		assert.ErrorContains(t, err, "reverted")
	}
	assert.Len(t, zoneRPC.sent, 2)
}

func TestZoneRetriesWithdrawalThatFailedBeforeSending(t *testing.T) {
	t.Parallel()
	zoneRPC := &zoneTestRPC{mode: "prepare"}
	method, _ := newZoneMethod(t, zoneRPC, zoneTestChainID, nil)
	challenge := zoneChallenge(t, nil)
	_, err := method.CreateCredential(context.Background(), challenge)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "outcome is unknown")
	calls := zoneRPC.callCount()
	_, err = method.CreateCredential(context.Background(), challenge)
	require.Error(t, err)
	assert.Greater(t, zoneRPC.callCount(), calls)
	assert.NotContains(t, zoneRPC.calls, "eth_sendRawTransaction")
}

func TestZoneSharesOneWithdrawalBetweenConcurrentCalls(t *testing.T) {
	t.Parallel()
	zoneRPC := &zoneTestRPC{mode: "fail"}
	method, _ := newZoneMethod(t, zoneRPC, zoneTestChainID, nil)
	challenge := zoneChallenge(t, nil)
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range errs {
		wg.Go(func() {
			_, errs[i] = method.CreateCredential(context.Background(), challenge)
		})
	}
	wg.Wait()
	for _, err := range errs {
		assert.ErrorContains(t, err, "zone used")
	}
	assert.Equal(t, 1, zoneRPC.callCount())
}

// Concurrent calls for challenges that differ only in their description
// share one withdrawal key, so they share one withdrawal: the second waits
// for the first's send instead of sending its own.
func TestZoneSharesOneWithdrawalBetweenConcurrentDescriptionVariants(t *testing.T) {
	t.Parallel()
	zoneRPC := &zoneTestRPC{mode: "ok", sending: make(chan struct{}, 2), release: make(chan struct{})}
	method, _ := newZoneMethod(t, zoneRPC, zoneTestChainID, nil)
	challenge := zoneChallenge(t, nil)
	described := *challenge
	described.Description = "another description"
	var wg sync.WaitGroup
	credentials := make([]*mpp.Credential, 2)
	errs := make([]error, 2)
	wg.Go(func() { credentials[0], errs[0] = method.CreateCredential(context.Background(), challenge) })
	<-zoneRPC.sending
	wg.Go(func() { credentials[1], errs[1] = method.CreateCredential(context.Background(), &described) })
	// Give the second call time to send if it were going to.
	time.Sleep(100 * time.Millisecond)
	close(zoneRPC.release)
	wg.Wait()
	require.NoError(t, errs[0])
	require.NoError(t, errs[1])
	assert.Len(t, zoneRPC.sent, 1)
	assert.Equal(t, credentials[0].Payload["senderTag"], credentials[1].Payload["senderTag"])
}

// A Zone can refuse a send, such as for insufficient funds, while it (or
// another node behind its RPC) holds the transaction pending: the method waits for its receipt rather than
// deciding nothing was withdrawn, which would let a retry withdraw again.
func TestZoneWaitsForPendingWithdrawalDespiteSendError(t *testing.T) {
	t.Parallel()
	zoneRPC := &zoneTestRPC{mode: "pending"}
	method, _ := newZoneMethod(t, zoneRPC, zoneTestChainID, nil)
	method.zoneReceiptTimeout = 5 * time.Second
	credential, err := method.CreateCredential(context.Background(), zoneChallenge(t, nil))
	require.NoError(t, err)
	assert.Equal(t, "zone", credential.Payload["type"])
	assert.Len(t, zoneRPC.sent, 1)
}

// A pending withdrawal whose receipt never shows leaves its outcome unknown,
// so a retry does not withdraw again.
func TestZoneDoesNotWithdrawAgainWhilePendingDespiteSendError(t *testing.T) {
	t.Parallel()
	zoneRPC := &zoneTestRPC{mode: "stuck"}
	method, _ := newZoneMethod(t, zoneRPC, zoneTestChainID, nil)
	challenge := zoneChallenge(t, nil)
	for range 2 {
		_, err := method.CreateCredential(context.Background(), challenge)
		var unknown *ZoneWithdrawalUnknownError
		require.ErrorAs(t, err, &unknown)
		assert.ErrorContains(t, err, "insufficient funds")
	}
	assert.Len(t, zoneRPC.sent, 1)
}

// A Zone that answers the send with a refusal, such as insufficient funds,
// and shows neither the receipt nor the transaction does not prove nothing was
// withdrawn: another node behind a load-balanced or lagging RPC may have taken
// it. The outcome stays unknown, so a retry looks for the receipt without
// withdrawing again, as in mppx.
func TestZoneDoesNotWithdrawAgainAfterTheZoneRefusedTheSend(t *testing.T) {
	t.Parallel()
	zoneRPC := &zoneTestRPC{mode: "reject"}
	method, _ := newZoneMethod(t, zoneRPC, zoneTestChainID, nil)
	challenge := zoneChallenge(t, nil)
	for range 2 {
		_, err := method.CreateCredential(context.Background(), challenge)
		var unknown *ZoneWithdrawalUnknownError
		require.ErrorAs(t, err, &unknown)
		assert.ErrorContains(t, err, "insufficient funds")
	}
	assert.Len(t, zoneRPC.sent, 1)
	assert.NotContains(t, zoneRPC.calls, "eth_getTransactionByHash")
}

// A JSON-RPC send error that names no reason the Zone refused the
// transaction leaves its outcome unknown even when the Zone shows neither
// its receipt nor the transaction: a load-balanced or still-indexing Zone
// may not show a transaction it took yet. A retry looks for the receipt once
// without withdrawing again.
func TestZoneDoesNotWithdrawAgainAfterUnrecognizedSendError(t *testing.T) {
	t.Parallel()
	zoneRPC := &zoneTestRPC{mode: "internal"}
	method, _ := newZoneMethod(t, zoneRPC, zoneTestChainID, nil)
	challenge := zoneChallenge(t, nil)
	_, err := method.CreateCredential(context.Background(), challenge)
	var unknown *ZoneWithdrawalUnknownError
	require.ErrorAs(t, err, &unknown)
	assert.ErrorContains(t, err, "internal error")
	calls := zoneRPC.callCount()
	_, err = method.CreateCredential(context.Background(), challenge)
	require.ErrorAs(t, err, &unknown)
	assert.ErrorContains(t, err, "internal error")
	assert.Equal(t, calls+1, zoneRPC.callCount())
	assert.Equal(t, "eth_getTransactionReceipt", zoneRPC.calls[len(zoneRPC.calls)-1])
	assert.Len(t, zoneRPC.sent, 1)
}

// A Zone can answer a send it took with a JSON-RPC error, such as "nonce too
// low" once the transaction is included, or reth's "already imported": the
// withdrawal's receipt shows it was taken, so it pays the challenge rather
// than failing as if nothing were withdrawn, which would let a retry pay again.
func TestZoneUsesWithdrawalTheZoneTookDespiteSendError(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"nonce", "imported"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			zoneRPC := &zoneTestRPC{mode: mode}
			method, _ := newZoneMethod(t, zoneRPC, zoneTestChainID, nil)
			challenge := zoneChallenge(t, nil)
			credential, err := method.CreateCredential(context.Background(), challenge)
			require.NoError(t, err)
			assert.Equal(t, "zone", credential.Payload["type"])
			assert.Len(t, zoneRPC.sent, 1)
		})
	}
}

// A credential too large to send is refused before anything is withdrawn.
func TestZoneRefusesOversizedCredentialBeforeWithdrawing(t *testing.T) {
	t.Parallel()
	zoneRPC := &zoneTestRPC{mode: "ok"}
	method, _ := newZoneMethod(t, zoneRPC, zoneTestChainID, nil)
	challenge := zoneChallenge(t, nil)
	challenge.Opaque = map[string]string{"pad": strings.Repeat("a", 70_000)}
	_, err := method.CreateCredential(context.Background(), challenge)
	require.Error(t, err)
	assert.Zero(t, zoneRPC.callCount())
}

// The withdrawal pays gas in Config.FeeToken but still moves the currency.
func TestZonePaysGasInConfiguredFeeToken(t *testing.T) {
	t.Parallel()
	zoneRPC := &zoneTestRPC{mode: "ok"}
	method, _ := newZoneMethod(t, zoneRPC, zoneTestChainID, func(c *Config) { c.FeeToken = tempo.OUSDAddress })
	_, err := method.CreateCredential(context.Background(), zoneChallenge(t, nil))
	require.NoError(t, err)
	require.Len(t, zoneRPC.sent, 1)
	tx, err := tempotx.Deserialize(zoneRPC.sent[0])
	require.NoError(t, err)
	assert.Equal(t, common.HexToAddress(tempo.OUSDAddress), tx.FeeToken)
	require.Len(t, tx.Calls, 2)
	assert.Equal(t, common.HexToAddress(testCurrency), *tx.Calls[0].To)
}

// A Zone withdrawal is valid only until the challenge expiry less the
// settlement timeout: if the Zone includes it late, it cannot move funds once
// its settlement could no longer finish before the challenge expires.
func TestZoneWithdrawalExpiresBeforeChallengeSettlementDeadline(t *testing.T) {
	t.Parallel()
	zoneRPC := &zoneTestRPC{mode: "ok"}
	method, _ := newZoneMethod(t, zoneRPC, zoneTestChainID, func(c *Config) { c.Zone.SettlementTimeout = time.Minute })
	challenge := zoneChallenge(t, nil)
	_, err := method.CreateCredential(context.Background(), challenge)
	require.NoError(t, err)
	require.Len(t, zoneRPC.sent, 1)
	tx, err := tempotx.Deserialize(zoneRPC.sent[0])
	require.NoError(t, err)
	expiry, err := mpp.ParseExpires(challenge.Expires)
	require.NoError(t, err)
	assert.Equal(t, uint64(expiry.Add(-time.Minute).Unix()), tx.ValidBefore)
	assert.Zero(t, tx.NonceKey.Sign(), "a Zone withdrawal keeps its sequential nonce")
}

func TestZoneValidBefore(t *testing.T) {
	t.Parallel()
	validBefore, err := zoneValidBefore("", time.Minute)
	require.NoError(t, err)
	assert.Zero(t, validBefore)
	validBefore, err = zoneValidBefore("2026-01-01T00:01:00Z", time.Minute)
	require.NoError(t, err)
	assert.Equal(t, uint64(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Unix()), validBefore)
	_, err = zoneValidBefore("not a time", time.Minute)
	assert.Error(t, err)
}

// A successful receipt that does not show the signed withdrawal (no
// WithdrawalRequested event, one that does not decode or does not match the
// signed sender, token, recipient, amount or memo, or a receipt of another
// transaction) leaves the withdrawal's outcome unknown: a retry looks for the
// receipt again, and resumes the withdrawal once it shows it, without
// withdrawing again.
func TestZoneDoesNotWithdrawAgainAfterReceiptWithoutMatchingEvent(t *testing.T) {
	t.Parallel()
	for _, event := range []string{"none", "garbled", "sender", "token", "to", "amount", "memo", "hash"} {
		t.Run(event, func(t *testing.T) {
			t.Parallel()
			zoneRPC := &zoneTestRPC{mode: "ok", event: event}
			method, _ := newZoneMethod(t, zoneRPC, zoneTestChainID, nil)
			challenge := zoneChallenge(t, nil)
			_, err := method.CreateCredential(context.Background(), challenge)
			var unknown *ZoneWithdrawalUnknownError
			require.ErrorAs(t, err, &unknown)
			require.Len(t, zoneRPC.sent, 1)
			assert.Equal(t, crypto.Keccak256Hash(common.FromHex(zoneRPC.sent[0])).Hex(), unknown.TransactionHash)

			zoneRPC.mu.Lock()
			receipts := zoneRPC.receipts
			zoneRPC.mu.Unlock()
			_, err = method.CreateCredential(context.Background(), challenge)
			require.ErrorAs(t, err, &unknown)
			zoneRPC.mu.Lock()
			assert.Equal(t, receipts+1, zoneRPC.receipts, "a retry looks for the receipt again")
			zoneRPC.event = ""
			zoneRPC.mu.Unlock()
			assert.Len(t, zoneRPC.sent, 1)

			credential, err := method.CreateCredential(context.Background(), challenge)
			require.NoError(t, err)
			assert.Equal(t, "zone", credential.Payload["type"])
			assert.Len(t, zoneRPC.sent, 1)
		})
	}
}

// The method looks for the receipt of the transaction it signed, whatever
// hash the Zone answers the send with.
func TestZoneLooksUpReceiptOfSignedTransaction(t *testing.T) {
	t.Parallel()
	zoneRPC := &zoneTestRPC{mode: "ok", answerHash: common.HexToHash("0x02").Hex()}
	method, _ := newZoneMethod(t, zoneRPC, zoneTestChainID, nil)
	credential, err := method.CreateCredential(context.Background(), zoneChallenge(t, nil))
	require.NoError(t, err)
	require.Len(t, zoneRPC.sent, 1)
	signer, err := temposigner.NewSigner(testPrivateKey)
	require.NoError(t, err)
	txHash := crypto.Keccak256Hash(common.FromHex(zoneRPC.sent[0]))
	assert.Equal(t, zone.SenderTag(signer.Address(), txHash, 1).Hex(), credential.Payload["senderTag"])
}

// Preparing the withdrawal can take long enough that it could no longer
// settle before the challenge expires: the method checks the expiry again
// just before sending and sends nothing.
func TestZoneRechecksExpiryBeforeSending(t *testing.T) {
	t.Parallel()
	clock := &fakeClock{now: time.Now()}
	zoneRPC := &zoneTestRPC{mode: "ok", onNonce: func() { clock.Advance(4 * time.Minute) }}
	method, _ := newZoneMethod(t, zoneRPC, zoneTestChainID, func(c *Config) { c.Zone.SettlementTimeout = time.Minute })
	method.now = clock.Now
	challenge := zoneChallenge(t, nil)
	_, err := method.CreateCredential(context.Background(), challenge)
	require.ErrorIs(t, err, ErrZoneChallengeExpiresTooSoon)
	assert.Contains(t, zoneRPC.calls, "eth_getTransactionCount")
	assert.NotContains(t, zoneRPC.calls, "eth_sendRawTransaction")
	assert.Empty(t, zoneRPC.sent)
	assert.Nil(t, method.pendingZoneWithdrawal(zoneWithdrawalKey(challenge)))
}

// Concurrent payments of different challenges from one Zone signer read the
// Zone nonce one after the other, so each withdrawal signs its own nonce and
// both are included.
func TestZoneConcurrentPaymentsOfDifferentChallengesUseDistinctNonces(t *testing.T) {
	t.Parallel()
	// The pause widens the window between the nonce lookup and the send in
	// which another payment could read the same nonce.
	zoneRPC := &zoneTestRPC{mode: "ok", trackNonce: true, onNonce: func() { time.Sleep(20 * time.Millisecond) }}
	method, _ := newZoneMethod(t, zoneRPC, zoneTestChainID, nil)
	challenges := []*mpp.Challenge{zoneChallenge(t, nil), zoneChallenge(t, func(p *tempo.ChargeRequestParams) { p.Amount = "2" })}
	require.NotEqual(t, challenges[0].ID, challenges[1].ID)
	var wg sync.WaitGroup
	credentials := make([]*mpp.Credential, len(challenges))
	errs := make([]error, len(challenges))
	for i := range challenges {
		wg.Go(func() {
			credentials[i], errs[i] = method.CreateCredential(context.Background(), challenges[i])
		})
	}
	wg.Wait()
	for i := range challenges {
		require.NoError(t, errs[i])
		require.NotNil(t, credentials[i])
	}
	require.Len(t, zoneRPC.sent, 2)
	nonces := map[uint64]bool{}
	for _, raw := range zoneRPC.sent {
		tx, err := tempotx.Deserialize(raw)
		require.NoError(t, err)
		nonces[tx.Nonce] = true
	}
	assert.Equal(t, map[uint64]bool{0: true, 1: true}, nonces)
}

// A payment waiting for the nonce lock that is canceled sends nothing, so
// the challenge may be paid again.
func TestZoneCancelledWaitForNonceLockSendsNothing(t *testing.T) {
	t.Parallel()
	zoneRPC := &zoneTestRPC{mode: "ok"}
	method, _ := newZoneMethod(t, zoneRPC, zoneTestChainID, nil)
	method.zone.nonceLock <- struct{}{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	challenge := zoneChallenge(t, nil)
	_, err := method.CreateCredential(ctx, challenge)
	require.ErrorIs(t, err, context.Canceled)
	assert.Empty(t, zoneRPC.sent)
	assert.Nil(t, method.pendingZoneWithdrawal(zoneWithdrawalKey(challenge)))
	<-method.zone.nonceLock
	_, err = method.CreateCredential(context.Background(), challenge)
	require.NoError(t, err)
	assert.Len(t, zoneRPC.sent, 1)
}

// A withdrawal whose payout bounced on Tempo returns
// *ZoneWithdrawalBouncedError without a credential, and paying the challenge
// again withdraws again.
func TestZoneBouncedWithdrawalFailsAndMayBePaidAgain(t *testing.T) {
	t.Parallel()
	zoneRPC := &zoneTestRPC{mode: "ok"}
	method, tempoRPC := newZoneMethod(t, zoneRPC, zoneTestChainID, nil)
	tempoRPC.bounced = true
	challenge := zoneChallenge(t, nil)
	credential, err := method.CreateCredential(context.Background(), challenge)
	require.Nil(t, credential)
	var bounced *ZoneWithdrawalBouncedError
	require.ErrorAs(t, err, &bounced)
	assert.ErrorContains(t, err, "bounced")
	signer, err := temposigner.NewSigner(testPrivateKey)
	require.NoError(t, err)
	require.Len(t, zoneRPC.sent, 1)
	senderTag := zone.SenderTag(signer.Address(), crypto.Keccak256Hash(common.FromHex(zoneRPC.sent[0])), 1).Hex()
	assert.Equal(t, &ZoneWithdrawalBouncedError{ChallengeID: challenge.ID, SenderTag: senderTag, ZoneID: 3}, bounced)
	assert.Nil(t, method.pendingZoneWithdrawal(zoneWithdrawalKey(challenge)))

	tempoRPC.mu.Lock()
	tempoRPC.bounced = false
	tempoRPC.mu.Unlock()
	credential, err = method.CreateCredential(context.Background(), challenge)
	require.NoError(t, err)
	require.NotNil(t, credential)
	assert.Len(t, zoneRPC.sent, 2)
}

// A retry of a challenge whose withdrawal is pending does not run the
// payment policy again: the withdrawal was authorized when it was sent.
func TestZoneResumedPaymentDoesNotRerunPolicy(t *testing.T) {
	t.Parallel()
	zoneRPC := &zoneTestRPC{mode: "ok"}
	var mu sync.Mutex
	authorized := 0
	method, tempoRPC := newZoneMethod(t, zoneRPC, zoneTestChainID, func(c *Config) {
		c.Zone.SettlementTimeout = time.Nanosecond
		c.PaymentPolicy = func(context.Context, tempo.ChargeRequest) error {
			mu.Lock()
			defer mu.Unlock()
			authorized++
			return nil
		}
	})
	tempoRPC.settled = false
	challenge := zoneChallenge(t, nil)
	_, err := method.CreateCredential(context.Background(), challenge)
	var pending *ZoneSettlementPendingError
	require.ErrorAs(t, err, &pending)

	tempoRPC.mu.Lock()
	tempoRPC.settled = true
	tempoRPC.mu.Unlock()
	_, err = method.CreateCredential(context.Background(), challenge)
	require.NoError(t, err)
	assert.Equal(t, 1, authorized)
	assert.Len(t, zoneRPC.sent, 1)
}

// A request whose deadline leaves too little time to find the receipt sends
// no withdrawal.
func TestZoneRefusesToSendCloseToTheRequestDeadline(t *testing.T) {
	t.Parallel()
	zoneRPC := &zoneTestRPC{mode: "ok"}
	method, _ := newZoneMethod(t, zoneRPC, zoneTestChainID, nil)
	challenge := zoneChallenge(t, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := method.CreateCredential(ctx, challenge)
	require.ErrorIs(t, err, ErrZoneDeadlineTooSoon)
	assert.NotContains(t, zoneRPC.calls, "eth_sendRawTransaction")
	assert.Empty(t, zoneRPC.sent)
	assert.Nil(t, method.pendingZoneWithdrawal(zoneWithdrawalKey(challenge)))
}

// Concurrent payments of one challenge share one withdrawal, and the policy
// authorizes it once: only the call that sends it runs the policy.
func TestZoneConcurrentPaymentsOfOneChallengeRunPolicyOnce(t *testing.T) {
	t.Parallel()
	zoneRPC := &zoneTestRPC{mode: "ok"}
	var mu sync.Mutex
	authorized := 0
	entered := make(chan struct{})
	release := make(chan struct{})
	method, _ := newZoneMethod(t, zoneRPC, zoneTestChainID, func(c *Config) {
		c.PaymentPolicy = func(context.Context, tempo.ChargeRequest) error {
			mu.Lock()
			authorized++
			first := authorized == 1
			mu.Unlock()
			if first {
				// Hold the first payment's claim until the others wait on it.
				close(entered)
				<-release
			}
			return nil
		}
	})
	challenge := zoneChallenge(t, nil)
	var wg sync.WaitGroup
	pay := func() {
		defer wg.Done()
		_, err := method.CreateCredential(context.Background(), challenge)
		assert.NoError(t, err)
	}
	wg.Add(1)
	go pay()
	<-entered
	for range 7 {
		wg.Add(1)
		go pay()
	}
	time.Sleep(100 * time.Millisecond)
	close(release)
	wg.Wait()
	assert.Equal(t, 1, authorized)
	assert.Len(t, zoneRPC.sent, 1)
}

// A withdrawal the policy refused leaves nothing pending, so the next
// payment of the challenge runs the policy again before sending.
func TestZonePolicyRefusalSendsNothingAndIsAskedAgain(t *testing.T) {
	t.Parallel()
	zoneRPC := &zoneTestRPC{mode: "ok"}
	var mu sync.Mutex
	authorized := 0
	method, _ := newZoneMethod(t, zoneRPC, zoneTestChainID, func(c *Config) {
		c.PaymentPolicy = func(context.Context, tempo.ChargeRequest) error {
			mu.Lock()
			defer mu.Unlock()
			authorized++
			if authorized == 1 {
				return errors.New("over budget")
			}
			return nil
		}
	})
	challenge := zoneChallenge(t, nil)
	_, err := method.CreateCredential(context.Background(), challenge)
	require.ErrorContains(t, err, "payment rejected by policy")
	assert.Empty(t, zoneRPC.sent)
	assert.Nil(t, method.pendingZoneWithdrawal(zoneWithdrawalKey(challenge)))

	_, err = method.CreateCredential(context.Background(), challenge)
	require.NoError(t, err)
	assert.Equal(t, 2, authorized)
	assert.Len(t, zoneRPC.sent, 1)
}

// A withdrawal whose receipt appeared only long after the send is searched
// for on Tempo from the head when it was sent, so a settlement since then is
// still found.
func TestZoneLateReceiptSearchesFromTheSendStartBlock(t *testing.T) {
	t.Parallel()
	zoneRPC := &zoneTestRPC{mode: "send"}
	method, tempoRPC := newZoneMethod(t, zoneRPC, zoneTestChainID, nil)
	tempoRPC.head = 10_000
	challenge := zoneChallenge(t, nil)
	_, err := method.CreateCredential(context.Background(), challenge)
	var unknown *ZoneWithdrawalUnknownError
	require.ErrorAs(t, err, &unknown)

	zoneRPC.mu.Lock()
	zoneRPC.mode = "ok"
	zoneRPC.mu.Unlock()
	tempoRPC.mu.Lock()
	tempoRPC.settledAt = 10_001
	tempoRPC.head = 10_001 + 7_200 + 7_200
	tempoRPC.ranges = nil
	tempoRPC.mu.Unlock()
	credential, err := method.CreateCredential(context.Background(), challenge)
	require.NoError(t, err)
	assert.Equal(t, zoneTestSettlement, credential.Payload["hash"])
	assert.Len(t, zoneRPC.sent, 1)
	tempoRPC.mu.Lock()
	defer tempoRPC.mu.Unlock()
	require.NotEmpty(t, tempoRPC.ranges)
	assert.Equal(t, uint64(10_000-7_200), tempoRPC.ranges[0][0])
}

// A panicking payment policy sends nothing and leaves the challenge payable.
func TestZonePolicyPanicLeavesChallengePayable(t *testing.T) {
	t.Parallel()
	zoneRPC := &zoneTestRPC{mode: "ok"}
	var mu sync.Mutex
	calls := 0
	method, _ := newZoneMethod(t, zoneRPC, zoneTestChainID, func(c *Config) {
		c.PaymentPolicy = func(context.Context, tempo.ChargeRequest) error {
			mu.Lock()
			calls++
			first := calls == 1
			mu.Unlock()
			if first {
				panic("policy bug")
			}
			return nil
		}
	})
	challenge := zoneChallenge(t, nil)
	assert.Panics(t, func() { _, _ = method.CreateCredential(context.Background(), challenge) })
	assert.Empty(t, zoneRPC.sent)
	assert.Nil(t, method.pendingZoneWithdrawal(zoneWithdrawalKey(challenge)))

	_, err := method.CreateCredential(context.Background(), challenge)
	require.NoError(t, err)
	assert.Len(t, zoneRPC.sent, 1)
}

// A withdrawal fee above Zone.MaxWithdrawalFee is refused before signing.
func TestZoneRefusesWithdrawalFeeAboveMax(t *testing.T) {
	t.Parallel()
	zoneRPC := &zoneTestRPC{mode: "ok"}
	method, _ := newZoneMethod(t, zoneRPC, zoneTestChainID, func(c *Config) { c.Zone.MaxWithdrawalFee = big.NewInt(zoneTestFee - 1) })
	_, err := method.CreateCredential(context.Background(), zoneChallenge(t, nil))
	require.ErrorIs(t, err, ErrZoneWithdrawalFeeTooHigh)
	assert.Empty(t, zoneRPC.sent)

	zoneRPC = &zoneTestRPC{mode: "ok"}
	method, _ = newZoneMethod(t, zoneRPC, zoneTestChainID, func(c *Config) { c.Zone.MaxWithdrawalFee = big.NewInt(zoneTestFee) })
	_, err = method.CreateCredential(context.Background(), zoneChallenge(t, nil))
	require.NoError(t, err)
	assert.Len(t, zoneRPC.sent, 1)

	_, err = New(Config{PrivateKey: testPrivateKey, RPC: &zoneTestTempoRPC{}, PaymentPolicy: allowAll,
		Zone: &Zone{RPC: &zoneTestRPC{}, ChainID: zoneTestChainID, MaxWithdrawalFee: big.NewInt(-1)}})
	assert.ErrorContains(t, err, "MaxWithdrawalFee must not be negative")
}
