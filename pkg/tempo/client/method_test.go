package chargeclient

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tempoxyz/mpp-go/pkg/internal/mpptest"

	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	temporpc "github.com/tempoxyz/tempo-go/pkg/client"
	tempotx "github.com/tempoxyz/tempo-go/pkg/transaction"

	"github.com/tempoxyz/mpp-go/pkg/mpp"
	"github.com/tempoxyz/mpp-go/pkg/tempo"
)

const (
	// testPrivateKey is the fixed payer key used in Tempo client tests.
	testPrivateKey = "0x59c6995e998f97a5a0044966f0945389dc9e86dae88c7a8412f4603b6b78690d"
	testCurrency   = "0x20c0000000000000000000000000000000000001"
	testRecipient  = "0x70997970c51812dc3a010c7d01b50e0d17dc79c8"
	testRealm      = "api.example.com"
)

type mockRPC struct {
	chainID     uint64
	nonce       uint64
	gasPrice    string
	estimateGas string
	sentRawTxs  []string
}

func (m *mockRPC) GetChainID(context.Context) (uint64, error) {
	return m.chainID, nil
}

func (m *mockRPC) GetTransactionCount(context.Context, string) (uint64, error) {
	return m.nonce, nil
}

func (m *mockRPC) SendRawTransaction(_ context.Context, serialized string) (string, error) {
	m.sentRawTxs = append(m.sentRawTxs, serialized)
	return "0x2120c5a4e1f6b7d9c3a8e2f4b6d8a0c2e4f6a8b0c2d4e6f8a0b2c4d6e8f0a2b4", nil
}

func (m *mockRPC) SendRequest(_ context.Context, method string, _ ...interface{}) (*temporpc.JSONRPCResponse, error) {
	switch method {
	case "eth_gasPrice":
		return &temporpc.JSONRPCResponse{Result: m.gasPrice}, nil
	case "eth_estimateGas":
		return &temporpc.JSONRPCResponse{Result: m.estimateGas}, nil
	default:
		return nil, fmt.Errorf("unexpected rpc method %q", method)
	}
}

func TestCreateCredentialScenarios(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		config         Config
		rpc            *mockRPC
		params         tempo.ChargeRequestParams
		wantErr        string
		wantBroadcasts int
	}{
		{
			name: "hash credential rejected for fee payer challenge",
			config: Config{
				ChainID:        42431,
				CredentialType: tempo.CredentialTypeHash,
			},
			rpc: &mockRPC{chainID: 42431},
			params: tempo.ChargeRequestParams{
				Amount:    "0.50",
				Currency:  testCurrency,
				Recipient: testRecipient,
				Decimals:  6,
				ChainID:   42431,
				FeePayer:  true,
			},
			wantErr:        "hash credentials cannot be used with fee payer challenges",
			wantBroadcasts: 0,
		},
		{
			name: "chain id mismatch",
			config: Config{
				ChainID: 42431,
			},
			rpc: &mockRPC{chainID: 1},
			params: tempo.ChargeRequestParams{
				Amount:    "0.50",
				Currency:  testCurrency,
				Recipient: testRecipient,
				Decimals:  6,
				ChainID:   42431,
			},
			wantErr:        "chain id mismatch",
			wantBroadcasts: 0,
		},
		{
			name: "challenge chain id conflicts with client pin",
			config: Config{
				ChainID: 42431,
			},
			rpc: &mockRPC{chainID: 42431},
			params: tempo.ChargeRequestParams{
				Amount:    "0.50",
				Currency:  testCurrency,
				Recipient: testRecipient,
				Decimals:  6,
				ChainID:   1,
			},
			wantErr:        "challenge chain id mismatch",
			wantBroadcasts: 0,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			config := tt.config
			config.PrivateKey = testPrivateKey
			config.RPC = tt.rpc

			method, err := New(config)
			if !assert.NoErrorf(t, err,
				"New() error = %v", err) {
				return
			}

			challenge := buildChallenge(t, tt.params)
			_, err = method.CreateCredential(context.Background(), challenge)
			if !assert.Falsef(t, err == nil || !strings.Contains(err.Error(), tt.wantErr),
				"CreateCredential() error = %v, want substring %q", err, tt.wantErr) {
				return
			}
			{

				got := len(tt.rpc.sentRawTxs)
				if !assert.Equalf(t, tt.wantBroadcasts, got,
					"len(tt.rpc.sentRawTxs) = %d, want %d", got, tt.wantBroadcasts) {
					return
				}
			}

		})
	}
}

func TestCreateCredentialHashWithAttributionMemo(t *testing.T) {
	t.Parallel()

	params := tempo.ChargeRequestParams{
		Amount:    "0.50",
		Currency:  testCurrency,
		Recipient: testRecipient,
		Decimals:  6,
		ChainID:   42431,
	}
	request, err := tempo.NormalizeChargeRequest(params)
	if !assert.NoErrorf(t, err, "NormalizeChargeRequest() error = %v", err) {
		return
	}
	rpc := &mockRPC{chainID: 42431}
	method, err := New(Config{PaymentPolicy: func(context.Context, tempo.ChargeRequest) error { return nil },
		ChainID:        42431,
		CredentialType: tempo.CredentialTypeHash,
		PrivateKey:     testPrivateKey,
		RPC:            rpc,
	})
	if !assert.NoErrorf(t, err, "New() error = %v", err) {
		return
	}

	challenge := buildChallenge(t, params)
	challenge.Request["methodDetails"].(map[string]any)["memo"] = "0x" + strings.Repeat("ab", 32)
	credential, err := method.CreateCredential(context.Background(), challenge)
	if !assert.NoErrorf(t, err, "CreateCredential() error = %v", err) {
		return
	}
	payload, err := tempo.ParseChargeCredentialPayload(credential.Payload)
	if !assert.NoErrorf(t, err, "ParseChargeCredentialPayload() error = %v", err) {
		return
	}
	if !assert.Equal(t, tempo.CredentialTypeHash, payload.Type) ||
		!assert.Equal(t, "0x2120c5a4e1f6b7d9c3a8e2f4b6d8a0c2e4f6a8b0c2d4e6f8a0b2c4d6e8f0a2b4", payload.Hash) ||
		!assert.Len(t, rpc.sentRawTxs, 1) {
		return
	}
	tx, err := tempotx.Deserialize(rpc.sentRawTxs[0])
	if !assert.NoErrorf(t, err, "Deserialize() error = %v", err) ||
		!assert.Len(t, tx.Calls, 1) {
		return
	}
	if !assert.True(t, tempo.MatchTransferCalldata(
		hexutil.Encode(tx.Calls[0].Data), request, challenge.Realm, challenge.ID,
	), "MatchTransferCalldata() = false, want challenge-bound attribution transfer") {
		return
	}
}

func TestNewInfersChainIDFromRPCURL(t *testing.T) {
	t.Parallel()

	method, err := New(Config{PaymentPolicy: func(context.Context, tempo.ChargeRequest) error { return nil },
		PrivateKey: testPrivateKey,
		RPCURL:     tempotx.RpcUrlModerato,
	})
	if !assert.NoErrorf(t, err,
		"New() error = %v", err) {
		return
	}
	if !assert.EqualValuesf(t, tempotx.ChainIdModerato, method.chainID,
		"method.chainID = %d, want %d", method.chainID, tempotx.ChainIdModerato) {
		return
	}

}

func TestNewRejectsUnknownChainWithoutRPC(t *testing.T) {
	t.Parallel()

	_, err := New(Config{PaymentPolicy: func(context.Context, tempo.ChargeRequest) error { return nil }, PrivateKey: testPrivateKey, ChainID: 999999})
	if !assert.Falsef(t, err == nil || err.Error() != "tempo client: unknown chain id 999999; configure RPC or RPCURL explicitly",
		"New() error = %v, want unknown chain id error", err) {
		return
	}

}

func TestNewRejectsUnsupportedCredentialType(t *testing.T) {
	t.Parallel()

	_, err := New(Config{
		PrivateKey:     testPrivateKey,
		CredentialType: tempo.CredentialType("invalid"),
	})
	assert.EqualError(t, err, `tempo client: unsupported credential type "invalid"`)
}

func TestCreateCredentialRejectsUnknownChallengeChainWithoutRPC(t *testing.T) {
	t.Parallel()

	method, err := New(Config{PaymentPolicy: func(context.Context, tempo.ChargeRequest) error { return nil }, PrivateKey: testPrivateKey})
	if !assert.NoErrorf(t, err,
		"New() error = %v", err) {
		return
	}

	challenge := buildChallenge(t, tempo.ChargeRequestParams{
		Amount:    "0.50",
		Currency:  testCurrency,
		Recipient: testRecipient,
		Decimals:  6,
		ChainID:   999999,
	})
	_, err = method.CreateCredential(context.Background(), challenge)
	if !assert.Falsef(t, err == nil || !strings.Contains(err.Error(), "unknown chain id 999999"),
		"CreateCredential() error = %v, want unknown chain id error", err) {
		return
	}

}

func TestCreateCredentialRejectsWireChallengeChainConflict(t *testing.T) {
	t.Parallel()

	method, err := New(Config{
		ChainID:    42431,
		PrivateKey: testPrivateKey,
		RPC:        &mockRPC{chainID: 42431},
	})
	if !assert.NoErrorf(t, err,
		"New() error = %v", err) {
		return
	}

	// Round-trip the challenge through the WWW-Authenticate header so the
	// chainId arrives the way a real client sees it (json.Number, not int64).
	issued := buildChallenge(t, tempo.ChargeRequestParams{
		Amount:    "0.50",
		Currency:  testCurrency,
		Recipient: testRecipient,
		Decimals:  6,
		ChainID:   1,
	})
	challenge, err := mpp.ParseChallenge(mpptest.FormatChallenge(issued))
	if !assert.NoErrorf(t, err,
		"ParseChallenge() error = %v", err) {
		return
	}

	_, err = method.CreateCredential(context.Background(), challenge)
	assert.ErrorContains(t, err, "challenge chain id mismatch")
}

func buildChallenge(t *testing.T, params tempo.ChargeRequestParams) *mpp.Challenge {
	t.Helper()

	request, err := tempo.NormalizeChargeRequest(params)
	if !assert.NoErrorf(t, err,
		"NormalizeChargeRequest() error = %v", err) {
		return *new(*mpp.Challenge)
	}

	return mpptest.NewChallenge(
		"secret-key",
		testRealm,
		tempo.MethodName,
		tempo.IntentCharge,
		request.Map(),
		mpp.WithExpires(mpp.ExpiresIn(5*time.Minute)),
	)
}

func TestHashCredentialSizePreflight(t *testing.T) {
	// 9000 produces a credential above 16 KiB from a challenge that fits its own
	// limit; mppx accepts it. 60000 exceeds the 64 KiB credential limit.
	for _, size := range []int{10, 9000, 60000} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			rpc := &mockRPC{chainID: 42431}
			method, err := New(Config{PaymentPolicy: func(context.Context, tempo.ChargeRequest) error { return nil }, ChainID: 42431, CredentialType: tempo.CredentialTypeHash, PrivateKey: testPrivateKey, RPC: rpc})
			if !assert.NoError(t, err) {
				return
			}
			challenge := buildChallenge(t, tempo.ChargeRequestParams{Amount: "0.50", Currency: testCurrency, Recipient: testRecipient, Decimals: 6, ChainID: 42431})
			challenge.Request["_mppx_scope"] = map[string]any{"query": strings.Repeat("a", size)}
			challenge.RequestB64 = ""
			if size < 60000 {
				_, err = mpp.FormatChallenge(challenge)
				if !assert.NoError(t, err) {
					return
				}
			}
			credential, err := method.CreateCredential(context.Background(), challenge)
			if size == 60000 {
				assert.Error(t, err)
				assert.Empty(t, rpc.sentRawTxs)
				return
			}
			if !assert.NoError(t, err) {
				return
			}
			assert.Len(t, rpc.sentRawTxs, 1)
			if size == 9000 {
				authorization, err := mpp.FormatCredential(credential)
				assert.NoError(t, err)
				assert.Greater(t, len(authorization), 16*1024)
			}
		})
	}
}

func TestCreateCredentialSharesInFlightSigning(t *testing.T) {
	t.Parallel()

	var policyCalls atomic.Int32
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	method, err := New(Config{
		ChainID:    42431,
		PrivateKey: testPrivateKey,
		RPC:        &mockRPC{chainID: 42431, gasPrice: "0x1", estimateGas: "0x5208"},
		PaymentPolicy: func(context.Context, tempo.ChargeRequest) error {
			policyCalls.Add(1)
			entered <- struct{}{}
			<-release
			return nil
		},
	})
	assert.NoError(t, err)
	challenge := buildChallenge(t, tempo.ChargeRequestParams{
		Amount:    "0.50",
		Currency:  testCurrency,
		Recipient: testRecipient,
		Decimals:  6,
		ChainID:   42431,
		FeePayer:  true,
	})

	var wg sync.WaitGroup
	credentials := make([]*mpp.Credential, 2)
	errs := make([]error, 2)
	call := func(index int) {
		defer wg.Done()
		credentials[index], errs[index] = method.CreateCredential(context.Background(), challenge)
	}
	wg.Add(2)
	go call(0)
	<-entered
	go call(1)
	waitForSigningWaiters(t, method, 1)
	close(release)
	wg.Wait()

	assert.NoError(t, errs[0])
	assert.NoError(t, errs[1])
	assert.Equal(t, int32(1), policyCalls.Load())
	assert.Equal(t, credentials[0].Payload, credentials[1].Payload)
	// Each caller owns its credential's maps.
	credentials[0].Payload["type"] = "mutated"
	assert.NotEqual(t, "mutated", credentials[1].Payload["type"])

	// A settled signing is not reused: a later call signs again.
	third, err := method.CreateCredential(context.Background(), challenge)
	assert.NoError(t, err)
	assert.NotNil(t, third)
	assert.Equal(t, int32(2), policyCalls.Load())
}

// waitForSigningWaiters waits until n calls have joined an in-flight signing.
func waitForSigningWaiters(t *testing.T, method *Method, n int) {
	t.Helper()
	require.Eventually(t, func() bool {
		method.signingMu.Lock()
		defer method.signingMu.Unlock()
		for _, call := range method.signing {
			if call.waiters >= n {
				return true
			}
		}
		return false
	}, 5*time.Second, time.Millisecond)
}

func TestCreateCredentialRetriesWhenLeadingContextEnds(t *testing.T) {
	t.Parallel()

	var policyCalls atomic.Int32
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	method, err := New(Config{
		ChainID:    42431,
		PrivateKey: testPrivateKey,
		RPC:        &mockRPC{chainID: 42431, gasPrice: "0x1", estimateGas: "0x5208"},
		PaymentPolicy: func(ctx context.Context, _ tempo.ChargeRequest) error {
			policyCalls.Add(1)
			entered <- struct{}{}
			select {
			case <-release:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		},
	})
	require.NoError(t, err)
	challenge := buildChallenge(t, tempo.ChargeRequestParams{
		Amount: "0.50", Currency: testCurrency, Recipient: testRecipient, Decimals: 6, ChainID: 42431, FeePayer: true,
	})

	leaderCtx, cancel := context.WithCancel(context.Background())
	leaderErr := make(chan error, 1)
	go func() {
		_, err := method.CreateCredential(leaderCtx, challenge)
		leaderErr <- err
	}()
	<-entered
	waiterResult := make(chan error, 1)
	go func() {
		credential, err := method.CreateCredential(context.Background(), challenge)
		if err == nil && credential == nil {
			err = errors.New("nil credential")
		}
		waiterResult <- err
	}()
	waitForSigningWaiters(t, method, 1)
	cancel()
	assert.ErrorIs(t, <-leaderErr, context.Canceled)
	// The waiter signs again under its own live context.
	<-entered
	close(release)
	assert.NoError(t, <-waiterResult)
	assert.Equal(t, int32(2), policyCalls.Load())
}

func TestCreateCredentialWaitersFailWhenSigningPanics(t *testing.T) {
	t.Parallel()

	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	method, err := New(Config{
		ChainID:    42431,
		PrivateKey: testPrivateKey,
		RPC:        &mockRPC{chainID: 42431, gasPrice: "0x1", estimateGas: "0x5208"},
		PaymentPolicy: func(context.Context, tempo.ChargeRequest) error {
			entered <- struct{}{}
			<-release
			panic("policy exploded")
		},
	})
	require.NoError(t, err)
	challenge := buildChallenge(t, tempo.ChargeRequestParams{
		Amount: "0.50", Currency: testCurrency, Recipient: testRecipient, Decimals: 6, ChainID: 42431, FeePayer: true,
	})

	leaderPanic := make(chan any, 1)
	go func() {
		defer func() { leaderPanic <- recover() }()
		_, _ = method.CreateCredential(context.Background(), challenge)
	}()
	<-entered
	waiterErr := make(chan error, 1)
	go func() {
		_, err := method.CreateCredential(context.Background(), challenge)
		waiterErr <- err
	}()
	waitForSigningWaiters(t, method, 1)
	close(release)
	assert.Equal(t, "policy exploded", <-leaderPanic)
	assert.ErrorIs(t, <-waiterErr, errSigningPanicked)
}

func TestCreateCredentialDoesNotShareAcrossDifferentChallenges(t *testing.T) {
	t.Parallel()

	var policyCalls atomic.Int32
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	method, err := New(Config{
		ChainID:    42431,
		PrivateKey: testPrivateKey,
		RPC:        &mockRPC{chainID: 42431, gasPrice: "0x1", estimateGas: "0x5208"},
		PaymentPolicy: func(context.Context, tempo.ChargeRequest) error {
			policyCalls.Add(1)
			entered <- struct{}{}
			<-release
			return nil
		},
	})
	require.NoError(t, err)
	params := tempo.ChargeRequestParams{
		Amount: "0.50", Currency: testCurrency, Recipient: testRecipient, Decimals: 6, ChainID: 42431, FeePayer: true,
	}
	first := buildChallenge(t, params)
	params.Amount = "0.75"
	second := buildChallenge(t, params)
	// Same ID and realm, different request: must not join the first signing.
	second.ID, second.Realm = first.ID, first.Realm

	var wg sync.WaitGroup
	wg.Add(2)
	for _, challenge := range []*mpp.Challenge{first, second} {
		go func() {
			defer wg.Done()
			_, err := method.CreateCredential(context.Background(), challenge)
			assert.NoError(t, err)
		}()
	}
	<-entered
	<-entered
	close(release)
	wg.Wait()
	assert.Equal(t, int32(2), policyCalls.Load())
}
