package chargeserver

import (
	"context"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	temposigner "github.com/tempoxyz/tempo-go/pkg/signer"

	"github.com/tempoxyz/mpp-go/pkg/mpp"
	"github.com/tempoxyz/mpp-go/pkg/server"
	"github.com/tempoxyz/mpp-go/pkg/tempo"
)

// serverMemo is a server-set methodDetails.memo in no particular layout.
var serverMemo = "0x" + strings.Repeat("ab", 32)

// withMemo returns request with methodDetails.memo set to memo.
func withMemo(request tempo.ChargeRequest, memo string) tempo.ChargeRequest {
	request.MethodDetails.Memo = memo
	return request
}

// plainHashCredential returns a hash credential for testReceiptHash, whose
// receipt carries one transfer of the charge with memo (none when empty).
func plainHashCredential(t *testing.T, request tempo.ChargeRequest, memo string) (*mpp.Credential, *mockRPC) {
	t.Helper()
	signer, err := temposigner.NewSigner(testPrivateKey)
	require.NoError(t, err)
	sender := signer.Address().Hex()
	amount, ok := new(big.Int).SetString(request.Amount, 10)
	require.True(t, ok)
	logs := []any{transferLog(request.Currency, sender, request.Recipient, amount, "")}
	if memo != "" {
		logs = append(logs, transferLog(request.Currency, sender, request.Recipient, amount, memo))
	}
	rpc := newMockRPC(request)
	rpc.receipts[testReceiptHash] = map[string]any{"status": "0x1", "logs": logs}
	challenge := buildChallenge(t, request)
	credential := challenge.NewCredential(
		map[string]any{"type": "hash", "hash": testReceiptHash},
		mpp.WithCredentialSource("did:pkh:eip155:42431:"+sender),
	)
	return credential, rpc
}

func TestChargeFlow_PrimaryAttributionMemoIsOptional(t *testing.T) {
	ctx := context.Background()
	request := buildRequest(t, false, nil)

	t.Run("hash without memo", func(t *testing.T) {
		credential, rpc := plainHashCredential(t, request, "")
		intent, err := NewIntent(IntentConfig{Store: tempo.NewMemoryStore(), RPC: rpc})
		require.NoError(t, err)
		receipt, err := intent.Verify(ctx, credential, request.Map())
		require.NoError(t, err)
		assert.Equal(t, testReceiptHash, receipt.Reference)
	})

	t.Run("hash with non-attribution memo", func(t *testing.T) {
		credential, rpc := plainHashCredential(t, request, serverMemo)
		intent, err := NewIntent(IntentConfig{Store: tempo.NewMemoryStore(), RPC: rpc})
		require.NoError(t, err)
		_, err = intent.Verify(ctx, credential, request.Map())
		require.NoError(t, err)
	})

	for _, credentialType := range []tempo.CredentialType{tempo.CredentialTypeTransaction, tempo.CredentialTypeHash} {
		t.Run(string(credentialType)+" with non-attribution memo", func(t *testing.T) {
			// The client carries the challenge's memo; the server verifies
			// against a request without one, which accepts any memo but a
			// mismatched attribution memo.
			rpc := newMockRPC(request)
			credential, err := newClientMethod(t, rpc, credentialType).CreateCredential(ctx, buildChallenge(t, withMemo(request, serverMemo)))
			require.NoError(t, err)
			intent, err := NewIntent(IntentConfig{Store: tempo.NewMemoryStore(), RPC: rpc})
			require.NoError(t, err)
			_, err = intent.Verify(ctx, credential, request.Map())
			require.NoError(t, err)
		})
	}
}

func TestChargeFlow_RejectsAttributionMemoForAnotherChallenge(t *testing.T) {
	ctx := context.Background()
	request := buildRequest(t, false, nil)
	for _, credentialType := range []tempo.CredentialType{tempo.CredentialTypeTransaction, tempo.CredentialTypeHash} {
		for name, mutate := range map[string]func(*mpp.Credential){
			"other challenge": func(credential *mpp.Credential) { credential.Challenge.ID = "other-challenge" },
			"other realm":     func(credential *mpp.Credential) { credential.Challenge.Realm = "other.example.com" },
		} {
			t.Run(string(credentialType)+"/"+name, func(t *testing.T) {
				rpc := newMockRPC(request)
				credential, err := newClientMethod(t, rpc, credentialType).CreateCredential(ctx, buildChallenge(t, request))
				require.NoError(t, err)
				mutate(credential)
				intent, err := NewIntent(IntentConfig{Store: tempo.NewMemoryStore(), RPC: rpc})
				require.NoError(t, err)
				_, err = intent.Verify(ctx, credential, request.Map())
				if credentialType == tempo.CredentialTypeHash {
					assert.ErrorIs(t, err, mpp.ErrorTypeVerificationFailed)
				} else {
					assert.ErrorContains(t, err, "does not contain a matching Tempo transfer")
				}
			})
		}
	}

	// A receipt-only (hash) payment carrying another challenge's
	// attribution memo is rejected the same way.
	credential, rpc := plainHashCredential(t, request, tempo.EncodeAttribution(testRealm, "", "other-challenge"))
	intent, err := NewIntent(IntentConfig{Store: tempo.NewMemoryStore(), RPC: rpc})
	require.NoError(t, err)
	_, err = intent.Verify(ctx, credential, request.Map())
	assert.ErrorIs(t, err, mpp.ErrorTypeVerificationFailed)
}

func TestChargeFlow_MethodDetailsMemoIsRequiredExactly(t *testing.T) {
	ctx := context.Background()
	base := buildRequest(t, false, nil)
	for _, credentialType := range []tempo.CredentialType{tempo.CredentialTypeTransaction, tempo.CredentialTypeHash} {
		t.Run(string(credentialType), func(t *testing.T) {
			request := withMemo(base, serverMemo)
			verify := func(challengeRequest tempo.ChargeRequest) error {
				rpc := newMockRPC(request)
				credential, err := newClientMethod(t, rpc, credentialType).CreateCredential(ctx, buildChallenge(t, challengeRequest))
				require.NoError(t, err)
				intent, err := NewIntent(IntentConfig{Store: tempo.NewMemoryStore(), RPC: rpc})
				require.NoError(t, err)
				_, err = intent.Verify(ctx, credential, request.Map())
				return err
			}
			// The client carries the challenge's memo: accepted.
			require.NoError(t, verify(request))
			// An attribution memo instead of the server's memo: rejected.
			assert.Error(t, verify(base))
			// Another memo: rejected.
			assert.Error(t, verify(withMemo(base, "0x"+strings.Repeat("cd", 32))))
		})
	}

	// A transfer without any memo does not carry the server's memo.
	request := withMemo(base, serverMemo)
	credential, rpc := plainHashCredential(t, request, "")
	intent, err := NewIntent(IntentConfig{Store: tempo.NewMemoryStore(), RPC: rpc})
	require.NoError(t, err)
	_, err = intent.Verify(ctx, credential, request.Map())
	assert.ErrorIs(t, err, mpp.ErrorTypeVerificationFailed)

	// The server memo is compared case-insensitively.
	credential, rpc = plainHashCredential(t, request, "0x"+strings.Repeat("AB", 32))
	intent, err = NewIntent(IntentConfig{Store: tempo.NewMemoryStore(), RPC: rpc})
	require.NoError(t, err)
	_, err = intent.Verify(ctx, credential, request.Map())
	require.NoError(t, err)
}

func TestBuildChargeRequestCarriesMemo(t *testing.T) {
	method := newMethod(methodConfig{Intent: testStoredIntent(),
		Currencies: []string{testCurrency},
		Recipient:  testRecipient,
		Decimals:   6,
		ChainID:    42431,
	})
	requestMap, err := method.BuildChargeRequest(server.ChargeParams{Amount: "0.50", Memo: "0x" + strings.Repeat("AB", 32)})
	require.NoError(t, err)
	assert.Equal(t, serverMemo, requestMap["methodDetails"].(map[string]any)["memo"])

	_, err = method.BuildChargeRequest(server.ChargeParams{Amount: "0.50", Memo: "0x1234"})
	assert.ErrorContains(t, err, "memo must be exactly 32 bytes")

	requestMap, err = method.BuildChargeRequest(server.ChargeParams{Amount: "0.50"})
	require.NoError(t, err)
	assert.NotContains(t, requestMap["methodDetails"], "memo")
}

func TestReplayClaimsPersistWithoutChallengeBoundMemo(t *testing.T) {
	ctx := context.Background()
	request := buildRequest(t, false, nil)
	newIntent := func(rpc *mockRPC) (*Intent, *expiringRecordingStore) {
		store := &expiringRecordingStore{MemoryStore: tempo.NewMemoryStore(), expiries: map[string]time.Time{}}
		intent, err := NewIntent(IntentConfig{Store: store, RPC: rpc, ExpireReplayKeys: true})
		require.NoError(t, err)
		return intent, store
	}
	assertPersisted := func(t *testing.T, intent *Intent, store *expiringRecordingStore, credential *mpp.Credential, verifyRequest tempo.ChargeRequest) {
		t.Helper()
		receipt, err := intent.Verify(ctx, credential, verifyRequest.Map())
		require.NoError(t, err)
		// The payment names no challenge, so it could pay a later one once
		// its key expired: the key is kept without an expiry.
		assert.Empty(t, store.expiries)
		_, exists, err := store.Get(ctx, tempo.ChargeStoreKey(receipt.Reference))
		require.NoError(t, err)
		assert.True(t, exists)
		_, err = intent.Verify(ctx, credential, verifyRequest.Map())
		assert.ErrorIs(t, err, mpp.ErrorTypeInvalidChallenge)
	}

	t.Run("hash without memo", func(t *testing.T) {
		credential, rpc := plainHashCredential(t, request, "")
		intent, store := newIntent(rpc)
		assertPersisted(t, intent, store, credential, request)
	})
	t.Run("hash with non-attribution memo", func(t *testing.T) {
		credential, rpc := plainHashCredential(t, request, serverMemo)
		intent, store := newIntent(rpc)
		assertPersisted(t, intent, store, credential, request)
	})
	for _, feePayer := range []bool{false, true} {
		t.Run(map[bool]string{false: "transaction", true: "sponsored"}[feePayer]+" with methodDetails.memo", func(t *testing.T) {
			memoRequest := withMemo(buildRequest(t, feePayer, nil), serverMemo)
			rpc := newMockRPC(memoRequest)
			credential, err := newClientMethod(t, rpc, tempo.CredentialTypeTransaction).CreateCredential(ctx, buildChallenge(t, memoRequest))
			require.NoError(t, err)
			store := &expiringRecordingStore{MemoryStore: tempo.NewMemoryStore(), expiries: map[string]time.Time{}}
			config := IntentConfig{Store: store, RPC: rpc, ExpireReplayKeys: true}
			if feePayer {
				config.FeePayerPrivateKey = feePayerKey
			}
			intent, err := NewIntent(config)
			require.NoError(t, err)
			assertPersisted(t, intent, store, credential, memoRequest)
		})
	}
}

func TestProofReplayIsRejectedBeforeSignatureVerification(t *testing.T) {
	ctx := context.Background()
	request := buildRequest(t, false, nil)
	request.Amount = "0"
	rpc := newMockRPC(request)
	credential, err := newClientMethod(t, rpc, tempo.CredentialTypeProof).CreateCredential(ctx, buildChallenge(t, request))
	require.NoError(t, err)
	store := tempo.NewMemoryStore()
	intent, err := NewIntent(IntentConfig{Store: store, RPC: rpc})
	require.NoError(t, err)
	_, err = intent.Verify(ctx, credential, request.Map())
	require.NoError(t, err)

	// A replay with a broken signature still gets the replay error, not a
	// signature error: replay is checked first.
	replay := *credential
	replay.Payload = map[string]any{"type": "proof", "signature": "0x00"}
	_, err = intent.Validate(ctx, &replay, request.Map())
	require.ErrorIs(t, err, mpp.ErrorTypeInvalidChallenge)
	assert.ErrorContains(t, err, "proof credential already used")
}
