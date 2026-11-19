package paymentctx

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOrigin(t *testing.T) {
	ctx := context.Background()
	assert.Empty(t, Origin(ctx))
	assert.Equal(t, ctx, WithOrigin(ctx, ""))
	assert.Equal(t, "https://api.example.com", Origin(WithOrigin(ctx, "https://api.example.com")))
}

func TestRetryPendingRetriesUpToTheLimit(t *testing.T) {
	calls := 0
	_, err := RetryPending(context.Background(), 3, func() (int, error) {
		calls++
		return 0, fmt.Errorf("settling: %w", ErrSettlementPending)
	})
	require.ErrorIs(t, err, ErrSettlementPending)
	assert.Equal(t, 3, calls)

	calls = 0
	value, err := RetryPending(context.Background(), 3, func() (int, error) {
		calls++
		if calls < 2 {
			return 0, ErrSettlementPending
		}
		return 7, nil
	})
	require.NoError(t, err)
	assert.Equal(t, 7, value)
	assert.Equal(t, 2, calls)
}

func TestRetryPendingReturnsOtherErrorsAtOnce(t *testing.T) {
	calls := 0
	refused := errors.New("refused")
	_, err := RetryPending(context.Background(), 3, func() (int, error) {
		calls++
		return 0, refused
	})
	require.ErrorIs(t, err, refused)
	assert.Equal(t, 1, calls)
}

func TestRetryPendingStopsWhenContextIsDone(t *testing.T) {
	reason := errors.New("canceled while settling")
	pendingErr := fmt.Errorf("zone withdrawal: %w", ErrSettlementPending)
	ctx, cancel := context.WithCancelCause(context.Background())
	calls := 0
	_, err := RetryPending(ctx, 3, func() (int, error) {
		calls++
		cancel(reason)
		return 0, pendingErr
	})
	require.ErrorIs(t, err, reason)
	require.ErrorIs(t, err, pendingErr)
	require.ErrorIs(t, err, ErrSettlementPending)
	assert.Equal(t, 1, calls)

	calls = 0
	_, err = RetryPending(context.Background(), 0, func() (int, error) {
		calls++
		return 0, ErrSettlementPending
	})
	require.ErrorIs(t, err, ErrSettlementPending)
	assert.Equal(t, 1, calls)
}

func TestRetryPendingKeepsCauseOnLastPendingAttempt(t *testing.T) {
	reason := errors.New("canceled on the last attempt")
	ctx, cancel := context.WithCancelCause(context.Background())
	calls := 0
	_, err := RetryPending(ctx, 2, func() (int, error) {
		calls++
		if calls == 2 {
			cancel(reason)
		}
		return 0, ErrSettlementPending
	})
	require.ErrorIs(t, err, reason)
	require.ErrorIs(t, err, ErrSettlementPending)
	assert.Equal(t, 2, calls)
}

type pendingError struct{ id string }

func (e *pendingError) Error() string { return "pending " + e.id }
func (e *pendingError) Unwrap() error { return ErrSettlementPending }

func TestRetryPendingPrefersThisCallsPendingErrorOverTheCause(t *testing.T) {
	for _, attempts := range []int{2, 3} {
		ctx, cancel := context.WithCancelCause(context.Background())
		calls := 0
		_, err := RetryPending(ctx, attempts, func() (int, error) {
			calls++
			if calls == 2 {
				cancel(&pendingError{id: "other"})
			}
			return 0, &pendingError{id: "this"}
		})
		var got *pendingError
		require.ErrorAs(t, err, &got)
		assert.Equal(t, "this", got.id)
	}
}

func TestRetryPendingDoesNotCreateOnceContextIsDone(t *testing.T) {
	reason := errors.New("canceled during approval")
	ctx, cancel := context.WithCancelCause(context.Background())
	cancel(reason)
	calls := 0
	_, err := RetryPending(ctx, 3, func() (int, error) {
		calls++
		return 1, nil
	})
	require.ErrorIs(t, err, reason)
	assert.Equal(t, 0, calls)
}
