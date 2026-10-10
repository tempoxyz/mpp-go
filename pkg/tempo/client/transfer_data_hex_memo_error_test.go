package chargeclient

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/tempoxyz/mpp-go/pkg/mpp"
	"github.com/tempoxyz/mpp-go/pkg/tempo"
)

// TestCreateCredentialRejectsMalformedMemoInsteadOfBroadcastingEmptyCalldata
// proves that a ChargeRequest with a malformed MethodDetails.Memo causes
// CreateCredential to fail loudly instead of silently signing and returning
// a transaction with empty calldata.
//
// ChargeRequest is an exported struct; nothing forces a caller to build one
// through NormalizeChargeRequest, so a hand-constructed (or otherwise
// unvalidated) ChargeRequest with a malformed memo can reach the client
// directly via a Challenge's request payload. Before this fix,
// transferDataHex discarded the error from EncodeTransferWithMemo and
// returned "" as calldata, which common.FromHex("") turns into an empty
// byte slice: the client would go on to build, sign, and return a real
// transaction that calls the token contract with no data at all, instead of
// the intended transferWithMemo call - silently dropping the transfer.
func TestCreateCredentialRejectsMalformedMemoInsteadOfBroadcastingEmptyCalldata(t *testing.T) {
	t.Parallel()

	rpc := &mockRPC{chainID: 42431, gasPrice: "0x3b9aca00", estimateGas: "0x5208"}
	method, err := New(Config{
		PrivateKey:     testPrivateKey,
		ChainID:        42431,
		RPC:            rpc,
		CredentialType: tempo.CredentialTypeTransaction,
	})
	if !assert.NoErrorf(t, err,
		"New() error = %v", err) {
		return
	}

	// Bypass NormalizeChargeRequest entirely, mirroring a hand-built or
	// otherwise unvalidated ChargeRequest: a memo that is not exactly 32
	// bytes of hex.
	request := tempo.ChargeRequest{
		Amount:    "500000",
		Currency:  testCurrency,
		Recipient: testRecipient,
		MethodDetails: tempo.MethodDetails{
			Memo: "not-a-valid-memo",
		},
	}
	challenge := mpp.NewChallenge(
		"secret-key",
		testRealm,
		tempo.MethodName,
		tempo.IntentCharge,
		request.Map(),
		mpp.WithExpires(mpp.Expires.Minutes(5)),
	)

	_, err = method.CreateCredential(context.Background(), challenge)
	if !assert.Falsef(t, err == nil || !strings.Contains(err.Error(), "memo"),
		"CreateCredential() error = %v, want an error mentioning the malformed memo", err) {
		return
	}
	if !assert.Lenf(t, rpc.sentRawTxs, 0,
		"expected malformed memo to be rejected before any transaction was built, got %d", len(rpc.sentRawTxs)) {
		return
	}
}
