// Package txsend classifies eth_sendRawTransaction errors for the Tempo
// client and server.
package txsend

import (
	"errors"
	"strings"

	temporpc "github.com/tempoxyz/tempo-go/pkg/client"
)

// duplicateMarkers are the error messages with which nodes reject a
// transaction they already hold.
var duplicateMarkers = []string{"already known", "known transaction", "already imported", "alreadyknown", "expiringnoncereplay"}

// IsDuplicateSubmission reports whether eth_sendRawTransaction failed because
// the node already holds the transaction, as geth- and reth-based nodes answer
// a re-broadcast of a pending transaction, and as Tempo answers with
// ExpiringNonceReplay when an expiring-nonce transaction with this hash was
// already accepted, such as when a transport retries a send the node took.
func IsDuplicateSubmission(err error) bool {
	var rpcErr *temporpc.JSONRPCError
	if !errors.As(err, &rpcErr) {
		return false
	}
	message := strings.ToLower(rpcErr.Message)
	for _, marker := range duplicateMarkers {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}
