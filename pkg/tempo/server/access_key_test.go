package chargeserver

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	temporpc "github.com/tempoxyz/tempo-go/pkg/client"
	"github.com/tempoxyz/tempo-go/pkg/keychain"
	temposigner "github.com/tempoxyz/tempo-go/pkg/signer"
	tempotx "github.com/tempoxyz/tempo-go/pkg/transaction"

	"github.com/tempoxyz/mpp-go/pkg/tempo"
	chargeclient "github.com/tempoxyz/mpp-go/pkg/tempo/client"
)

const (
	accessRootKey = "0x59c6995e998f97a5a0044966f0945389dc9e86dae88c7a8412f4603b6b78690d"
	accessKeyKey  = "0x5de4111afa1a4b94908f83103eb1f1706367c2e68ca870fc3fb9a804cdab365a"
)

// accessKeyClient pays as an access key for the root account, carrying the
// root's authorization of the key until the keychain reports it.
func accessKeyClient(t *testing.T, rpc tempo.RPCClient, credentialType tempo.CredentialType) (*chargeclient.Method, common.Address, common.Address) {
	t.Helper()
	root, err := temposigner.NewSigner(accessRootKey)
	require.NoError(t, err)
	key, err := temposigner.NewSigner(accessKeyKey)
	require.NoError(t, err)
	authorization, err := keychain.NewKeyAuthorization(42431, 0, key.Address()).Sign(root)
	require.NoError(t, err)
	method, err := chargeclient.New(chargeclient.Config{
		PaymentPolicy:  func(context.Context, tempo.ChargeRequest) error { return nil },
		AccessKey:      &chargeclient.AccessKey{Signer: key, Account: root.Address(), KeyAuthorization: authorization},
		RPC:            rpc,
		ChainID:        42431,
		CredentialType: credentialType,
	})
	require.NoError(t, err)
	return method, root.Address(), key.Address()
}

// keyInfo answers AccountKeychain.getKey with keyID (zero: not authorized),
// active for an hour.
func keyInfo(keyID common.Address) func(params ...interface{}) (*temporpc.JSONRPCResponse, error) {
	return func(...interface{}) (*temporpc.JSONRPCResponse, error) {
		expiry := fmt.Sprintf("%064x", time.Now().Add(time.Hour).Unix())
		return &temporpc.JSONRPCResponse{Result: "0x" + strings.Repeat("0", 64) + strings.Repeat("0", 24) +
			strings.TrimPrefix(strings.ToLower(keyID.Hex()), "0x") + expiry + strings.Repeat("0", 64*2)}, nil
	}
}

func TestAccessKeyProofVerifies(t *testing.T) {
	ctx := context.Background()
	request, err := tempo.NormalizeChargeRequest(tempo.ChargeRequestParams{
		Amount: "0", Currency: testCurrency, Recipient: testRecipient, Decimals: 6, ChainID: 42431,
	})
	require.NoError(t, err)
	rpc := newMockRPC(request)
	method, root, key := accessKeyClient(t, rpc, "")
	rpc.onCall = keyInfo(key)

	credential, err := method.CreateCredential(ctx, buildChallenge(t, request))
	require.NoError(t, err)
	assert.Equal(t, tempo.ProofSource(42431, root), credential.Source)
	signature := common.FromHex(credential.Payload["signature"].(string))
	require.Len(t, signature, keychain.KeychainSignatureLength)
	assert.Equal(t, byte(keychain.KeychainSignatureType), signature[0])

	intent, err := NewIntent(IntentConfig{Store: tempo.NewMemoryStore(), RPC: rpc})
	require.NoError(t, err)
	_, err = intent.Verify(ctx, credential, request.Map())
	require.NoError(t, err)
}

func TestAccessKeySignsTransactionsForTheRootAccount(t *testing.T) {
	for _, tt := range []struct {
		name          string
		authorized    bool
		authorization bool
	}{
		{name: "first use carries the key authorization", authorized: false, authorization: true},
		{name: "authorized key omits it", authorized: true, authorization: false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			request := buildRequest(t, false, nil)
			rpc := newMockRPC(request)
			method, root, key := accessKeyClient(t, rpc, tempo.CredentialTypeTransaction)
			onChain := common.Address{}
			if tt.authorized {
				onChain = key
			}
			rpc.onCall = keyInfo(onChain)

			credential, err := method.CreateCredential(ctx, buildChallenge(t, request))
			require.NoError(t, err)
			assert.Equal(t, tempo.ProofSource(42431, root), credential.Source)
			tx, err := tempotx.Deserialize(credential.Payload["signature"].(string))
			require.NoError(t, err)
			assert.Equal(t, tt.authorization, tx.KeyAuthorization != nil)
			signedKey, signedRoot, err := keychain.VerifyAccessKeySignature(tx)
			require.NoError(t, err)
			assert.Equal(t, key, signedKey)
			assert.Equal(t, root, signedRoot)
		})
	}
}

func TestAccessKeyConfigIsValidated(t *testing.T) {
	key, err := temposigner.NewSigner(accessKeyKey)
	require.NoError(t, err)
	for _, config := range []chargeclient.Config{
		{AccessKey: &chargeclient.AccessKey{Account: common.Address{1}}},
		{AccessKey: &chargeclient.AccessKey{Signer: key}},
		{AccessKey: &chargeclient.AccessKey{Signer: key, Account: common.Address{1}}, PrivateKey: accessRootKey},
	} {
		_, err := chargeclient.New(config)
		require.Error(t, err)
	}
}
