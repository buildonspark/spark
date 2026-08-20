package tokens

import (
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateTimestampNotFutureDated(t *testing.T) {
	tests := []struct {
		name              string
		timestampMillis   uint64
		expectedRejection bool
	}{
		{
			name:            "inside the skew window",
			timestampMillis: uint64(time.Now().Add(MaxTimestampFutureSkew - time.Minute).UnixMilli()),
		},
		{
			name:              "past the skew window",
			timestampMillis:   uint64(time.Now().Add(MaxTimestampFutureSkew + time.Minute).UnixMilli()),
			expectedRejection: true,
		},
		{
			name:            "old timestamps stay acceptable on the internal path",
			timestampMillis: uint64(time.Now().Add(-48 * time.Hour).UnixMilli()),
		},
		{
			name: "zero",
		},
		{
			// Comparing as int64 would wrap this to a past instant and accept it, leaving a
			// grant no honest revoke could supersede.
			name:              "high-bit value that wraps negative as int64",
			timestampMillis:   math.MaxUint64,
			expectedRejection: true,
		},
		{
			name:              "max int64",
			timestampMillis:   math.MaxInt64,
			expectedRejection: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateTimestampNotFutureDated(tt.timestampMillis)
			if !tt.expectedRejection {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), "too far in the future")
		})
	}
}
