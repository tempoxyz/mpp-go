package chargeserver

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestSponsoredValidityDeadline(t *testing.T) {
	now := time.Unix(2_000_000_000, 0)
	for _, tc := range []struct {
		name    string
		seconds int64
		ok      bool
	}{
		{"expired", -1, false}, {"now", 0, false}, {"too short", 14, false},
		{"minimum", 15, true}, {"client default", 25, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateFeePayerDeadline(uint64(now.Unix()+tc.seconds), now)
			if tc.ok {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
	require.Error(t, validateFeePayerDeadline(0, now))
}

func TestSponsoredValidityDeadlineRoundsUpFractionalSeconds(t *testing.T) {
	for _, nanos := range []int64{1, 100_000_000, 900_000_000, 999_999_999} {
		now := time.Unix(2_000_000_000, nanos)
		require.Error(t, validateFeePayerDeadline(uint64(now.Unix()+15), now))
		require.NoError(t, validateFeePayerDeadline(uint64(now.Unix()+16), now))
	}
}
