package handler

import (
	"testing"

	pbgossip "github.com/lightsparkdev/spark/proto/gossip"
	pbinternal "github.com/lightsparkdev/spark/proto/spark_internal"
	"github.com/lightsparkdev/spark/so/db"
	sparktesting "github.com/lightsparkdev/spark/testing"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/anypb"
)

// TestDispatchPrepare_CoordinatorIndexGuards pins the coordinator-identity
// fencing on the ConsensusPrepare boundary: an index that doesn't resolve to a
// known operator fails closed (no silent fallback to recording the participant
// itself as coordinator), and an index naming the receiving SO is rejected
// outright — a coordinator prepares locally and never calls ConsensusPrepare on
// itself, so such a request is always forged or misrouted. Both guards run
// before the flow handler's Prepare.
func TestDispatchPrepare_CoordinatorIndexGuards(t *testing.T) {
	ctx, _ := db.ConnectToTestPostgres(t)
	cfg := sparktesting.TestConfig(t)
	h := NewConsensusHandler(cfg)

	op, err := anypb.New(&pbinternal.StaticDepositUtxoRefundPrepareRequest{})
	require.NoError(t, err)

	t.Run("unknown coordinator index fails closed", func(t *testing.T) {
		_, err := h.DispatchPrepare(ctx,
			pbgossip.ConsensusOperationType_CONSENSUS_OPERATION_TYPE_STATIC_DEPOSIT_UTXO_REFUND,
			op, "", 99)
		require.ErrorContains(t, err, "unknown coordinator_index 99")
	})

	t.Run("self coordinator index is rejected", func(t *testing.T) {
		_, err := h.DispatchPrepare(ctx,
			pbgossip.ConsensusOperationType_CONSENSUS_OPERATION_TYPE_STATIC_DEPOSIT_UTXO_REFUND,
			op, "", uint(cfg.Index))
		require.ErrorContains(t, err, "declares the receiving SO")
	})

	t.Run("valid remote coordinator index reaches the flow handler", func(t *testing.T) {
		remoteIndex := uint(cfg.Index) + 1
		_, err := h.DispatchPrepare(ctx,
			pbgossip.ConsensusOperationType_CONSENSUS_OPERATION_TYPE_STATIC_DEPOSIT_UTXO_REFUND,
			op, "", remoteIndex)
		// The guards pass and the refund flow handler's own validation fires,
		// proving Prepare was actually dispatched.
		require.ErrorContains(t, err, "request is required")
	})
}

// TestDispatchPrepare_BoundEmptyFlowIDRejected pins the prepare-side counterpart
// to validateDecisionAgainstPreparedOp's empty-id fail-closed: a bound flow
// (SEND_TRANSFER implements PrepareBoundFlowHandler) with an empty
// flow_execution_id must be rejected with an ERROR — not a (nil, nil) result.
// The error-vs-nil distinction is load-bearing: a nil result reads as a
// successful prepare and would let the coordinator record a false COMMITTED for
// the other participants, whereas an RPC error drives its rollback path. The
// guard runs after the coordinator-index checks, so this uses a valid remote
// index to reach it.
func TestDispatchPrepare_BoundEmptyFlowIDRejected(t *testing.T) {
	ctx, _ := db.ConnectToTestPostgres(t)
	cfg := sparktesting.TestConfig(t)
	h := NewConsensusHandler(cfg)

	op, err := anypb.New(&pbinternal.SendTransferPrepareRequest{})
	require.NoError(t, err)

	_, err = h.DispatchPrepare(ctx,
		pbgossip.ConsensusOperationType_CONSENSUS_OPERATION_TYPE_SEND_TRANSFER,
		op, "", uint(cfg.Index)+1)
	require.ErrorContains(t, err, "requires a flow_execution_id")
}
