package chargeserver

import (
	"context"
	"fmt"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	temporpc "github.com/tempoxyz/tempo-go/pkg/client"

	"github.com/tempoxyz/mpp-go/pkg/internal/mpptest"
	"github.com/tempoxyz/mpp-go/pkg/mpp"
	"github.com/tempoxyz/mpp-go/pkg/server"
	"github.com/tempoxyz/mpp-go/pkg/tempo"
	"github.com/tempoxyz/mpp-go/pkg/tempo/internal/zone"
)

const (
	zoneTestMerchant      = "0x2222222222222222222222222222222222222222"
	zoneTestOther         = "0x3333333333333333333333333333333333333333"
	zoneTestOtherCurrency = "0x20c0000000000000000000000000000000000002"
	zoneTestID            = uint32(3)
)

var (
	zoneSettlementHash = "0x" + strings.Repeat("ab", 32)
	zoneTagA           = "0x" + strings.Repeat("a1", 32)
	zoneTagB           = "0x" + strings.Repeat("b2", 32)
)

// zoneWithdrawalFixture is one withdrawal the mock settlement transaction
// processes; zero fields take the charge's values.
type zoneWithdrawalFixture struct {
	senderTag      string
	memo           string
	to             string
	token          string
	amount         *big.Int
	gasLimit       uint64
	callbackFailed bool
	eventPortal    *common.Address
	notInCalldata  bool
}

// zoneRPC serves a Tempo settlement transaction of Zone 3's portal that
// processes the fixture withdrawals.
type zoneRPC struct {
	withdrawals []zoneWithdrawalFixture
	reverted    bool
	methods     []string
}

func (r *zoneRPC) GetChainID(context.Context) (uint64, error) { return 42431, nil }

func (r *zoneRPC) GetTransactionCount(context.Context, string) (uint64, error) { return 0, nil }

func (r *zoneRPC) SendRawTransaction(context.Context, string) (string, error) {
	return "", fmt.Errorf("unexpected send")
}

func (r *zoneRPC) SendRequest(_ context.Context, method string, _ ...interface{}) (*temporpc.JSONRPCResponse, error) {
	r.methods = append(r.methods, method)
	portal := zone.PortalAddress(zoneTestID)
	switch method {
	case "eth_getTransactionReceipt":
		logs := make([]any, 0, len(r.withdrawals))
		for _, w := range r.withdrawals {
			address := portal
			if w.eventPortal != nil {
				address = *w.eventPortal
			}
			data, err := zone.PortalABI.Events["WithdrawalProcessed"].Inputs.NonIndexed().Pack(
				common.HexToAddress(w.tokenOr(testCurrency)), w.amountOr(), !w.callbackFailed)
			if err != nil {
				return nil, err
			}
			logs = append(logs, map[string]any{
				"address": address.Hex(),
				"topics": []any{
					zone.ProcessedTopic.Hex(),
					common.BytesToHash(common.HexToAddress(w.toOr(zoneTestMerchant)).Bytes()).Hex(),
					w.senderTag,
				},
				"data": hexutil.Encode(data),
			})
		}
		status := "0x1"
		if r.reverted {
			status = "0x0"
		}
		return &temporpc.JSONRPCResponse{Result: map[string]any{
			"transactionHash": zoneSettlementHash,
			"status":          status,
			"logs":            logs,
		}}, nil
	case "eth_getTransactionByHash":
		withdrawals := make([]zone.Withdrawal, 0, len(r.withdrawals))
		for _, w := range r.withdrawals {
			if w.notInCalldata {
				continue
			}
			withdrawals = append(withdrawals, zone.Withdrawal{
				Token:           common.HexToAddress(w.tokenOr(testCurrency)),
				SenderTag:       common.HexToHash(w.senderTag),
				To:              common.HexToAddress(w.toOr(zoneTestMerchant)),
				Amount:          w.amountOr(),
				Memo:            common.HexToHash(w.memo),
				GasLimit:        w.gasLimit,
				FallbackNonce:   1,
				CallbackData:    []byte{},
				EncryptedSender: []byte{},
			})
		}
		data, err := zone.PortalABI.Pack("processWithdrawals", withdrawals, [32]byte{})
		if err != nil {
			return nil, err
		}
		return &temporpc.JSONRPCResponse{Result: map[string]any{
			"hash":  zoneSettlementHash,
			"from":  "0x4444444444444444444444444444444444444444",
			"type":  "0x76",
			"calls": []any{map[string]any{"to": portal.Hex(), "data": hexutil.Encode(data)}},
		}}, nil
	}
	return nil, fmt.Errorf("unexpected %s", method)
}

func (w zoneWithdrawalFixture) tokenOr(fallback string) string {
	if w.token != "" {
		return w.token
	}
	return fallback
}

func (w zoneWithdrawalFixture) toOr(fallback string) string {
	if w.to != "" {
		return w.to
	}
	return fallback
}

func (w zoneWithdrawalFixture) amountOr() *big.Int {
	if w.amount != nil {
		return w.amount
	}
	return big.NewInt(1_000_000)
}

type zoneSetup struct {
	rpc    *zoneRPC
	method *Method
}

func newZoneSetup(t *testing.T, zones *tempo.Zones) zoneSetup {
	t.Helper()
	if zones == nil {
		zones = &tempo.Zones{IDs: []uint32{zoneTestID}}
	}
	rpc := &zoneRPC{}
	method, err := New(Config{
		RPC:        rpc,
		Store:      tempo.NewMemoryStore(),
		Currencies: []string{testCurrency},
		Recipient:  zoneTestMerchant,
		ChainID:    tempo.ChainIDModerato,
		Zones:      zones,
	})
	require.NoError(t, err)
	return zoneSetup{rpc: rpc, method: method}
}

// challenge issues a challenge for 1 unit, and returns its attribution memo.
func (s zoneSetup) challenge(t *testing.T, params server.ChargeParams) (*mpp.Challenge, string) {
	t.Helper()
	params.Amount = "1"
	request, err := s.method.BuildChargeRequest(params)
	require.NoError(t, err)
	challenge := mpptest.NewChallenge("secret-key", testRealm, tempo.MethodName, tempo.IntentCharge, request,
		mpp.WithExpires(mpp.ExpiresIn(5*time.Minute)))
	return challenge, tempo.EncodeAttribution(challenge.Realm, "", challenge.ID)
}

func (s zoneSetup) verify(challenge *mpp.Challenge, senderTag string, zoneID uint32) (*mpp.Receipt, error) {
	credential := &mpp.Credential{
		Challenge: challenge.ToEcho(),
		Payload: map[string]any{
			"type":      "zone",
			"hash":      zoneSettlementHash,
			"senderTag": senderTag,
			"zoneId":    int64(zoneID),
		},
	}
	return s.method.intent.Verify(context.Background(), credential, challenge.Request)
}

func TestZoneAdvertisesAcceptedZones(t *testing.T) {
	t.Parallel()
	s := newZoneSetup(t, &tempo.Zones{IDs: []uint32{3, 7}})
	challenge, _ := s.challenge(t, server.ChargeParams{})
	assert.Equal(t, []uint32{3, 7}, challenge.Request["methodDetails"].(map[string]any)["zones"])

	s = newZoneSetup(t, &tempo.Zones{Any: true})
	challenge, _ = s.challenge(t, server.ChargeParams{})
	assert.Equal(t, true, challenge.Request["methodDetails"].(map[string]any)["zones"])

	// A route overrides the method's Zones.
	challenge, _ = s.challenge(t, server.ChargeParams{Zones: &tempo.Zones{IDs: []uint32{9}}})
	assert.Equal(t, []uint32{9}, challenge.Request["methodDetails"].(map[string]any)["zones"])
}

func TestZoneAcceptsSettledWithdrawal(t *testing.T) {
	t.Parallel()
	s := newZoneSetup(t, nil)
	challenge, memo := s.challenge(t, server.ChargeParams{})
	s.rpc.withdrawals = []zoneWithdrawalFixture{{senderTag: zoneTagA, memo: memo}}
	receipt, err := s.verify(challenge, zoneTagA, zoneTestID)
	require.NoError(t, err)
	assert.Equal(t, tempo.MethodName, receipt.Method)
	assert.Equal(t, zoneSettlementHash, receipt.Reference)
	assert.Equal(t, "success", receipt.Status)

	validation, err := s.method.intent.(*Intent).Validate(context.Background(), &mpp.Credential{
		Challenge: challenge.ToEcho(),
		Payload:   tempo.ChargeCredentialPayload{Type: tempo.CredentialTypeZone, Hash: zoneSettlementHash, SenderTag: zoneTagA, ZoneID: zoneTestID}.Map(),
	}, challenge.Request)
	require.NoError(t, err)
	assert.Equal(t, "push", validation.Details["mode"])
}

func TestZoneRejectsCredentialWithoutSettlementHash(t *testing.T) {
	t.Parallel()
	s := newZoneSetup(t, nil)
	challenge, _ := s.challenge(t, server.ChargeParams{})
	_, err := s.method.intent.Verify(context.Background(), &mpp.Credential{
		Challenge: challenge.ToEcho(),
		Payload:   map[string]any{"type": "zone", "senderTag": zoneTagA, "zoneId": int64(zoneTestID)},
	}, challenge.Request)
	require.Error(t, err)
	assert.NotContains(t, s.rpc.methods, "eth_getTransactionReceipt")
}

func TestZoneRejectsReplayedWithdrawal(t *testing.T) {
	t.Parallel()
	s := newZoneSetup(t, nil)
	challenge, memo := s.challenge(t, server.ChargeParams{})
	s.rpc.withdrawals = []zoneWithdrawalFixture{{senderTag: zoneTagA, memo: memo}}
	_, err := s.verify(challenge, zoneTagA, zoneTestID)
	require.NoError(t, err)
	_, err = s.verify(challenge, zoneTagA, zoneTestID)
	assert.ErrorContains(t, err, "zone withdrawal has already been used")
}

func TestZoneAcceptsTwoPaymentsSettledInOneTransaction(t *testing.T) {
	t.Parallel()
	s := newZoneSetup(t, nil)
	a, memoA := s.challenge(t, server.ChargeParams{})
	b, memoB := s.challenge(t, server.ChargeParams{ExternalID: "b"})
	s.rpc.withdrawals = []zoneWithdrawalFixture{{senderTag: zoneTagA, memo: memoA}, {senderTag: zoneTagB, memo: memoB}}
	_, err := s.verify(a, zoneTagA, zoneTestID)
	require.NoError(t, err)
	_, err = s.verify(b, zoneTagB, zoneTestID)
	require.NoError(t, err)
}

func TestZoneRejectsWithdrawalBoundToAnotherChallenge(t *testing.T) {
	t.Parallel()
	s := newZoneSetup(t, nil)
	_, memoA := s.challenge(t, server.ChargeParams{})
	b, _ := s.challenge(t, server.ChargeParams{ExternalID: "b"})
	s.rpc.withdrawals = []zoneWithdrawalFixture{{senderTag: zoneTagA, memo: memoA}}
	_, err := s.verify(b, zoneTagA, zoneTestID)
	assert.ErrorContains(t, err, "memo is not bound to this challenge")
}

func TestZoneRejectsWithdrawalWithoutMPPMemo(t *testing.T) {
	t.Parallel()
	s := newZoneSetup(t, nil)
	challenge, _ := s.challenge(t, server.ChargeParams{})
	s.rpc.withdrawals = []zoneWithdrawalFixture{{senderTag: zoneTagA, memo: "0x" + strings.Repeat("00", 32)}}
	_, err := s.verify(challenge, zoneTagA, zoneTestID)
	assert.ErrorContains(t, err, "memo is not bound to this challenge")
}

func TestZoneRejectsMismatchedWithdrawal(t *testing.T) {
	t.Parallel()
	otherPortal := zone.PortalAddress(4)
	for name, tc := range map[string]struct {
		fixture zoneWithdrawalFixture
		want    string
	}{
		"recipient":                        {zoneWithdrawalFixture{to: zoneTestOther}, "zone withdrawal not found"},
		"amount":                           {zoneWithdrawalFixture{amount: big.NewInt(999_999)}, "zone withdrawal not found"},
		"currency":                         {zoneWithdrawalFixture{token: zoneTestOtherCurrency}, "zone withdrawal not found"},
		"bounced withdrawal":               {zoneWithdrawalFixture{callbackFailed: true}, "zone withdrawal bounced"},
		"event from another portal":        {zoneWithdrawalFixture{eventPortal: &otherPortal}, "not settled"},
		"withdrawal missing from calldata": {zoneWithdrawalFixture{notInCalldata: true}, "zone withdrawal not found"},
		"callback withdrawal":              {zoneWithdrawalFixture{gasLimit: 100_000}, "zone withdrawal not found"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			s := newZoneSetup(t, nil)
			challenge, memo := s.challenge(t, server.ChargeParams{})
			fixture := tc.fixture
			fixture.senderTag, fixture.memo = zoneTagA, memo
			s.rpc.withdrawals = []zoneWithdrawalFixture{fixture}
			_, err := s.verify(challenge, zoneTagA, zoneTestID)
			assert.ErrorContains(t, err, tc.want)
		})
	}
}

func TestZoneRejectsRevertedSettlement(t *testing.T) {
	t.Parallel()
	s := newZoneSetup(t, nil)
	challenge, memo := s.challenge(t, server.ChargeParams{})
	s.rpc.reverted = true
	s.rpc.withdrawals = []zoneWithdrawalFixture{{senderTag: zoneTagA, memo: memo}}
	_, err := s.verify(challenge, zoneTagA, zoneTestID)
	assert.ErrorIs(t, err, mpp.ErrorTypeVerificationFailed)
	assert.ErrorContains(t, err, "settlement transaction reverted")
}

func TestZoneRejectsZoneTheChallengeDoesNotAccept(t *testing.T) {
	t.Parallel()
	s := newZoneSetup(t, nil)
	challenge, _ := s.challenge(t, server.ChargeParams{})
	_, err := s.verify(challenge, zoneTagA, 4)
	assert.ErrorContains(t, err, "zone is not accepted for this challenge")
	assert.NotContains(t, s.rpc.methods, "eth_getTransactionReceipt")
}

func TestZoneAcceptsAnyZone(t *testing.T) {
	t.Parallel()
	s := newZoneSetup(t, &tempo.Zones{Any: true})
	challenge, memo := s.challenge(t, server.ChargeParams{})
	s.rpc.withdrawals = []zoneWithdrawalFixture{{senderTag: zoneTagA, memo: memo}}
	_, err := s.verify(challenge, zoneTagA, zoneTestID)
	require.NoError(t, err)
}

func TestZoneChecksPortalOfNamedZoneWhenAnyZoneIsAccepted(t *testing.T) {
	t.Parallel()
	s := newZoneSetup(t, &tempo.Zones{Any: true})
	challenge, memo := s.challenge(t, server.ChargeParams{})
	s.rpc.withdrawals = []zoneWithdrawalFixture{{senderTag: zoneTagA, memo: memo}}
	// The settlement came from Zone 3's portal, not Zone 4's.
	_, err := s.verify(challenge, zoneTagA, 4)
	assert.ErrorContains(t, err, "zone withdrawal not settled")
}

func TestZoneRejectsInvalidZoneID(t *testing.T) {
	t.Parallel()
	for _, zoneID := range []any{int64(0), int64(-1), 1.5, int64(1 << 32)} {
		s := newZoneSetup(t, &tempo.Zones{Any: true})
		challenge, _ := s.challenge(t, server.ChargeParams{})
		_, err := s.method.intent.Verify(context.Background(), &mpp.Credential{
			Challenge: challenge.ToEcho(),
			Payload:   map[string]any{"type": "zone", "hash": zoneSettlementHash, "senderTag": zoneTagA, "zoneId": zoneID},
		}, challenge.Request)
		assert.Error(t, err, "zone ID %v", zoneID)
		assert.NotContains(t, s.rpc.methods, "eth_getTransactionReceipt")
	}
}

func TestZoneRejectsPullOnlyChallenge(t *testing.T) {
	t.Parallel()
	s := newZoneSetup(t, nil)
	challenge, _ := s.challenge(t, server.ChargeParams{SupportedModes: []tempo.ChargeMode{tempo.ChargeModePull}})
	_, err := s.verify(challenge, zoneTagA, zoneTestID)
	assert.ErrorContains(t, err, "zone credentials are not supported for this challenge")
	assert.NotContains(t, s.rpc.methods, "eth_getTransactionReceipt")
}

func TestZoneRejectsSplitPayments(t *testing.T) {
	t.Parallel()
	s := newZoneSetup(t, nil)
	challenge, _ := s.challenge(t, server.ChargeParams{Splits: []tempo.SplitParams{{Amount: "0.1", Recipient: zoneTestOther}}})
	_, err := s.verify(challenge, zoneTagA, zoneTestID)
	assert.ErrorContains(t, err, "zone credentials do not support split payments")
}
