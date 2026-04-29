package task

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	pbgossip "github.com/lightsparkdev/spark/proto/gossip"
	pbinternal "github.com/lightsparkdev/spark/proto/spark_internal"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/ent/flowexecution"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/handler"
	"github.com/lightsparkdev/spark/so/knobs"
	sparktesting "github.com/lightsparkdev/spark/testing"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/anypb"
)

// stuckParticipantRow inserts a PARTICIPANT FlowExecution row and backdates
// its update_time past the reconcile stuck threshold so a reconcile pass
// will pick it up. Uses STORE_PREIMAGE_SHARE op type because that flow's
// Commit and Rollback are no-ops — the test exercises the reconcile /
// gossip-dispatch plumbing without triggering domain-specific commit work
// that would need heavier fixtures.
func stuckParticipantRow(t *testing.T, ctx context.Context, id uuid.UUID, coordIdx uint) {
	t.Helper()
	insertStaleParticipantRow(t, ctx, id, coordIdx, 1*time.Hour)
}

// insertStaleParticipantRow is the variant of stuckParticipantRow that takes
// an explicit age, used by the ordering / batch-cap tests where each row
// needs a distinct update_time.
func insertStaleParticipantRow(t *testing.T, ctx context.Context, id uuid.UUID, coordIdx uint, age time.Duration) {
	t.Helper()
	client, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)
	_, err = client.FlowExecution.Create().
		SetID(id).
		SetRole(st.FlowExecutionRoleParticipant).
		SetOpType(int32(pbgossip.ConsensusOperationType_CONSENSUS_OPERATION_TYPE_STORE_PREIMAGE_SHARE)).
		SetCoordinatorIndex(coordIdx).
		Save(ctx)
	require.NoError(t, err)
	// Ent's update_time has UpdateDefault(time.Now), so we have to update
	// the column directly to backdate it.
	_, err = client.FlowExecution.Update().
		Where(flowexecution.ID(id)).
		SetUpdateTime(time.Now().Add(-age)).
		Save(ctx)
	require.NoError(t, err)
}

// insertCoordinatorRow inserts a COORDINATOR FlowExecution row in the given
// status with a backdated update_time so SweepStaleCoordinatorFlows will
// pick it up. Returns the inserted id.
func insertStaleCoordinatorRow(t *testing.T, ctx context.Context, status st.FlowExecutionStatus, age time.Duration) uuid.UUID {
	t.Helper()
	client, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)
	id := uuid.New()
	_, err = client.FlowExecution.Create().
		SetID(id).
		SetRole(st.FlowExecutionRoleCoordinator).
		SetOpType(int32(pbgossip.ConsensusOperationType_CONSENSUS_OPERATION_TYPE_STORE_PREIMAGE_SHARE)).
		SetStatus(status).
		SetCoordinatorIndex(0).
		Save(ctx)
	require.NoError(t, err)
	_, err = client.FlowExecution.Update().
		Where(flowexecution.ID(id)).
		SetUpdateTime(time.Now().Add(-age)).
		Save(ctx)
	require.NoError(t, err)
	return id
}

// stubQuery builds an outcomeQueryFunc returning the supplied response for
// any call. The test asserts the reconciler behaves correctly regardless of
// which operator the call goes to.
func stubQuery(resp *pbinternal.ConsensusQueryOutcomeResponse) outcomeQueryFunc {
	return func(_ context.Context, _ *so.SigningOperator, _ string) (*pbinternal.ConsensusQueryOutcomeResponse, error) {
		return resp, nil
	}
}

// anyOperation wraps a minimal gossip message as an Any — the preimage
// share flow's Commit/Rollback ignore payload content, so any well-formed
// Any works.
func anyOperation(t *testing.T) *anypb.Any {
	t.Helper()
	a, err := anypb.New(&pbgossip.GossipMessage{MessageId: "reconcile-test"})
	require.NoError(t, err)
	return a
}

func testConfigWithOperator(t *testing.T, coordIdx uint64) *so.Config {
	t.Helper()
	cfg := sparktesting.TestConfig(t)
	// Ensure at least one operator with the index the test uses. The default
	// TestConfig already has operators with IDs 0..N; assert that.
	op, err := cfg.GetOperatorByID(coordIdx)
	require.NoError(t, err, "test setup requires operator with ID %d", coordIdx)
	require.NotNil(t, op)
	return cfg
}

// recoveryEnabledKnobs returns a knobs fixture with the active-recovery
// switch on. Most reconcile/sweep tests exercise the recovery path; the
// production default is monitor-only.
func recoveryEnabledKnobs() knobs.Knobs {
	return knobs.NewFixedKnobs(map[string]float64{
		knobs.KnobFlowExecutionReconcileEnabled: 1,
	})
}

// ---------- ReconcileStuckParticipantFlows ----------

func TestReconcile_CoordinatorCommitted_TransitionsRowToCommitted(t *testing.T) {
	ctx, _ := db.ConnectToTestPostgres(t)
	cfg := testConfigWithOperator(t, 0)
	id := uuid.New()
	stuckParticipantRow(t, ctx, id, 0)

	r := &participantReconciler{
		config: cfg,
		knobs:  recoveryEnabledKnobs(),
		query: stubQuery(&pbinternal.ConsensusQueryOutcomeResponse{
			Outcome:         pbinternal.ConsensusQueryOutcomeResponse_OUTCOME_COMMITTED,
			OpType:          int32(pbgossip.ConsensusOperationType_CONSENSUS_OPERATION_TYPE_STORE_PREIMAGE_SHARE),
			DecisionPayload: anyOperation(t),
		}),
		gossipHandler: handler.NewGossipHandler(cfg),
	}
	require.NoError(t, r.reconcile(ctx))

	client, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)
	row, err := client.FlowExecution.Get(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, st.FlowExecutionStatusCommitted, row.Status)
}

func TestReconcile_CoordinatorRolledBack_TransitionsRowToRolledBack(t *testing.T) {
	ctx, _ := db.ConnectToTestPostgres(t)
	cfg := testConfigWithOperator(t, 0)
	id := uuid.New()
	stuckParticipantRow(t, ctx, id, 0)

	r := &participantReconciler{
		config: cfg,
		knobs:  recoveryEnabledKnobs(),
		query: stubQuery(&pbinternal.ConsensusQueryOutcomeResponse{
			Outcome:         pbinternal.ConsensusQueryOutcomeResponse_OUTCOME_ROLLED_BACK,
			OpType:          int32(pbgossip.ConsensusOperationType_CONSENSUS_OPERATION_TYPE_STORE_PREIMAGE_SHARE),
			DecisionPayload: anyOperation(t),
		}),
		gossipHandler: handler.NewGossipHandler(cfg),
	}
	require.NoError(t, r.reconcile(ctx))

	client, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)
	row, err := client.FlowExecution.Get(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, st.FlowExecutionStatusRolledBack, row.Status)
}

func TestReconcile_CoordinatorInFlight_LeavesRowInFlight(t *testing.T) {
	ctx, _ := db.ConnectToTestPostgres(t)
	cfg := testConfigWithOperator(t, 0)
	id := uuid.New()
	stuckParticipantRow(t, ctx, id, 0)

	r := &participantReconciler{
		config: cfg,
		knobs:  recoveryEnabledKnobs(),
		query: stubQuery(&pbinternal.ConsensusQueryOutcomeResponse{
			Outcome: pbinternal.ConsensusQueryOutcomeResponse_OUTCOME_IN_FLIGHT,
		}),
		gossipHandler: handler.NewGossipHandler(cfg),
	}
	require.NoError(t, r.reconcile(ctx))

	client, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)
	row, err := client.FlowExecution.Get(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, st.FlowExecutionStatusInFlight, row.Status)
}

func TestReconcile_CoordinatorUnspecified_LeavesRowInFlight(t *testing.T) {
	ctx, _ := db.ConnectToTestPostgres(t)
	cfg := testConfigWithOperator(t, 0)
	id := uuid.New()
	stuckParticipantRow(t, ctx, id, 0)

	r := &participantReconciler{
		config: cfg,
		knobs:  recoveryEnabledKnobs(),
		query: stubQuery(&pbinternal.ConsensusQueryOutcomeResponse{
			Outcome: pbinternal.ConsensusQueryOutcomeResponse_OUTCOME_UNSPECIFIED,
		}),
		gossipHandler: handler.NewGossipHandler(cfg),
	}
	require.NoError(t, r.reconcile(ctx))

	client, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)
	row, err := client.FlowExecution.Get(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, st.FlowExecutionStatusInFlight, row.Status,
		"UNSPECIFIED (coordinator data loss) must not prematurely terminate the row")
}

func TestReconcile_RpcError_LeavesRowInFlightAndContinues(t *testing.T) {
	ctx, _ := db.ConnectToTestPostgres(t)
	cfg := testConfigWithOperator(t, 0)
	id1, id2 := uuid.New(), uuid.New()
	stuckParticipantRow(t, ctx, id1, 0)
	stuckParticipantRow(t, ctx, id2, 0)

	calls := 0
	failingThenSucceedingQuery := func(_ context.Context, _ *so.SigningOperator, _ string) (*pbinternal.ConsensusQueryOutcomeResponse, error) {
		calls++
		if calls == 1 {
			return nil, fmt.Errorf("network blip")
		}
		return &pbinternal.ConsensusQueryOutcomeResponse{
			Outcome:         pbinternal.ConsensusQueryOutcomeResponse_OUTCOME_COMMITTED,
			OpType:          int32(pbgossip.ConsensusOperationType_CONSENSUS_OPERATION_TYPE_STORE_PREIMAGE_SHARE),
			DecisionPayload: anyOperation(t),
		}, nil
	}

	r := &participantReconciler{
		config:        cfg,
		knobs:         recoveryEnabledKnobs(),
		query:         failingThenSucceedingQuery,
		gossipHandler: handler.NewGossipHandler(cfg),
	}
	require.NoError(t, r.reconcile(ctx), "per-row RPC failure must not abort the whole sweep")
	assert.Equal(t, 2, calls, "both stuck rows should be attempted")
}

// ---------- SweepStaleCoordinatorFlows ----------

func TestSweepStaleCoordinatorFlows_TransitionsOldInFlightToRolledBack(t *testing.T) {
	ctx, _ := db.ConnectToTestPostgres(t)
	// Default coordinator stall threshold is 600s (10m); use 1h for "old" to
	// stay comfortably past it, 10s for "fresh" to stay comfortably under.
	old := insertStaleCoordinatorRow(t, ctx, st.FlowExecutionStatusInFlight, 1*time.Hour)
	fresh := insertStaleCoordinatorRow(t, ctx, st.FlowExecutionStatusInFlight, 10*time.Second)
	terminal := insertStaleCoordinatorRow(t, ctx, st.FlowExecutionStatusCommitted, 1*time.Hour)

	require.NoError(t, SweepStaleCoordinatorFlows(ctx, sparktesting.TestConfig(t), recoveryEnabledKnobs()))

	client, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	oldRow, err := client.FlowExecution.Get(ctx, old)
	require.NoError(t, err)
	assert.Equal(t, st.FlowExecutionStatusRolledBack, oldRow.Status, "old IN_FLIGHT row should be rolled back")

	freshRow, err := client.FlowExecution.Get(ctx, fresh)
	require.NoError(t, err)
	assert.Equal(t, st.FlowExecutionStatusInFlight, freshRow.Status, "fresh IN_FLIGHT row must be untouched")

	terminalRow, err := client.FlowExecution.Get(ctx, terminal)
	require.NoError(t, err)
	assert.Equal(t, st.FlowExecutionStatusCommitted, terminalRow.Status, "already-terminal row must be untouched")
}

// ---------- Batch limit + ordering ----------

// TestReconcile_RespectsBatchLimitAndOldestFirstOrdering inserts more stuck
// participant rows than the batch limit, runs reconcile, and verifies that
// only the batch-limit oldest rows are transitioned. Pins both invariants:
// the reconcile sweep is bounded, and it processes the oldest rows first
// (so a row with a permanently-down coordinator can't monopolize ticks
// indefinitely once newer rows fall past the threshold).
func TestReconcile_RespectsBatchLimitAndOldestFirstOrdering(t *testing.T) {
	ctx, _ := db.ConnectToTestPostgres(t)
	cfg := testConfigWithOperator(t, 0)

	// Insert 5 rows with monotonically increasing age (older first). With a
	// batch limit of 2, only the 2 oldest should be processed.
	const batchLimit = 2
	const totalRows = 5
	ids := make([]uuid.UUID, totalRows)
	for i := range totalRows {
		ids[i] = uuid.New()
		// row[0] is oldest (5h), row[totalRows-1] is newest of the qualifying
		// set (1h). All exceed the default stuck threshold (300s) so they
		// all qualify; ordering decides which the batch picks.
		age := time.Duration(totalRows-i) * time.Hour
		insertStaleParticipantRow(t, ctx, ids[i], 0, age)
	}

	r := &participantReconciler{
		config: cfg,
		knobs: knobs.NewFixedKnobs(map[string]float64{
			knobs.KnobFlowExecutionSweepBatchLimit:  batchLimit,
			knobs.KnobFlowExecutionReconcileEnabled: 1,
		}),
		query: stubQuery(&pbinternal.ConsensusQueryOutcomeResponse{
			Outcome:         pbinternal.ConsensusQueryOutcomeResponse_OUTCOME_COMMITTED,
			OpType:          int32(pbgossip.ConsensusOperationType_CONSENSUS_OPERATION_TYPE_STORE_PREIMAGE_SHARE),
			DecisionPayload: anyOperation(t),
		}),
		gossipHandler: handler.NewGossipHandler(cfg),
	}
	require.NoError(t, r.reconcile(ctx))

	client, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)
	for i, id := range ids {
		row, err := client.FlowExecution.Get(ctx, id)
		require.NoError(t, err)
		if i < batchLimit {
			assert.Equal(t, st.FlowExecutionStatusCommitted, row.Status,
				"row %d (age=%dh) is among the oldest %d and should have been processed",
				i, totalRows-i, batchLimit)
		} else {
			assert.Equal(t, st.FlowExecutionStatusInFlight, row.Status,
				"row %d (age=%dh) is past the batch limit and should be untouched",
				i, totalRows-i)
		}
	}
}

// TestSweepStaleCoordinatorFlows_RespectsBatchLimitAndOldestFirstOrdering is
// the coordinator-sweep counterpart: with batch_limit=2 and 5 stale
// IN_FLIGHT coordinator rows of monotonically increasing age, only the 2
// oldest should flip to ROLLED_BACK on a single sweep tick.
func TestSweepStaleCoordinatorFlows_RespectsBatchLimitAndOldestFirstOrdering(t *testing.T) {
	ctx, _ := db.ConnectToTestPostgres(t)

	const batchLimit = 2
	const totalRows = 5
	ids := make([]uuid.UUID, totalRows)
	for i := range totalRows {
		// All rows exceed the default coordinator stall threshold (600s);
		// row[0] is oldest. Use 1h granularity so the order is unambiguous
		// even under DB clock skew.
		age := time.Duration(totalRows-i+1) * time.Hour
		ids[i] = insertStaleCoordinatorRow(t, ctx, st.FlowExecutionStatusInFlight, age)
	}

	require.NoError(t, SweepStaleCoordinatorFlows(ctx, sparktesting.TestConfig(t),
		knobs.NewFixedKnobs(map[string]float64{
			knobs.KnobFlowExecutionSweepBatchLimit:  batchLimit,
			knobs.KnobFlowExecutionReconcileEnabled: 1,
		})))

	client, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)
	for i, id := range ids {
		row, err := client.FlowExecution.Get(ctx, id)
		require.NoError(t, err)
		if i < batchLimit {
			assert.Equal(t, st.FlowExecutionStatusRolledBack, row.Status,
				"row %d is among the oldest %d and should have been swept", i, batchLimit)
		} else {
			assert.Equal(t, st.FlowExecutionStatusInFlight, row.Status,
				"row %d is past the batch limit and should be untouched", i)
		}
	}
}

// ---------- Monitor-only mode (KnobFlowExecutionReconcileEnabled = 0) ----------

// TestReconcile_MonitorOnly_LeavesRowsUntouched confirms that with the
// reconcile-enabled knob off (production default), the participant
// reconcile pass identifies stuck rows but does not call the coordinator
// and does not transition any row. The row stays IN_FLIGHT for the next
// tick to consider once the operator decides to enable recovery.
func TestReconcile_MonitorOnly_LeavesRowsUntouched(t *testing.T) {
	ctx, _ := db.ConnectToTestPostgres(t)
	cfg := testConfigWithOperator(t, 0)
	id := uuid.New()
	stuckParticipantRow(t, ctx, id, 0)

	queryCalls := 0
	r := &participantReconciler{
		config: cfg,
		// Empty knobs: KnobFlowExecutionReconcileEnabled defaults to 0.
		knobs: knobs.NewEmptyFixedKnobs(),
		query: func(_ context.Context, _ *so.SigningOperator, _ string) (*pbinternal.ConsensusQueryOutcomeResponse, error) {
			queryCalls++
			return &pbinternal.ConsensusQueryOutcomeResponse{
				Outcome: pbinternal.ConsensusQueryOutcomeResponse_OUTCOME_COMMITTED,
			}, nil
		},
		gossipHandler: handler.NewGossipHandler(cfg),
	}
	require.NoError(t, r.reconcile(ctx))

	assert.Equal(t, 0, queryCalls, "monitor-only mode must not issue ConsensusQueryOutcome RPCs")

	client, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)
	row, err := client.FlowExecution.Get(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, st.FlowExecutionStatusInFlight, row.Status,
		"monitor-only mode must leave the stuck row IN_FLIGHT")
}

// TestSweepStaleCoordinatorFlows_MonitorOnly_LeavesRowsUntouched is the
// coordinator-side counterpart: stale IN_FLIGHT coordinator rows are
// identified and logged but not transitioned to ROLLED_BACK while the
// reconcile-enabled knob is off.
func TestSweepStaleCoordinatorFlows_MonitorOnly_LeavesRowsUntouched(t *testing.T) {
	ctx, _ := db.ConnectToTestPostgres(t)
	id := insertStaleCoordinatorRow(t, ctx, st.FlowExecutionStatusInFlight, 1*time.Hour)

	require.NoError(t, SweepStaleCoordinatorFlows(ctx, sparktesting.TestConfig(t), knobs.NewEmptyFixedKnobs()))

	client, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)
	row, err := client.FlowExecution.Get(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, st.FlowExecutionStatusInFlight, row.Status,
		"monitor-only mode must leave the stale coordinator row IN_FLIGHT")
}
