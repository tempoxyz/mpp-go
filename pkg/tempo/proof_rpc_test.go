package tempo

import (
	"math/big"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/assert"
	tempotx "github.com/tempoxyz/tempo-go/pkg/transaction"
)

func TestDefaultRPCURLForChain(t *testing.T) {
	t.Parallel()

	if got := DefaultRPCURLForChain(tempotx.ChainIdModerato); got != tempotx.RpcUrlModerato {
		assert.Failf(t, "", "DefaultRPCURLForChain(moderato) = %q, want %q", got, tempotx.RpcUrlModerato)
		return
	}
	if got := DefaultRPCURLForChain(tempotx.ChainIdMainnet); got != tempotx.RpcUrlMainnet {
		assert.Failf(t, "", "DefaultRPCURLForChain(mainnet) = %q, want %q", got, tempotx.RpcUrlMainnet)
		return
	}
	if got := DefaultRPCURLForChain(999999); got != tempotx.RpcUrlMainnet {
		assert.Failf(t, "", "DefaultRPCURLForChain(unknown) = %q, want %q", got, tempotx.RpcUrlMainnet)
		return
	}
}

func TestProofHelpers(t *testing.T) {
	t.Parallel()

	address := common.HexToAddress("0x70997970c51812dc3a010c7d01b50e0d17dc79c8")
	if got := ProofSource(42431, address); got != "did:pkh:eip155:42431:"+address.Hex() {
		assert.Failf(t, "", "ProofSource() = %q, want %q", got, "did:pkh:eip155:42431:"+address.Hex())
		return
	}

	hash1, err := ProofTypedDataHash(42431, address, "challenge-1", "api.example.com")
	if !assert.NoErrorf(t, err,
		"ProofTypedDataHash() error = %v", err) {
		return
	}

	hash2, err := ProofTypedDataHash(42431, address, "challenge-1", "api.example.com")
	if !assert.NoErrorf(t, err,
		"ProofTypedDataHash() repeat error = %v", err) {
		return
	}

	hash3, err := ProofTypedDataHash(42431, address, "challenge-2", "api.example.com")
	if !assert.NoErrorf(t, err,
		"ProofTypedDataHash() different challenge error = %v", err) {
		return
	}

	hash4, err := ProofTypedDataHash(42431, address, "challenge-1", "other.example.com")
	if !assert.NoErrorf(t, err,
		"ProofTypedDataHash() different realm error = %v", err) {
		return
	}

	otherAccount := common.HexToAddress("0x000000000000000000000000000000000000dEaD")
	hash5, err := ProofTypedDataHash(42431, otherAccount, "challenge-1", "api.example.com")
	if !assert.NoErrorf(t, err,
		"ProofTypedDataHash() different account error = %v", err) {
		return
	}
	if !assert.Equalf(t, hash2, hash1,
		"hash1 = %s, want same hash as hash2 %s", hash1.Hex(), hash2.Hex()) {
		return
	}

	if !assert.NotEqualf(t, hash3, hash1,
		"hash1 = %s, want different hash from hash3 %s", hash1.Hex(), hash3.Hex()) {
		return
	}
	if !assert.NotEqualf(t, hash4, hash1,
		"hash1 = %s, want different hash from hash4 %s", hash1.Hex(), hash4.Hex()) {
		return
	}
	// Wallet binding: a different payer account yields a different digest.
	if !assert.NotEqualf(t, hash5, hash1,
		"hash1 = %s, want different hash from hash5 %s", hash1.Hex(), hash5.Hex()) {
		return
	}
}

func TestParseHexHelpersAndClientConstruction(t *testing.T) {
	t.Parallel()

	parsedUint, err := ParseHexUint64("0x2a")
	if !assert.NoErrorf(t, err,
		"ParseHexUint64() error = %v", err) {
		return
	}
	if !assert.EqualValuesf(t, 42, parsedUint,
		"ParseHexUint64() = %d, want %d", parsedUint, 42) {
		return
	}

	parsedBig, err := ParseHexBigInt("0x2a")
	if !assert.NoErrorf(t, err,
		"ParseHexBigInt() error = %v", err) {
		return
	}
	if !assert.EqualValuesf(t, 0, parsedBig.Cmp(big.NewInt(42)),
		"ParseHexBigInt() = %s, want %d", parsedBig.String(), 42) {
		return
	}

	zero, err := ParseHexBigInt("0x")
	if !assert.NoErrorf(t, err,
		"ParseHexBigInt(0x) error = %v", err) {
		return
	}
	if !assert.EqualValuesf(t, 0, zero.Sign(),
		"ParseHexBigInt(0x) = %s, want 0", zero.String()) {
		return
	}

	if _, err := ParseHexBigInt("0xzz"); err == nil {
		assert.Fail(t, "ParseHexBigInt(0xzz) error = nil, want error")
		return
	}

	if client := NewRPCClient("https://rpc.example.com"); client == nil {
		assert.Fail(t, "NewRPCClient() = nil, want client")
		return
	}
}

func TestTransferABIHelpers(t *testing.T) {
	t.Parallel()

	recipient := common.HexToAddress("0x70997970c51812dc3a010c7d01b50e0d17dc79c8")
	amount := big.NewInt(42)
	calldata := EncodeTransfer(recipient.Hex(), amount)
	if !assert.Lenf(t, calldata, 2+8+64+64,
		"len(calldata) = %d, want %d", len(calldata), 2+8+64+64) {
		return
	}

	if got := ParseTopicAddress("0x" + strings.Repeat("0", 24) + recipient.Hex()[2:]); got != recipient.Hex() {
		assert.Failf(t, "", "ParseTopicAddress() = %q, want %q", got, recipient.Hex())
		return
	}
	if got := ParseTopicAddress("0x1234"); got != "" {
		assert.Failf(t, "", "ParseTopicAddress(short) = %q, want empty string", got)
		return
	}
}
