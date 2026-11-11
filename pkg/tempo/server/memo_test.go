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

	"github.com/tempoxyz/mpp-go/pkg/internal/mpptest"
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

func TestChargeFlow_PrimaryAttributionMemoIsRequiredForPush(t *testing.T) {
	ctx := context.Background()
	request := buildRequest(t, false, nil)

	// A hash credential proves only that a transfer reached the recipient,
	// so without methodDetails.memo it must carry this challenge's
	// attribution memo: a plain or unrelated transfer could be anyone's.
	for name, memo := range map[string]string{
		"without memo":              "",
		"with non-attribution memo": serverMemo,
	} {
		t.Run("hash "+name, func(t *testing.T) {
			credential, rpc := plainHashCredential(t, request, memo)
			intent, err := NewIntent(IntentConfig{Store: tempo.NewMemoryStore(), RPC: rpc})
			require.NoError(t, err)
			_, err = intent.Verify(ctx, credential, request.Map())
			assert.ErrorIs(t, err, mpp.ErrorTypeVerificationFailed)
		})
	}

	t.Run("hash with attribution memo", func(t *testing.T) {
		challenge := buildChallenge(t, request)
		credential, rpc := plainHashCredential(t, request, tempo.EncodeAttribution(testRealm, "", challenge.ID))
		credential.Challenge = challenge.ToEcho()
		intent, err := NewIntent(IntentConfig{Store: tempo.NewMemoryStore(), RPC: rpc})
		require.NoError(t, err)
		receipt, err := intent.Verify(ctx, credential, request.Map())
		require.NoError(t, err)
		assert.Equal(t, testReceiptHash, receipt.Reference)
	})
}

func TestChargeFlow_PrimaryAttributionMemoIsOptionalForPull(t *testing.T) {
	ctx := context.Background()
	request := buildRequest(t, false, nil)
	t.Run("transaction with non-attribution memo", func(t *testing.T) {
		// The client carries the challenge's memo; the server verifies
		// against a request without methodDetails.memo, which accepts a
		// plain transfer (TestMatchTransfers_PushRequiresAttribution) or any
		// memo but a mismatched attribution memo, because the server
		// broadcasts the client's own signed transaction.
		rpc := newMockRPC(request)
		credential, err := newClientMethod(t, rpc, tempo.CredentialTypeTransaction).CreateCredential(ctx, buildChallenge(t, withMemo(request, serverMemo)))
		require.NoError(t, err)
		intent, err := NewIntent(IntentConfig{Store: tempo.NewMemoryStore(), RPC: rpc})
		require.NoError(t, err)
		_, err = intent.Verify(ctx, credential, request.Map())
		require.NoError(t, err)
	})

	// A push payment the client made for a challenge with
	// methodDetails.memo does not pay one without it.
	rpc := newMockRPC(request)
	credential, err := newClientMethod(t, rpc, tempo.CredentialTypeHash).CreateCredential(ctx, buildChallenge(t, withMemo(request, serverMemo)))
	require.NoError(t, err)
	intent, err := NewIntent(IntentConfig{Store: tempo.NewMemoryStore(), RPC: rpc})
	require.NoError(t, err)
	_, err = intent.Verify(ctx, credential, request.Map())
	assert.ErrorIs(t, err, mpp.ErrorTypeVerificationFailed)
}

// A challenge that configures this realm's attribution memo for another
// challenge as its methodDetails.memo must not accept the payment bound to
// that challenge, whose replay key may have expired.
func TestChargeFlow_ConfiguredMemoNeverRedeemsAnotherChallengesPayment(t *testing.T) {
	ctx := context.Background()
	base := buildRequest(t, false, nil)
	verify := func(memo string) error {
		request := withMemo(base, memo)
		rpc := newMockRPC(request)
		credential, err := newClientMethod(t, rpc, tempo.CredentialTypeTransaction).CreateCredential(ctx, buildChallenge(t, request))
		require.NoError(t, err)
		intent, err := NewIntent(IntentConfig{Store: tempo.NewMemoryStore(), RPC: rpc})
		require.NoError(t, err)
		_, err = intent.Verify(ctx, credential, request.Map())
		return err
	}
	assert.ErrorContains(t, verify(tempo.EncodeAttribution(testRealm, "", "earlier-challenge")), "does not contain a matching Tempo transfer")

	// Another realm's attribution memo, such as a partner's, still works.
	require.NoError(t, verify(tempo.EncodeAttribution("partner.example.com", "", "partner-challenge")))
}

// A configured methodDetails.memo is shared by every payer on the route, so
// it cannot tie an unsigned hash credential to one challenge: hash
// credentials are refused on such routes, even for a transfer that carries
// the memo.
func TestChargeFlow_HashCredentialRejectedWithMethodDetailsMemo(t *testing.T) {
	ctx := context.Background()
	request := withMemo(buildRequest(t, false, nil), serverMemo)
	credential, rpc := plainHashCredential(t, request, serverMemo)
	store := tempo.NewMemoryStore()
	intent, err := NewIntent(IntentConfig{Store: store, RPC: rpc})
	require.NoError(t, err)
	_, err = intent.Verify(ctx, credential, request.Map())
	require.ErrorIs(t, err, mpp.ErrorTypeVerificationFailed)
	assert.ErrorContains(t, err, "hash credentials are not accepted when methodDetails.memo is set")
	_, err = intent.Validate(ctx, credential, request.Map())
	require.ErrorIs(t, err, mpp.ErrorTypeVerificationFailed)
	// The transfer stays unclaimed for its payer's own credential.
	_, exists, err := store.Get(ctx, tempo.ChargeStoreKey(testReceiptHash))
	require.NoError(t, err)
	assert.False(t, exists)
}

func TestBuildChargeRequestMemoOffersOnlyPull(t *testing.T) {
	newTestMethod := func(modes ...tempo.ChargeMode) *Method {
		return newMethod(methodConfig{Intent: testStoredIntent(),
			Currencies:     []string{testCurrency},
			Recipient:      testRecipient,
			Decimals:       6,
			ChainID:        42431,
			SupportedModes: modes,
		})
	}
	modes := func(requestMap map[string]any) any {
		return requestMap["methodDetails"].(map[string]any)["supportedModes"]
	}

	requestMap, err := newTestMethod().BuildChargeRequest(server.ChargeParams{Amount: "0.50", Memo: serverMemo})
	require.NoError(t, err)
	assert.Equal(t, []string{"pull"}, modes(requestMap))

	requestMap, err = newTestMethod(tempo.ChargeModePush, tempo.ChargeModePull).BuildChargeRequest(server.ChargeParams{Amount: "0.50", Memo: serverMemo})
	require.NoError(t, err)
	assert.Equal(t, []string{"pull"}, modes(requestMap))

	_, err = newTestMethod().BuildChargeRequest(server.ChargeParams{Amount: "0.50", Memo: serverMemo, SupportedModes: []tempo.ChargeMode{tempo.ChargeModePush}})
	assert.ErrorContains(t, err, "ChargeParams.Memo cannot be combined with push-only SupportedModes")
	_, err = newTestMethod(tempo.ChargeModePush).BuildChargeRequest(server.ChargeParams{Amount: "0.50", Memo: serverMemo})
	assert.ErrorContains(t, err, "ChargeParams.Memo cannot be combined with push-only SupportedModes")

	// Without a memo, push mode stays available.
	requestMap, err = newTestMethod().BuildChargeRequest(server.ChargeParams{Amount: "0.50"})
	require.NoError(t, err)
	assert.Nil(t, modes(requestMap))
	requestMap, err = newTestMethod(tempo.ChargeModePush).BuildChargeRequest(server.ChargeParams{Amount: "0.50"})
	require.NoError(t, err)
	assert.Equal(t, []string{"push"}, modes(requestMap))
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
	for _, credentialType := range []tempo.CredentialType{tempo.CredentialTypeTransaction} {
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
	upper := []decodedTransfer{{amount: request.Amount, recipient: request.Recipient, hasMemo: true, memo: "0x" + strings.Repeat("AB", 32)}}
	assert.True(t, matchTransfers(upper, expectedTransfers(request), testRealm, "challenge-1"))
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

	t.Run("plain transaction", func(t *testing.T) {
		rpc := newMockRPC(request)
		credential, err := newClientMethod(t, rpc, tempo.CredentialTypeTransaction).CreateCredential(ctx, buildChallenge(t, withMemo(request, serverMemo)))
		require.NoError(t, err)
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

func TestMatchTransfers_PushRequiresAttribution(t *testing.T) {
	t.Parallel()
	const challengeID = "challenge-1"
	request := tempo.ChargeRequest{Amount: "1000000", Recipient: testRecipient}
	transfer := func(memo string) []decodedTransfer {
		return []decodedTransfer{{amount: "1000000", recipient: testRecipient, hasMemo: memo != "", memo: memo}}
	}
	bound := tempo.EncodeAttribution(testRealm, "", challengeID)
	for name, tt := range map[string]struct {
		memo       string
		pull, push bool
	}{
		"plain":           {"", true, false},
		"custom":          {serverMemo, true, false},
		"bound":           {bound, true, true},
		"other challenge": {tempo.EncodeAttribution(testRealm, "", "other"), false, false},
		"other realm":     {tempo.EncodeAttribution("other.example.com", "", challengeID), false, false},
	} {
		assert.Equal(t, tt.pull, matchTransfers(transfer(tt.memo), expectedTransfers(request), testRealm, challengeID), "pull/%s", name)
		assert.Equal(t, tt.push, matchTransfers(transfer(tt.memo), expectedPushTransfers(request), testRealm, challengeID), "push/%s", name)
	}

	// With methodDetails.memo, pull requires exactly that memo. Push still
	// requires the attribution memo; validateHash refuses push credentials
	// on such routes before matching.
	request.MethodDetails.Memo = serverMemo
	assert.True(t, matchTransfers(transfer(serverMemo), expectedTransfers(request), testRealm, challengeID))
	assert.False(t, matchTransfers(transfer(bound), expectedTransfers(request), testRealm, challengeID))
	assert.False(t, matchTransfers(transfer(""), expectedTransfers(request), testRealm, challengeID))
	assert.False(t, matchTransfers(transfer(serverMemo), expectedPushTransfers(request), testRealm, challengeID))
	assert.True(t, matchTransfers(transfer(bound), expectedPushTransfers(request), testRealm, challengeID))
}

// A pull payment's broadcast transaction is recorded under the same replay
// key push (hash) credentials check, and a challenge whose static
// methodDetails.memo the transfer satisfies refuses hash credentials, so a
// transaction settled for one challenge can't be redeemed as a push
// credential for another.
func TestChargeFlow_PullSettledHashCannotPayAnotherChallengeAsPush(t *testing.T) {
	for _, feePayer := range []bool{false, true} {
		t.Run(map[bool]string{false: "transaction", true: "sponsored"}[feePayer], func(t *testing.T) {
			testPullSettledHashCannotPayAnotherChallengeAsPush(t, feePayer)
		})
	}
}

func testPullSettledHashCannotPayAnotherChallengeAsPush(t *testing.T, feePayer bool) {
	ctx := context.Background()
	request := withMemo(buildRequest(t, feePayer, nil), serverMemo)
	pushRequest := withMemo(buildRequest(t, false, nil), serverMemo)
	rpc := newMockRPC(request)
	store := tempo.NewMemoryStore()
	config := IntentConfig{Store: store, RPC: rpc}
	if feePayer {
		config.FeePayerPrivateKey = feePayerKey
	}
	intent, err := NewIntent(config)
	require.NoError(t, err)

	pull, err := newClientMethod(t, rpc, tempo.CredentialTypeTransaction).CreateCredential(ctx, buildChallenge(t, request))
	require.NoError(t, err)
	receipt, err := intent.Verify(ctx, pull, request.Map())
	require.NoError(t, err)
	txHash := receipt.Reference

	// The same transaction, presented as a push credential for another
	// challenge, satisfies its transfer and memo checks.
	signer, err := temposigner.NewSigner(testPrivateKey)
	require.NoError(t, err)
	sender := signer.Address().Hex()
	amount, ok := new(big.Int).SetString(request.Amount, 10)
	require.True(t, ok)
	rpc.receipts[txHash] = map[string]any{"status": "0x1", "logs": []any{
		transferLog(request.Currency, sender, request.Recipient, amount, ""),
		transferLog(request.Currency, sender, request.Recipient, amount, serverMemo),
	}}
	other := mpptest.NewChallenge("secret-key", testRealm, tempo.MethodName, tempo.IntentCharge, pushRequest.Map(),
		mpp.WithExpires(mpp.ExpiresIn(10*time.Minute)))
	require.NotEqual(t, pull.Challenge.ID, other.ID)
	push := other.NewCredential(
		map[string]any{"type": "hash", "hash": txHash},
		mpp.WithCredentialSource("did:pkh:eip155:42431:"+sender),
	)

	// A server that never counted the transaction refuses it too: the
	// shared memo does not bind the hash to the other challenge.
	fresh, err := NewIntent(IntentConfig{Store: tempo.NewMemoryStore(), RPC: rpc})
	require.NoError(t, err)
	_, err = fresh.Verify(ctx, push, pushRequest.Map())
	require.ErrorIs(t, err, mpp.ErrorTypeVerificationFailed)
	assert.ErrorContains(t, err, "hash credentials are not accepted when methodDetails.memo is set")

	// This one counted it for the pull credential and refuses it.
	_, err = intent.Verify(ctx, push, pushRequest.Map())
	require.Error(t, err)
	_, exists, err := store.Get(ctx, tempo.ChargeStoreKey(txHash))
	require.NoError(t, err)
	assert.True(t, exists)
}
