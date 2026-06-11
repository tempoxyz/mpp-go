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

// ProofTypedData builds the EIP-712 typed data for a Tempo zero-amount proof
// credential. The Proof struct includes the payer wallet (account) so the
// signature commits to a specific payer and cannot be replayed against a
// different account.
func ProofTypedData(chainID int64, account common.Address, challengeID, realm string) apitypes.TypedData {
	return apitypes.TypedData{
		Types: apitypes.Types{
			"EIP712Domain": {
				{Name: "name", Type: "string"},
				{Name: "version", Type: "string"},
				{Name: "chainId", Type: "uint256"},
			},
			"Proof": {
				{Name: "account", Type: "address"},
				{Name: "challengeId", Type: "string"},
				{Name: "realm", Type: "string"},
			},
		},
		PrimaryType: "Proof",
		Domain: apitypes.TypedDataDomain{
			Name:    "MPP",
			Version: "3",
			ChainId: math.NewHexOrDecimal256(chainID),
		},
		Message: apitypes.TypedDataMessage{
			"account":     account.Hex(),
			"challengeId": challengeID,
			"realm":       realm,
		},
	}
}

// ProofTypedDataHash returns the EIP-712 digest for a Tempo proof credential,
// bound to the payer wallet (account). See [ProofTypedData].
func ProofTypedDataHash(chainID int64, account common.Address, challengeID, realm string) (common.Hash, error) {
	hash, _, err := apitypes.TypedDataAndHash(ProofTypedData(chainID, account, challengeID, realm))
	if err != nil {
		return common.Hash{}, err
	}
	return common.BytesToHash(hash), nil
}
