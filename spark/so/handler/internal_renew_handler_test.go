package handler

import (
	"testing"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// TestCheckRefundTimelockMonotonicity exercises the stale-replay guard for
// FinalizeRenewRefundTimelock. Within a renew epoch the leaf's RawTx
// timelock must strictly decrease per finalize (NextSequence enforces
// this on the producer); the guard rejects payloads whose timelock is
// higher than or equal to the current leaf's.
func TestCheckRefundTimelockMonotonicity(t *testing.T) {
	leafID := uuid.New()

	// Use spark sequence-flag bits to match production tx construction.
	const seqFlag = 1 << 30 // BIP68 type flag, same shape as spark.InitialSequence().

	tests := []struct {
		name             string
		currentTimelock  uint32
		incomingTimelock uint32
		wantCode         codes.Code // OK == nil
	}{
		{
			name:             "incoming strictly lower — legitimate refund renewal",
			currentTimelock:  spark.InitialTimeLock,
			incomingTimelock: spark.InitialTimeLock - spark.TimeLockInterval,
			wantCode:         codes.OK,
		},
		{
			name:             "incoming equal to current — stale (byte-equality short-circuit handles true redelivery elsewhere)",
			currentTimelock:  spark.InitialTimeLock - spark.TimeLockInterval,
			incomingTimelock: spark.InitialTimeLock - spark.TimeLockInterval,
			wantCode:         codes.AlreadyExists,
		},
		{
			name:             "incoming strictly higher — stale replay from before a newer refund landed",
			currentTimelock:  spark.InitialTimeLock - 2*spark.TimeLockInterval,
			incomingTimelock: spark.InitialTimeLock - spark.TimeLockInterval,
			wantCode:         codes.AlreadyExists,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			currentTx := createValidTestTransactionBytesWithSequence(t, tt.currentTimelock|seqFlag)
			incomingTx := createValidTestTransactionBytesWithSequence(t, tt.incomingTimelock|seqFlag)

			err := checkRefundTimelockMonotonicity(currentTx, incomingTx, leafID)

			if tt.wantCode == codes.OK {
				if err != nil {
					t.Fatalf("expected nil, got %v", err)
				}
				return
			}
			if got := status.Code(err); got != tt.wantCode {
				t.Fatalf("expected gRPC code %v, got %v (err=%v)", tt.wantCode, got, err)
			}
		})
	}
}

// TestCheckNodeRenewPrecondition exercises the stale-replay guard for
// FinalizeRenewNodeTimelock. validateAndConstructNodeTimelock only
// produces a renew-node payload when the existing leaf's RawTx timelock
// is at or below the renew threshold (300). The guard rejects finalizes
// against leaves whose current timelock is above that — those are stale
// payloads from before a newer renew-node added a chain layer that reset
// the timelock high.
func TestCheckNodeRenewPrecondition(t *testing.T) {
	leafID := uuid.New()
	const seqFlag = 1 << 30

	tests := []struct {
		name            string
		currentTimelock uint32
		wantCode        codes.Code
	}{
		{
			name:            "current at zero — eligible for renew-node-zero",
			currentTimelock: 0,
			wantCode:        codes.OK,
		},
		{
			name:            "current well below threshold — eligible for renew-node",
			currentTimelock: 100,
			wantCode:        codes.OK,
		},
		{
			name:            "current exactly at threshold — eligible (matches validateAndConstructNodeTimelock's <=)",
			currentTimelock: spark.RenewTimelockThreshold,
			wantCode:        codes.OK,
		},
		{
			name:            "current just above threshold — stale payload (a newer renew-node already happened)",
			currentTimelock: spark.RenewTimelockThreshold + 1,
			wantCode:        codes.AlreadyExists,
		},
		{
			name:            "current near InitialTimeLock — clearly stale",
			currentTimelock: spark.InitialTimeLock - spark.TimeLockInterval,
			wantCode:        codes.AlreadyExists,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			currentTx := createValidTestTransactionBytesWithSequence(t, tt.currentTimelock|seqFlag)

			err := checkNodeRenewPrecondition(currentTx, leafID)

			if tt.wantCode == codes.OK {
				if err != nil {
					t.Fatalf("expected nil, got %v", err)
				}
				return
			}
			if got := status.Code(err); got != tt.wantCode {
				t.Fatalf("expected gRPC code %v, got %v (err=%v)", tt.wantCode, got, err)
			}
		})
	}
}
