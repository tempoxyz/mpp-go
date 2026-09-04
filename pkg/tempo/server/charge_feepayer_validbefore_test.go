package chargeserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/assert"
	"github.com/tempoxyz/mpp-go/pkg/tempo"
	temposigner "github.com/tempoxyz/tempo-go/pkg/signer"
	tempotx "github.com/tempoxyz/tempo-go/pkg/transaction"
)

// TestChargeFlow_FeePayerTransactionViaRemoteSignerRejectsZeroValidBefore
// proves that a remote fee payer response can't sponsor a transaction with
// ValidBefore reset to 0. Before the fix, validateFeePayerTransaction only
// enforced the sponsor-policy upper bound when ValidBefore was non-zero, and
// the standalone "ValidBefore == 0 || expired" guard applied to the original
// client-submitted tx was never re-applied to the co-signed tx returned by
// the remote fee payer, so a co-signed ValidBefore of 0 slipped through both
// checks.
func TestChargeFlow_FeePayerTransactionViaRemoteSignerRejectsZeroValidBefore(t *testing.T) {
	ctx := context.Background()
	request := buildRequest(t, true, nil)
	rpc := newMockRPC(request)
	clientMethod := newClientMethod(t, rpc, tempo.CredentialTypeTransaction)
	challenge := buildChallenge(t, request)

	credential, err := clientMethod.CreateCredential(ctx, challenge)
	if !assert.NoErrorf(t, err,
		"CreateCredential() error = %v", err) {
		return
	}

	raw := credential.Payload["signature"].(string)

	feePayerSigner, err := temposigner.NewSigner(feePayerKey)
	if !assert.NoErrorf(t, err,
		"NewSigner() error = %v", err) {
		return
	}

	feePayerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		coSignedTx, err := tempotx.Deserialize(raw)
		if !assert.NoErrorf(t, err,
			"Deserialize(raw) error = %v", err) {
			return
		}

		sender, err := tempotx.VerifySignature(coSignedTx)
		if !assert.NoErrorf(t, err,
			"VerifySignature() error = %v", err) {
			return
		}

		coSignedTx.From = sender
		coSignedTx.FeeToken = common.HexToAddress(request.Currency)
		coSignedTx.AwaitingFeePayer = false
		// The regression under test: the remote fee payer hands back a
		// transaction with ValidBefore zeroed out instead of preserving the
		// caller's original expiry.
		coSignedTx.ValidBefore = 0
		{
			err := tempotx.AddFeePayerSignature(coSignedTx, feePayerSigner)
			if !assert.NoErrorf(t, err,
				"AddFeePayerSignature() error = %v", err) {
				return
			}
		}

		serialized, err := tempotx.Serialize(coSignedTx, nil)
		if !assert.NoErrorf(t, err,
			"Serialize() error = %v", err) {
			return
		}

		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": serialized})
	}))
	defer feePayerServer.Close()
	request.MethodDetails.FeePayerURL = feePayerServer.URL

	intent, err := NewIntent(IntentConfig{RPC: rpc})
	if !assert.NoErrorf(t, err,
		"NewIntent() error = %v", err) {
		return
	}

	if _, err := intent.Verify(ctx, credential, request.Map()); err == nil || !strings.Contains(err.Error(), "expired") {
		assert.Failf(t, "", "Verify() error = %v, want expiry rejection", err)
		return
	}
	if !assert.Lenf(t, rpc.sentRawTxs, 0,
		"expected zero-ValidBefore transaction to be rejected before broadcast, got %d broadcasts", len(rpc.sentRawTxs)) {
		return
	}
}
