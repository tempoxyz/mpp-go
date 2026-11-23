package zone

import (
	"context"
	"math"
	"math/big"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	temporpc "github.com/tempoxyz/tempo-go/pkg/client"
)

func TestIDFromChainID(t *testing.T) {
	t.Parallel()
	for chainID, want := range map[int64]uint32{
		421_700_003:      3,
		1_424_310_003:    3,
		1337*(1<<32) + 1: 1,
	} {
		got, err := IDFromChainID(chainID)
		require.NoError(t, err)
		assert.Equal(t, want, got, "chain %d", chainID)
	}
	_, err := IDFromChainID(42_431)
	assert.ErrorContains(t, err, "cannot derive a Zone ID")
}

func TestParentChainIDFromChainID(t *testing.T) {
	t.Parallel()
	for chainID, want := range map[int64]int64{
		421_700_003:      4_217,
		1_424_310_003:    42_431,
		1337*(1<<32) + 1: 1337,
	} {
		got, ok := ParentChainIDFromChainID(chainID)
		assert.True(t, ok)
		assert.Equal(t, want, got, "chain %d", chainID)
	}
	_, ok := ParentChainIDFromChainID(42_431)
	assert.False(t, ok)
}

func TestIsID(t *testing.T) {
	t.Parallel()
	for _, id := range []uint32{1, 3, 421_699_999} {
		assert.True(t, IsID(id), "zone %d", id)
	}
	for _, id := range []uint32{0, 421_700_000, 421_700_003, 1_424_310_003, 4_294_967_295} {
		assert.False(t, IsID(id), "zone %d", id)
	}
}

// The expected portals are viem's Addresses.zonePortal for valid Zone IDs.
// Zone chain IDs are read literally, not as Zone 3's or Zone 9's portal.
func TestPortalAddress(t *testing.T) {
	t.Parallel()
	for id, want := range map[uint32]string{
		3:             "0x5ad0000000000000000000000000000000000003",
		6:             "0x7069DeC4E64Fd07334A0933eDe836C17259c9B23",
		7:             "0x3F5296303400B56271b476F5A0B9cBF74350D6Ac",
		421_700_003:   "0x5ad000000000000000000000000000001922a1a3",
		1_424_310_003: "0x5ad0000000000000000000000000000054e53ef3",
		4_217_000_009: "0x5ad00000000000000000000000000000fb5a5049",
		4_294_967_295: "0x5ad00000000000000000000000000000ffffffff",
	} {
		assert.Equal(t, common.HexToAddress(want), PortalAddress(id), "zone %d", id)
	}
}

// The expected tag is viem's keccak256(encodePacked(['address', 'bytes32', 'uint64'], ...)).
func TestSenderTag(t *testing.T) {
	t.Parallel()
	got := SenderTag(common.HexToAddress("0x1111111111111111111111111111111111111111"), common.HexToHash("0x"+repeat("ab", 32)), 5)
	assert.Equal(t, "0x788fcff52bc1aca0be1c1ea444c37d65c3ace8da9b825356ab590beccac6a973", got.Hex())
}

// The expected calldata is viem's encodeFunctionData for the ZoneOutbox ABI.
func TestOutboxABI(t *testing.T) {
	t.Parallel()
	data, err := OutboxABI.Pack("requestWithdrawal",
		common.HexToAddress("0x20c0000000000000000000000000000000000001"),
		common.HexToAddress("0x2222222222222222222222222222222222222222"),
		big.NewInt(1_000_000),
		[32]byte(common.HexToHash("0x"+repeat("cd", 32))),
		uint64(0),
		common.HexToAddress("0x3333333333333333333333333333333333333333"),
		[]byte{}, []byte{})
	require.NoError(t, err)
	assert.Equal(t, "0xb3b200aa00000000000000000000000020c0000000000000000000000000000000000001000000000000000000000000222222222222222222222222222222222222222200000000000000000000000000000000000000000000000000000000000f4240cdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcd000000000000000000000000000000000000000000000000000000000000000000000000000000000000000033333333333333333333333333333333333333330000000000000000000000000000000000000000000000000000000000000100000000000000000000000000000000000000000000000000000000000000012000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000", hexutil.Encode(data))
	fee, err := OutboxABI.Pack("calculateWithdrawalFee", uint64(0))
	require.NoError(t, err)
	assert.Equal(t, "0x7b9c9aa40000000000000000000000000000000000000000000000000000000000000000", hexutil.Encode(fee))
}

func TestFindWithdrawalAndProcessedEvent(t *testing.T) {
	t.Parallel()
	portal := PortalAddress(3)
	tagA := common.HexToHash("0x" + repeat("a1", 32))
	tagB := common.HexToHash("0x" + repeat("b2", 32))
	to := common.HexToAddress("0x2222222222222222222222222222222222222222")
	token := common.HexToAddress("0x20c0000000000000000000000000000000000001")
	withdrawal := func(tag common.Hash) Withdrawal {
		return Withdrawal{Token: token, SenderTag: tag, To: to, Amount: big.NewInt(1_000_000), Memo: [32]byte{1}, FallbackNonce: 1, CallbackData: []byte{}, EncryptedSender: []byte{}}
	}
	data, err := PortalABI.Pack("processWithdrawals", []Withdrawal{withdrawal(tagA), withdrawal(tagB)}, [32]byte{})
	require.NoError(t, err)
	calls := []any{
		map[string]any{"to": "0x0000000000000000000000000000000000000001", "data": hexutil.Encode(data)},
		map[string]any{"to": portal.Hex(), "input": hexutil.Encode(data)},
	}
	found, ok := FindWithdrawal(calls, portal, tagB)
	require.True(t, ok)
	assert.Equal(t, [32]byte(tagB), found.SenderTag)
	assert.Equal(t, "1000000", found.Amount.String())
	_, ok = FindWithdrawal(calls, PortalAddress(4), tagB)
	assert.False(t, ok)
	_, ok = FindWithdrawal(calls, portal, common.Hash{})
	assert.False(t, ok)

	receipt := map[string]any{"logs": []any{processedLog(t, portal, to, tagA, token, true)}}
	event, ok := FindProcessedEvent(receipt, portal, tagA)
	require.True(t, ok)
	assert.Equal(t, to, event.To)
	assert.True(t, event.CallbackSuccess)
	_, ok = FindProcessedEvent(receipt, portal, tagB)
	assert.False(t, ok)
	_, ok = FindProcessedEvent(receipt, PortalAddress(4), tagA)
	assert.False(t, ok)
}

type logsRPC struct {
	blocks []string
	logs   func(filter map[string]any) []any
}

func (r *logsRPC) SendRequest(_ context.Context, method string, params ...interface{}) (*temporpc.JSONRPCResponse, error) {
	switch method {
	case "eth_blockNumber":
		block := r.blocks[0]
		if len(r.blocks) > 1 {
			r.blocks = r.blocks[1:]
		}
		return &temporpc.JSONRPCResponse{Result: block}, nil
	case "eth_getLogs":
		return &temporpc.JSONRPCResponse{Result: r.logs(params[0].(map[string]any))}, nil
	}
	return &temporpc.JSONRPCResponse{Error: &temporpc.JSONRPCError{Code: -1, Message: method}}, nil
}

func TestWaitForSettlement(t *testing.T) {
	t.Parallel()
	portal := PortalAddress(3)
	tag := common.HexToHash("0x" + repeat("a1", 32))
	var filters []map[string]any
	rpc := &logsRPC{blocks: []string{"0x2710", "0x2710", "0x2712"}, logs: func(filter map[string]any) []any {
		filters = append(filters, filter)
		if len(filters) < 2 {
			return []any{}
		}
		return []any{map[string]any{"transactionHash": "0x" + repeat("ab", 32)}}
	}}
	start, err := SettlementStartBlock(context.Background(), rpc)
	require.NoError(t, err)
	assert.Equal(t, uint64(0x2710-lookbackBlocks), start)
	settlement, err := WaitForSettlement(context.Background(), rpc, portal, tag, nil, start, 5*time.Second)
	require.NoError(t, err)
	assert.Equal(t, Settlement{Hash: "0x" + repeat("ab", 32), SenderTag: tag}, settlement)
	// The first poll looks back lookbackBlocks; the next only at new blocks.
	assert.Equal(t, "0xaf0", filters[0]["fromBlock"])
	assert.Equal(t, "0x2710", filters[0]["toBlock"])
	assert.Equal(t, "0x2711", filters[1]["fromBlock"])
	assert.Equal(t, []any{ProcessedTopic.Hex(), nil, tag.Hex()}, filters[0]["topics"])

	_, err = WaitForSettlement(context.Background(), &logsRPC{blocks: []string{"0x1"}, logs: func(map[string]any) []any { return nil }}, portal, tag, nil, 0, 0)
	assert.ErrorContains(t, err, "did not settle on Tempo")

	start, err = SettlementStartBlock(context.Background(), &logsRPC{blocks: []string{"0x10"}})
	require.NoError(t, err)
	assert.Equal(t, uint64(0), start)
}

// A wait resumed long after the first one searches from the first one's
// start block, however far the head has moved, in bounded log ranges.
func TestWaitForSettlementResumesFromStartBlockInChunks(t *testing.T) {
	t.Parallel()
	portal := PortalAddress(3)
	tag := common.HexToHash("0x" + repeat("a1", 32))
	const settledAt = 10_001
	var ranges [][2]uint64
	rpc := &logsRPC{blocks: []string{hexutil.EncodeUint64(30_000)}, logs: func(filter map[string]any) []any {
		from := hexutil.MustDecodeUint64(filter["fromBlock"].(string))
		to := hexutil.MustDecodeUint64(filter["toBlock"].(string))
		ranges = append(ranges, [2]uint64{from, to})
		if from <= settledAt && settledAt <= to {
			return []any{map[string]any{"transactionHash": "0x" + repeat("ab", 32)}}
		}
		return []any{}
	}}
	settlement, err := WaitForSettlement(context.Background(), rpc, portal, tag, nil, 2_800, 0)
	require.NoError(t, err)
	assert.Equal(t, "0x"+repeat("ab", 32), settlement.Hash)
	assert.Equal(t, [][2]uint64{{2_800, 10_000}, {10_001, 17_201}}, ranges)

	// With no settlement, it searches every block up to the head once.
	ranges = nil
	rpc.logs = func(filter map[string]any) []any {
		from := hexutil.MustDecodeUint64(filter["fromBlock"].(string))
		to := hexutil.MustDecodeUint64(filter["toBlock"].(string))
		ranges = append(ranges, [2]uint64{from, to})
		return []any{}
	}
	_, err = WaitForSettlement(context.Background(), rpc, portal, tag, nil, 2_800, 0)
	assert.ErrorContains(t, err, "did not settle on Tempo")
	assert.Equal(t, [][2]uint64{{2_800, 10_000}, {10_001, 17_201}, {17_202, 24_402}, {24_403, 30_000}}, ranges)
}

// WaitForSettlement reports a payout as bounced only when the event's
// callbackSuccess decodes to false.
func TestWaitForSettlementReportsBouncedPayout(t *testing.T) {
	t.Parallel()
	portal := PortalAddress(3)
	to := common.HexToAddress("0x00000000000000000000000000000000000000c1")
	tag := common.HexToHash("0x" + repeat("a1", 32))
	token := common.HexToAddress("0x20c0000000000000000000000000000000000000")
	hash := "0x" + repeat("ab", 32)
	for name, test := range map[string]struct {
		data    func() string
		bounced bool
	}{
		"success": {data: func() string { return asString(processedLog(t, portal, to, tag, token, true)["data"]) }},
		"bounced": {data: func() string { return asString(processedLog(t, portal, to, tag, token, false)["data"]) }, bounced: true},
		"garbled": {data: func() string { return "0x01" }},
		"missing": {data: func() string { return "" }},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			rpc := &logsRPC{blocks: []string{"0x10"}, logs: func(map[string]any) []any {
				entry := map[string]any{"transactionHash": hash}
				if data := test.data(); data != "" {
					entry["data"] = data
				}
				return []any{entry}
			}}
			settlement, err := WaitForSettlement(context.Background(), rpc, portal, tag, &to, 0, 0)
			require.NoError(t, err)
			assert.Equal(t, Settlement{Hash: hash, SenderTag: tag, Bounced: test.bounced}, settlement)
		})
	}
}

func processedLog(t *testing.T, portal, to common.Address, tag common.Hash, token common.Address, success bool) map[string]any {
	t.Helper()
	data, err := PortalABI.Events["WithdrawalProcessed"].Inputs.NonIndexed().Pack(token, big.NewInt(1_000_000), success)
	require.NoError(t, err)
	return map[string]any{
		"address": portal.Hex(),
		"topics":  []any{ProcessedTopic.Hex(), common.BytesToHash(to.Bytes()).Hex(), tag.Hex()},
		"data":    hexutil.Encode(data),
	}
}

func repeat(s string, n int) string {
	out := ""
	for range n {
		out += s
	}
	return out
}

// stalledRPC answers eth_blockNumber and then stalls every eth_getLogs call
// until its context ends.
type stalledRPC struct{}

func (stalledRPC) SendRequest(ctx context.Context, method string, _ ...interface{}) (*temporpc.JSONRPCResponse, error) {
	if method == "eth_blockNumber" {
		return &temporpc.JSONRPCResponse{Result: "0x10"}, nil
	}
	<-ctx.Done()
	return nil, ctx.Err()
}

// A stalled Tempo RPC cannot hold a wait without a context deadline past its
// timeout and rpcGrace.
func TestWaitForSettlementBoundsStalledRPCCalls(t *testing.T) {
	t.Parallel()
	start := time.Now()
	_, err := WaitForSettlement(context.Background(), stalledRPC{}, PortalAddress(3), common.HexToHash("0x01"), nil, 0, 10*time.Millisecond)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Less(t, time.Since(start), rpcGrace+5*time.Second)
}

func TestIsPortalAddress(t *testing.T) {
	t.Parallel()
	for address, want := range map[string]bool{
		"0x5ad0000000000000000000000000000000000003": true,
		"0x5AD0000000000000000000000000000000000003": true,
		"0x5ad00000000000000000000000000000ffffffff": true,
		"0x7069DeC4E64Fd07334A0933eDe836C17259c9B23": true,
		"0x7069dec4e64fd07334a0933ede836c17259c9b23": true,
		"0x3F5296303400B56271b476F5A0B9cBF74350D6Ac": true,
		"0x5ad0000000000000000000010000000000000003": false,
		"0x5ad1000000000000000000000000000000000003": false,
		"0x70997970C51812dc3A010C7d01b50e0d17dc79C8": false,
	} {
		assert.Equal(t, want, IsPortalAddress(common.HexToAddress(address)), address)
	}
}

// zoneCallRPC is a Zone RPC whose outbox reports before at block 9 and after
// at block 10.
type zoneCallRPC struct {
	before, after uint64
	calls         []any
}

func (r *zoneCallRPC) SendRequest(_ context.Context, method string, params ...interface{}) (*temporpc.JSONRPCResponse, error) {
	if method != "eth_call" {
		return &temporpc.JSONRPCResponse{Error: &temporpc.JSONRPCError{Code: -1, Message: method}}, nil
	}
	r.calls = append(r.calls, params...)
	nonce := r.after
	if params[1] == "0x9" {
		nonce = r.before
	}
	return &temporpc.JSONRPCResponse{Result: hexutil.Encode(common.BigToHash(new(big.Int).SetUint64(nonce)).Bytes())}, nil
}

func TestSenderTagCandidates(t *testing.T) {
	t.Parallel()
	sender := common.HexToAddress("0x0000000000000000000000000000000000000001")
	hash := common.HexToHash("0x" + repeat("ab", 32))

	t.Run("derives one tag per nonce the block added", func(t *testing.T) {
		t.Parallel()
		rpc := &zoneCallRPC{before: 4, after: 6}
		tags, err := SenderTagCandidates(context.Background(), rpc, 10, sender, hash)
		require.NoError(t, err)
		assert.Equal(t, []common.Hash{SenderTag(sender, hash, 5), SenderTag(sender, hash, 6)}, tags)
		selector := hexutil.Encode(OutboxABI.Methods["lastFallbackNonce"].ID)
		assert.Equal(t, []any{
			map[string]any{"to": OutboxAddress.Hex(), "data": selector}, "0x9",
			map[string]any{"to": OutboxAddress.Hex(), "data": selector}, "0xa",
		}, rpc.calls)
	})

	t.Run("rejects a block that added no withdrawal", func(t *testing.T) {
		t.Parallel()
		_, err := SenderTagCandidates(context.Background(), &zoneCallRPC{before: 4, after: 4}, 10, sender, hash)
		assert.ErrorContains(t, err, "did not request a withdrawal")
		_, err = SenderTagCandidates(context.Background(), &zoneCallRPC{}, 0, sender, hash)
		assert.ErrorContains(t, err, "did not request a withdrawal")
	})

	t.Run("rejects a nonce range no block could hold", func(t *testing.T) {
		t.Parallel()
		_, err := SenderTagCandidates(context.Background(), &zoneCallRPC{before: 0, after: 1 << 63}, 10, sender, hash)
		assert.ErrorContains(t, err, "more than 64")
	})

	t.Run("derives the tags of a range ending at the largest nonce", func(t *testing.T) {
		t.Parallel()
		rpc := &zoneCallRPC{before: math.MaxUint64 - 2, after: math.MaxUint64}
		tags, err := SenderTagCandidates(context.Background(), rpc, 10, sender, hash)
		require.NoError(t, err)
		assert.Equal(t, []common.Hash{SenderTag(sender, hash, math.MaxUint64-1), SenderTag(sender, hash, math.MaxUint64)}, tags)
	})

	t.Run("reports a failed read", func(t *testing.T) {
		t.Parallel()
		_, err := SenderTagCandidates(context.Background(), &logsRPC{}, 10, sender, hash)
		assert.ErrorContains(t, err, "lastFallbackNonce")
	})
}

// WaitForSettledWithdrawal asks for every candidate tag at once and reports
// the one the settlement carries.
func TestWaitForSettledWithdrawal(t *testing.T) {
	t.Parallel()
	portal := PortalAddress(3)
	to := common.HexToAddress("0x00000000000000000000000000000000000000c1")
	token := common.HexToAddress("0x20c0000000000000000000000000000000000000")
	tagA := common.HexToHash("0x" + repeat("a1", 32))
	tagB := common.HexToHash("0x" + repeat("b2", 32))
	other := common.HexToHash("0x" + repeat("c3", 32))
	hash := "0x" + repeat("ab", 32)
	var filters []map[string]any
	rpc := &logsRPC{blocks: []string{"0x10"}, logs: func(filter map[string]any) []any {
		filters = append(filters, filter)
		unrelated := processedLog(t, portal, to, other, token, true)
		unrelated["transactionHash"] = "0x" + repeat("cd", 32)
		settled := processedLog(t, portal, to, tagB, token, true)
		settled["transactionHash"] = hash
		return []any{unrelated, settled}
	}}
	settlement, err := WaitForSettledWithdrawal(context.Background(), rpc, portal, []common.Hash{tagA, tagB}, &to, 0, 0)
	require.NoError(t, err)
	assert.Equal(t, Settlement{Hash: hash, SenderTag: tagB}, settlement)
	assert.Equal(t, []any{ProcessedTopic.Hex(), common.BytesToHash(to.Bytes()).Hex(), []string{tagA.Hex(), tagB.Hex()}}, filters[0]["topics"])

	_, err = WaitForSettledWithdrawal(context.Background(), rpc, portal, nil, &to, 0, 0)
	assert.ErrorContains(t, err, "needs at least one sender tag")

	_, err = WaitForSettledWithdrawal(context.Background(), &logsRPC{blocks: []string{"0x1"}, logs: func(map[string]any) []any { return nil }}, portal, []common.Hash{tagA, tagB}, nil, 0, 0)
	assert.ErrorContains(t, err, "zone withdrawal "+tagA.Hex()+" or "+tagB.Hex()+" did not settle on Tempo")
}

func TestHasRequestedLog(t *testing.T) {
	t.Parallel()
	requested := map[string]any{"address": OutboxAddress.Hex(), "topics": []any{RequestedTopic.Hex()}, "data": "0x01"}
	other := map[string]any{"address": PortalAddress(3).Hex(), "topics": []any{RequestedTopic.Hex()}}
	assert.True(t, HasRequestedLog(map[string]any{"logs": []any{other, requested}}))
	assert.False(t, HasRequestedLog(map[string]any{"logs": []any{other}}))
	assert.False(t, HasRequestedLog(map[string]any{"logs": []any{}}))
}
