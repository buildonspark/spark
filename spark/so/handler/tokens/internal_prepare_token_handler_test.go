package tokens

import (
	"testing"
	"time"

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
	cases := []testCase{
		{name: "nil_timestamp_fails", useNilTS: true, shouldFail: true},
		{name: "now_ok", offset: 0, shouldFail: false},
		{name: "slightly_within_past_ok", offset: -(time.Duration(validitySecs)*time.Second - 5*time.Second), shouldFail: false},
		{name: "too_old_fail", offset: -(time.Duration(validitySecs)*time.Second + 5*time.Second), shouldFail: true},
		{name: "slightly_future_ok", offset: 55 * time.Second, shouldFail: false},
		{name: "too_future_fail", offset: 65 * time.Second, shouldFail: true},
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
