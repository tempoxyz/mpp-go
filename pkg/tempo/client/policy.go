package chargeclient

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"slices"
	"strings"

	"github.com/tempoxyz/mpp-go/pkg/tempo"
)

// PaymentPolicy authorizes a nonzero Tempo charge before the client signs or
// broadcasts it; returning an error rejects the payment. The request has the
// normalized base-unit amount (including splits), the currency, the
// recipient and the chain ID verified with the RPC. It may run concurrently
// and must not mutate the request. Build one with MaxAmount, MaxAtomicAmount
// and AllowChainIDs, combined with AllPolicies, or write your own, for
// example to enforce a cumulative budget atomically.
type PaymentPolicy func(ctx context.Context, request tempo.ChargeRequest) error

// ErrPaymentPolicy is wrapped by the errors the PaymentPolicy helpers in this
// package return when they reject a payment.
var ErrPaymentPolicy = errors.New("tempo client: payment not allowed")

// AllPolicies returns a PaymentPolicy that allows a payment only when every
// policy allows it, checking them in order and returning the first rejection.
// Nil policies are skipped; with no policies it allows every payment.
func AllPolicies(policies ...PaymentPolicy) PaymentPolicy {
	policies = slices.Clone(policies)
	return func(ctx context.Context, request tempo.ChargeRequest) error {
		for _, policy := range policies {
			if policy == nil {
				continue
			}
			if err := policy(ctx, request); err != nil {
				return err
			}
		}
		return nil
	}
}

// MaxAmount returns a PaymentPolicy that rejects a single payment above
// maxAmount,
// a decimal amount in display units of a token with the given decimals, such
// as MaxAmount(tempo.OUSDAddress, "0.50", 6) for at most 0.50 OUSD, like
// mppx's maxAmount. The limit is per payment, not cumulative, and covers the
// whole amount including splits.
//
// With a currency, it also rejects payments in any other currency, compared
// case-insensitively. An empty currency applies the limit to every currency,
// which suits the Tempo stablecoins, which all have 6 decimals. An invalid
// maxAmount or negative decimals rejects every payment.
func MaxAmount(currency, maxAmount string, decimals int) PaymentPolicy {
	limit, err := parseUnits(maxAmount, decimals)
	if err != nil {
		err = fmt.Errorf("%w: invalid MaxAmount %q: %w", ErrPaymentPolicy, maxAmount, err)
		return func(context.Context, tempo.ChargeRequest) error { return err }
	}
	return maxAtomicAmount(currency, limit, "MaxAmount "+maxAmount)
}

// MaxAtomicAmount returns a PaymentPolicy that rejects a single payment above
// maxAmount base units, like mppx's maxAtomicAmount. currency works as in
// MaxAmount. A nil or negative maxAmount rejects every payment.
func MaxAtomicAmount(currency string, maxAmount *big.Int) PaymentPolicy {
	if maxAmount == nil || maxAmount.Sign() < 0 {
		err := fmt.Errorf("%w: invalid MaxAtomicAmount %v", ErrPaymentPolicy, maxAmount)
		return func(context.Context, tempo.ChargeRequest) error { return err }
	}
	limit := new(big.Int).Set(maxAmount)
	return maxAtomicAmount(currency, limit, "MaxAtomicAmount "+limit.String())
}

func maxAtomicAmount(currency string, limit *big.Int, label string) PaymentPolicy {
	return func(_ context.Context, request tempo.ChargeRequest) error {
		if currency != "" && !strings.EqualFold(currency, request.Currency) {
			return fmt.Errorf("%w: currency %s is not %s", ErrPaymentPolicy, request.Currency, currency)
		}
		amount, ok := new(big.Int).SetString(request.Amount, 10)
		if !ok {
			return fmt.Errorf("%w: invalid amount %q", ErrPaymentPolicy, request.Amount)
		}
		if amount.Cmp(limit) > 0 {
			return fmt.Errorf("%w: amount %s exceeds %s (%s base units)", ErrPaymentPolicy, amount, label, limit)
		}
		return nil
	}
}

// AllowChainIDs returns a PaymentPolicy that rejects payments on chains other
// than ids, such as tempo.ChainIDMainnet, like mppx's allowedChainIds. The
// chain is the one the client verified with its RPC.
func AllowChainIDs(ids ...int64) PaymentPolicy {
	ids = slices.Clone(ids)
	return func(_ context.Context, request tempo.ChargeRequest) error {
		chainID := request.MethodDetails.ChainID
		if chainID == nil || !slices.Contains(ids, *chainID) {
			got := "unknown"
			if chainID != nil {
				got = fmt.Sprint(*chainID)
			}
			return fmt.Errorf("%w: chain id %s", ErrPaymentPolicy, got)
		}
		return nil
	}
}

// parseUnits converts a non-negative decimal amount to base units.
func parseUnits(value string, decimals int) (*big.Int, error) {
	if decimals < 0 {
		return nil, fmt.Errorf("decimals must not be negative")
	}
	value = strings.TrimSpace(value)
	intPart, fracPart, _ := strings.Cut(value, ".")
	if intPart == "" && fracPart == "" {
		return nil, fmt.Errorf("amount is required")
	}
	for _, digit := range intPart + fracPart {
		if digit < '0' || digit > '9' {
			return nil, fmt.Errorf("not a non-negative decimal amount")
		}
	}
	if trimmed := strings.TrimRight(fracPart, "0"); len(trimmed) > decimals {
		return nil, fmt.Errorf("more than %d decimals", decimals)
	}
	if len(fracPart) > decimals {
		fracPart = fracPart[:decimals]
	}
	fracPart += strings.Repeat("0", decimals-len(fracPart))
	units, ok := new(big.Int).SetString("0"+intPart+fracPart, 10)
	if !ok {
		return nil, fmt.Errorf("not a decimal amount")
	}
	return units, nil
}
