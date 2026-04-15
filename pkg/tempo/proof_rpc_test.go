package tempo

import (
	"math/big"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	tempotx "github.com/tempoxyz/tempo-go/pkg/transaction"
)

func TestDefaultRPCURLForChain(t *testing.T) {
	t.Parallel()

	if got := DefaultRPCURLForChain(tempotx.ChainIdModerato); got != tempotx.RpcUrlModerato {
		t.Fatalf("DefaultRPCURLForChain(moderato) = %q, want %q", got, tempotx.RpcUrlModerato)
	}
	if got := DefaultRPCURLForChain(tempotx.ChainIdMainnet); got != tempotx.RpcUrlMainnet {
		t.Fatalf("DefaultRPCURLForChain(mainnet) = %q, want %q", got, tempotx.RpcUrlMainnet)
	}
	if got := DefaultRPCURLForChain(999999); got != tempotx.RpcUrlMainnet {
		t.Fatalf("DefaultRPCURLForChain(unknown) = %q, want %q", got, tempotx.RpcUrlMainnet)
	}
}

func TestProofHelpers(t *testing.T) {
	t.Parallel()

	address := common.HexToAddress("0x70997970c51812dc3a010c7d01b50e0d17dc79c8")
	if got := ProofSource(42431, address); got != "did:pkh:eip155:42431:"+address.Hex() {
		t.Fatalf("ProofSource() = %q, want %q", got, "did:pkh:eip155:42431:"+address.Hex())
	}

	hash1, err := ProofTypedDataHash(42431, "challenge-1")
	if err != nil {
		t.Fatalf("ProofTypedDataHash() error = %v", err)
	}
	hash2, err := ProofTypedDataHash(42431, "challenge-1")
	if err != nil {
		t.Fatalf("ProofTypedDataHash() repeat error = %v", err)
	}
	hash3, err := ProofTypedDataHash(42431, "challenge-2")
	if err != nil {
		t.Fatalf("ProofTypedDataHash() different challenge error = %v", err)
	}
	if hash1 != hash2 {
		t.Fatalf("hash1 = %s, want same hash as hash2 %s", hash1.Hex(), hash2.Hex())
	}
	if hash1 == hash3 {
		t.Fatalf("hash1 = %s, want different hash from hash3 %s", hash1.Hex(), hash3.Hex())
	}
}

func TestParseHexHelpersAndClientConstruction(t *testing.T) {
	t.Parallel()

	parsedUint, err := ParseHexUint64("0x2a")
	if err != nil {
		t.Fatalf("ParseHexUint64() error = %v", err)
	}
	if parsedUint != 42 {
		t.Fatalf("ParseHexUint64() = %d, want %d", parsedUint, 42)
	}

	parsedBig, err := ParseHexBigInt("0x2a")
	if err != nil {
		t.Fatalf("ParseHexBigInt() error = %v", err)
	}
	if parsedBig.Cmp(big.NewInt(42)) != 0 {
		t.Fatalf("ParseHexBigInt() = %s, want %d", parsedBig.String(), 42)
	}

	zero, err := ParseHexBigInt("0x")
	if err != nil {
		t.Fatalf("ParseHexBigInt(0x) error = %v", err)
	}
	if zero.Sign() != 0 {
		t.Fatalf("ParseHexBigInt(0x) = %s, want 0", zero.String())
	}

	if _, err := ParseHexBigInt("0xzz"); err == nil {
		t.Fatal("ParseHexBigInt(0xzz) error = nil, want error")
	}

	if client := NewRPCClient("https://rpc.example.com"); client == nil {
		t.Fatal("NewRPCClient() = nil, want client")
	}
}

func TestTransferABIHelpers(t *testing.T) {
	t.Parallel()

	recipient := common.HexToAddress("0x70997970c51812dc3a010c7d01b50e0d17dc79c8")
	amount := big.NewInt(42)
	calldata := EncodeTransfer(recipient.Hex(), amount)
	if len(calldata) != 2+8+64+64 {
		t.Fatalf("len(calldata) = %d, want %d", len(calldata), 2+8+64+64)
	}
	if got := ParseTopicAddress("0x" + strings.Repeat("0", 24) + recipient.Hex()[2:]); got != recipient.Hex() {
		t.Fatalf("ParseTopicAddress() = %q, want %q", got, recipient.Hex())
	}
	if got := ParseTopicAddress("0x1234"); got != "" {
		t.Fatalf("ParseTopicAddress(short) = %q, want empty string", got)
	}
}
