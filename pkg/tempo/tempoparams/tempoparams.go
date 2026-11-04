// Package tempoparams holds the Tempo charge parameter types that the core
// server package (pkg/server) exposes in server.ChargeParams. It has no
// dependencies outside the standard library, so pkg/server does not pull in
// go-ethereum, go-redis or tempo-go through pkg/tempo.
//
// pkg/tempo re-exports every identifier here (tempo.ChargeMode,
// tempo.SplitParams and the ChargeMode constants); use those names.
package tempoparams

// ChargeMode declares which Tempo credential flow a challenge accepts.
type ChargeMode string

const (
	// ChargeModePull allows clients to return a signed transaction credential.
	ChargeModePull ChargeMode = "pull"
	// ChargeModePush allows clients to broadcast first and return a hash credential.
	ChargeModePush ChargeMode = "push"
)

// SplitParams are the human-friendly inputs used to build a canonical Split.
type SplitParams struct {
	// Amount is the human-readable amount routed to this split recipient.
	Amount string
	// Memo is an optional split-specific Tempo memo.
	Memo string
	// Recipient is the split recipient address.
	Recipient string
}
