package chargeclient

import (
	"context"
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
// error; "nonce" and "imported" have it answer a send it took with a
// JSON-RPC error; "pending" has it answer a send it took with a JSON-RPC
// error while the transaction is pending, showing its receipt only on a later
// poll, and "stuck" likewise but never shows a receipt; "revert" reverts the
// withdrawal. When sending is set, a send signals it and then waits for
// release.
type zoneTestRPC struct {
	mode     string
	sending  chan struct{}
	release  chan struct{}
	mu       sync.Mutex
	calls    []string
	sent     []string
	receipts int
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
	if r.mode == "fail" {
		return 0, errors.New("zone used")
	}
	return 0, nil
}

func (r *zoneTestRPC) SendRawTransaction(_ context.Context, serialized string) (string, error) {
	r.record("eth_sendRawTransaction")
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
	case "nonce":
		return "", fmt.Errorf("eth_sendRawTransaction: %w", &temporpc.JSONRPCError{Code: -32003, Message: "nonce too low"})
	case "imported":
		return "", fmt.Errorf("eth_sendRawTransaction: %w", &temporpc.JSONRPCError{Code: -32000, Message: "transaction already imported"})
	case "pending", "stuck":
		return "", fmt.Errorf("eth_sendRawTransaction: %w", &temporpc.JSONRPCError{Code: -32000, Message: "replacement transaction underpriced"})
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
		r.mu.Unlock()
		if r.mode == "send" || r.mode == "reject" || r.mode == "stuck" || (r.mode == "pending" && receipts == 1) {
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

// receipt returns the receipt of the last withdrawal sent.
func (r *zoneTestRPC) receipt(hash string) map[string]any {
	if r.mode == "revert" {
		return map[string]any{"transactionHash": hash, "status": "0x0", "logs": []any{}}
	}
	r.mu.Lock()
	serialized := r.sent[len(r.sent)-1]
	r.mu.Unlock()
	tx, err := tempotx.Deserialize(serialized)
	if err != nil {
		panic(err)
	}
	sender, err := tempotx.VerifySignature(tx)
	if err != nil {
		panic(err)
	}
	data, err := zone.OutboxABI.Events["WithdrawalRequested"].Inputs.NonIndexed().Pack(
		common.HexToAddress(testCurrency), common.HexToAddress(testRecipient), big.NewInt(1_000_000), big.NewInt(zoneTestFee),
		[32]byte{}, uint64(0), uint64(1), []byte{}, []byte{})
	if err != nil {
		panic(err)
	}
	return map[string]any{
		"transactionHash": hash,
		"status":          "0x1",
		"logs": []any{map[string]any{
			"address": zone.OutboxAddress.Hex(),
			"topics":  []any{zone.RequestedTopic.Hex(), common.BigToHash(big.NewInt(0)).Hex(), common.BytesToHash(sender.Bytes()).Hex()},
			"data":    hexutil.Encode(data),
		}},
	}
}

// zoneTestTempoRPC is the Tempo RPC; it shows a settlement once settled.
type zoneTestTempoRPC struct {
	mu      sync.Mutex
	settled bool
	topics  []any
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
		return &temporpc.JSONRPCResponse{Result: "0x10"}, nil
	case "eth_getLogs":
		r.mu.Lock()
		defer r.mu.Unlock()
		r.topics = params[0].(map[string]any)["topics"].([]any)
		if !r.settled {
			return &temporpc.JSONRPCResponse{Result: []any{}}, nil
		}
		return &temporpc.JSONRPCResponse{Result: []any{map[string]any{"transactionHash": zoneTestSettlement}}}, nil
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
	calls := zoneRPC.callCount()
	_, err = method.CreateCredential(context.Background(), challenge)
	assert.ErrorContains(t, err, "Zone withdrawal outcome is unknown")
	assert.Equal(t, calls, zoneRPC.callCount())
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
	assert.Equal(t, calls, zoneRPC.callCount())
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

// A Zone can answer a send with a JSON-RPC error while it holds the
// transaction pending: the method waits for its receipt rather than
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
	assert.Contains(t, zoneRPC.calls, "eth_getTransactionByHash")
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
		assert.ErrorContains(t, err, "replacement transaction underpriced")
	}
	assert.Len(t, zoneRPC.sent, 1)
}

// A Zone that rejects the withdrawal moved nothing, so the challenge may be
// paid again.
func TestZoneRetriesWithdrawalTheZoneRejected(t *testing.T) {
	t.Parallel()
	zoneRPC := &zoneTestRPC{mode: "reject"}
	method, _ := newZoneMethod(t, zoneRPC, zoneTestChainID, nil)
	challenge := zoneChallenge(t, nil)
	for range 2 {
		_, err := method.CreateCredential(context.Background(), challenge)
		assert.ErrorContains(t, err, "insufficient funds")
		assert.NotContains(t, err.Error(), "outcome is unknown")
	}
	assert.Len(t, zoneRPC.sent, 2)
	// Each rejection was checked once for a receipt and once for the
	// transaction, without polling.
	receiptLookups, transactionLookups := 0, 0
	for _, call := range zoneRPC.calls {
		switch call {
		case "eth_getTransactionReceipt":
			receiptLookups++
		case "eth_getTransactionByHash":
			transactionLookups++
		}
	}
	assert.Equal(t, 2, receiptLookups)
	assert.Equal(t, 2, transactionLookups)
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
