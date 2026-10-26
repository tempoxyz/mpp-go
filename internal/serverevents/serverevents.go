// Package serverevents lets this module's transports, such as MCP, report a
// payment event to a server.Mpp's Events hooks without exporting that API
// from package server.
package serverevents

import (
	"context"

	"github.com/tempoxyz/mpp-go/pkg/mpp"
)

// PaymentFailed reports a submitted Credential a transport rejected before
// it reached a handler's Charge, such as one that cannot be parsed
// (credential nil), to the PaymentFailed hook of every distinct *server.Mpp
// in payments, once each. Package server sets it when it is initialized;
// nil entries are skipped.
var PaymentFailed func(ctx context.Context, payments []any, credential *mpp.Credential, err error)
