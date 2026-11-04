package tempo

import (
	"errors"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/math"
	"github.com/ethereum/go-ethereum/crypto"
)

// ProofSource formats the canonical did:pkh source used by Tempo proof credentials.
func ProofSource(chainID int64, address common.Address) string {
	return fmt.Sprintf("did:pkh:eip155:%d:%s", chainID, address.Hex())
}

// TypedDataField is one member of an EIP-712 struct type. Its JSON form
// matches the "types" entries of eth_signTypedData_v4.
type TypedDataField struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

// TypedDataDomain is the EIP-712 domain of a Tempo proof credential.
type TypedDataDomain struct {
	Name    string                `json:"name"`
	Version string                `json:"version"`
	ChainId *math.HexOrDecimal256 `json:"chainId"`
}

// TypedData is the EIP-712 typed data of a Tempo proof credential, in the
// shape of eth_signTypedData_v4 (and of go-ethereum's apitypes.TypedData).
type TypedData struct {
	Types       map[string][]TypedDataField `json:"types"`
	PrimaryType string                      `json:"primaryType"`
	Domain      TypedDataDomain             `json:"domain"`
	Message     map[string]any              `json:"message"`
}

const (
	proofPrimaryType   = "Proof"
	proofDomainName    = "MPP"
	proofDomainVersion = "3"

	// EIP-712 encodeType strings for the proof's two fixed struct types.
	proofDomainTypeString = "EIP712Domain(string name,string version,uint256 chainId)"
	proofTypeString       = "Proof(address account,string challengeId,string realm)"
)

var (
	proofDomainTypeHash = crypto.Keccak256([]byte(proofDomainTypeString))
	proofTypeHash       = crypto.Keccak256([]byte(proofTypeString))

	errNegativeChainID = errors.New("invalid negative value for unsigned type uint256")
)

// ProofTypedData builds the EIP-712 typed data for a Tempo zero-amount proof
// credential. The Proof struct includes the payer wallet (account) so the
// signature commits to a specific payer and cannot be replayed against a
// different account.
func ProofTypedData(chainID int64, account common.Address, challengeID, realm string) TypedData {
	return TypedData{
		Types: map[string][]TypedDataField{
			"EIP712Domain": {
				{Name: "name", Type: "string"},
				{Name: "version", Type: "string"},
				{Name: "chainId", Type: "uint256"},
			},
			proofPrimaryType: {
				{Name: "account", Type: "address"},
				{Name: "challengeId", Type: "string"},
				{Name: "realm", Type: "string"},
			},
		},
		PrimaryType: proofPrimaryType,
		Domain: TypedDataDomain{
			Name:    proofDomainName,
			Version: proofDomainVersion,
			ChainId: math.NewHexOrDecimal256(chainID),
		},
		Message: map[string]any{
			"account":     account.Hex(),
			"challengeId": challengeID,
			"realm":       realm,
		},
	}
}

// ProofTypedDataHash returns the EIP-712 digest for a Tempo proof credential,
// bound to the payer wallet (account). See [ProofTypedData]. It returns an
// error only for a negative chainID, which uint256 cannot encode.
//
// The digest is computed directly for the proof's two fixed struct types
// (EIP-712 hashStruct of the domain and of Proof, then
// keccak256(0x19 0x01 || domainSeparator || hashStruct(message))), so
// pkg/tempo does not link go-ethereum's signer/core/apitypes and with it
// go-eth-kzg, gnark-crypto and c-kzg. This is a reimplementation of the
// EIP-712 specification (https://eips.ethereum.org/EIPS/eip-712) for these
// fixed types, not copied code. Its reference implementation, whose output it
// reproduces byte for byte, is go-ethereum v1.17.5
// (github.com/ethereum/go-ethereum, commit
// 9621c6ad10934a01b5514886fb6fbd87640b6c05), signer/core/apitypes/types.go:
// TypedDataAndHash, HashStruct, EncodeData and EncodePrimitiveValue
// (go-ethereum library code is licensed LGPL-3.0-or-later).
// proof_vectors_test.go pins digests that implementation produced.
func ProofTypedDataHash(chainID int64, account common.Address, challengeID, realm string) (common.Hash, error) {
	if chainID < 0 {
		return common.Hash{}, errNegativeChainID
	}
	domainSeparator := crypto.Keccak256(
		proofDomainTypeHash,
		crypto.Keccak256([]byte(proofDomainName)),
		crypto.Keccak256([]byte(proofDomainVersion)),
		math.U256Bytes(big.NewInt(chainID)),
	)
	structHash := crypto.Keccak256(
		proofTypeHash,
		common.LeftPadBytes(account.Bytes(), 32),
		crypto.Keccak256([]byte(challengeID)),
		crypto.Keccak256([]byte(realm)),
	)
	return crypto.Keccak256Hash([]byte{0x19, 0x01}, domainSeparator, structHash), nil
}
