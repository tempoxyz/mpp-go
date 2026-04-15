package tempo

import (
	"fmt"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/math"
	"github.com/ethereum/go-ethereum/signer/core/apitypes"
)

// ProofSource formats the canonical did:pkh source used by Tempo proof credentials.
func ProofSource(chainID int64, address common.Address) string {
	return fmt.Sprintf("did:pkh:eip155:%d:%s", chainID, address.Hex())
}

// ProofTypedDataHash returns the EIP-712 digest for a Tempo proof credential.
func ProofTypedDataHash(chainID int64, challengeID string) (common.Hash, error) {
	typedData := apitypes.TypedData{
		Types: apitypes.Types{
			"EIP712Domain": {
				{Name: "name", Type: "string"},
				{Name: "version", Type: "string"},
				{Name: "chainId", Type: "uint256"},
			},
			"Proof": {
				{Name: "challengeId", Type: "string"},
			},
		},
		PrimaryType: "Proof",
		Domain: apitypes.TypedDataDomain{
			Name:    "MPP",
			Version: "1",
			ChainId: math.NewHexOrDecimal256(chainID),
		},
		Message: apitypes.TypedDataMessage{
			"challengeId": challengeID,
		},
	}
	hash, _, err := apitypes.TypedDataAndHash(typedData)
	if err != nil {
		return common.Hash{}, err
	}
	return common.BytesToHash(hash), nil
}
