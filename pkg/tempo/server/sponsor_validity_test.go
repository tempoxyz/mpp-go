package chargeserver

import (
	"github.com/stretchr/testify/require"
	"testing"
	"time"
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
