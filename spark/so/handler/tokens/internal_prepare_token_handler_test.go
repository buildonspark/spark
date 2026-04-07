package tokens

import (
	"testing"
	"time"

	"github.com/lightsparkdev/spark"
	tokenpb "github.com/lightsparkdev/spark/proto/spark_token"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestValidateClientCreatedTimestamp(t *testing.T) {
	validitySecs := uint64(30)

	newTx := func(ts *timestamppb.Timestamp) *tokenpb.TokenTransaction {
		return &tokenpb.TokenTransaction{
			Version:                 3,
			ClientCreatedTimestamp:  ts,
			ValidityDurationSeconds: &validitySecs,
		}
	}

	type testCase struct {
		name       string
		offset     time.Duration
		useNilTS   bool
		shouldFail bool
	}
	// Avoid exact boundaries to reduce flakiness since validateClientCreatedTimestamp uses time.Now() internally.
	// The oldest allowed timestamp is validitySecs + MaxTimestampSkew (1 minute) in the past.
	// The latest allowed timestamp is MaxTimestampSkew (1 minute) in the future.
	cases := []testCase{
		{name: "nil_timestamp_fails", useNilTS: true, shouldFail: true},
		{name: "now_ok", offset: 0, shouldFail: false},
		{name: "slightly_within_past_ok", offset: -(time.Duration(validitySecs)*time.Second + MaxTimestampSkew - 5*time.Second), shouldFail: false},
		{name: "too_old_fail", offset: -(time.Duration(validitySecs)*time.Second + MaxTimestampSkew + 5*time.Second), shouldFail: true},
		{name: "slightly_future_ok", offset: MaxTimestampSkew - 5*time.Second, shouldFail: false},
		{name: "too_future_fail", offset: MaxTimestampSkew + 5*time.Second, shouldFail: true},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var ts *timestamppb.Timestamp
			if !c.useNilTS {
				ts = timestamppb.New(time.Now().Add(c.offset))
			}
			tx := newTx(ts)
			err := validateClientCreatedTimestamp(tx)
			if c.shouldFail {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestValidateClientCreatedTimestamp_WithExecuteBefore(t *testing.T) {
	validitySecs := uint64(30)

	// ValidateExecuteBefore requires microsecond precision (PostgreSQL constraint).
	truncUS := func(d time.Duration) time.Time {
		return time.Now().Add(d).UTC().Truncate(time.Microsecond)
	}

	newTx := func(cctOffset time.Duration) *tokenpb.TokenTransaction {
		return &tokenpb.TokenTransaction{
			Version:                 3,
			ClientCreatedTimestamp:  timestamppb.New(truncUS(cctOffset)),
			ValidityDurationSeconds: &validitySecs,
		}
	}

	t.Run("CCT_in_past_with_valid_execute_before_passes", func(t *testing.T) {
		tx := newTx(-1 * time.Hour)
		tx.ExecuteBefore = timestamppb.New(truncUS(1 * time.Hour))
		require.NoError(t, validateClientCreatedTimestamp(tx))
	})

	t.Run("CCT_after_execute_before_fails", func(t *testing.T) {
		tx := newTx(0)
		tx.ExecuteBefore = timestamppb.New(truncUS(-1 * time.Hour))
		require.Error(t, validateClientCreatedTimestamp(tx))
	})

	t.Run("CCT_too_far_before_execute_before_fails", func(t *testing.T) {
		tx := newTx(-(spark.TokenMaxExecuteBeforeWindow + 1*time.Hour))
		tx.ExecuteBefore = timestamppb.New(truncUS(1 * time.Hour))
		require.Error(t, validateClientCreatedTimestamp(tx))
	})

	t.Run("execute_before_already_expired_fails", func(t *testing.T) {
		tx := newTx(-5 * time.Minute)
		tx.ExecuteBefore = timestamppb.New(truncUS(-30 * time.Second))
		require.Error(t, validateClientCreatedTimestamp(tx))
	})

	t.Run("execute_before_too_far_in_future_fails", func(t *testing.T) {
		tx := newTx(0)
		tx.ExecuteBefore = timestamppb.New(truncUS(spark.TokenMaxExecuteBeforeWindow + 5*time.Second))
		require.Error(t, validateClientCreatedTimestamp(tx))
	})

	t.Run("execute_before_within_max_window_passes", func(t *testing.T) {
		tx := newTx(0)
		tx.ExecuteBefore = timestamppb.New(truncUS(spark.TokenMaxExecuteBeforeWindow - 1*time.Hour))
		require.NoError(t, validateClientCreatedTimestamp(tx))
	})
}
