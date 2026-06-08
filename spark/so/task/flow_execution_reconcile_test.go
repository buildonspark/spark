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
	"google.golang.org/protobuf/proto"
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
	insertStaleParticipantRowWithPayload(t, ctx, id, coordIdx, age, nil)
}

// insertStaleParticipantRowWithPayload is the variant used by presumed-abort
// tests that exercise the rollback-from-persisted-prepare-payload path. nil
// payload models pre-rollout rows.
func insertStaleParticipantRowWithPayload(t *testing.T, ctx context.Context, id uuid.UUID, coordIdx uint, age time.Duration, prepare []byte) {
	t.Helper()
	client, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)
	create := client.FlowExecution.Create().
		SetID(id).
		SetRole(st.FlowExecutionRoleParticipant).
		SetOpType(int32(pbgossip.ConsensusOperationType_CONSENSUS_OPERATION_TYPE_STORE_PREIMAGE_SHARE)).
		SetCoordinatorIndex(coordIdx)
	if prepare != nil {
		create = create.SetPreparePayload(prepare)
	}
	_, err = create.Save(ctx)
	require.NoError(t, err)
	// Ent's update_time has UpdateDefault(time.Now), so we have to update
	// the column directly to backdate it.
	_, err = client.FlowExecution.Update().
		Where(flowexecution.ID(id)).
		SetUpdateTime(time.Now().Add(-age)).
		Save(ctx)
	require.NoError(t, err)
}

// preparePayloadBytes returns the marshaled-Any bytes the consensus dispatcher
// persists on a PARTICIPANT row at create time. Tests stash this on
// fixtures so the reconciler's presumed-abort path can unmarshal it back.
func preparePayloadBytes(t *testing.T) []byte {
	t.Helper()
	bytes, err := proto.Marshal(anyOperation(t))
	require.NoError(t, err)
	return bytes
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

// requireStuckRowsErr asserts that the reconcile/sweep task returned a
// stuck-rows alert error for the given role. Both tasks now intentionally
// surface this as a task-level error (so the scheduler routes it to the
// alerting pipeline) whenever the threshold-based query found any rows —
// independent of whether recovery succeeded for those rows. Every test
// that seeds a stuck row must therefore expect the error.
func requireStuckRowsErr(t *testing.T, err error, role string) {
	t.Helper()
	require.Error(t, err, "task must surface stuck rows as an error so on-call gets a Slack alert")
	require.ErrorContains(t, err, "stuck "+role+" flow_execution rows")
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

// ---------- ReconcileStuckParticipantFlows ----------

func TestReconcile_CoordinatorCommitted_TransitionsRowToCommitted(t *testing.T) {
	ctx, _ := db.ConnectToTestPostgres(t)
	cfg := testConfigWithOperator(t, 0)
	id := uuid.New()
	stuckParticipantRow(t, ctx, id, 0)

	r := &participantReconciler{
		config: cfg,
		knobs:  knobs.NewEmptyFixedKnobs(),
		query: stubQuery(&pbinternal.ConsensusQueryOutcomeResponse{
			Outcome:         pbinternal.ConsensusQueryOutcomeResponse_OUTCOME_COMMITTED,
			OpType:          int32(pbgossip.ConsensusOperationType_CONSENSUS_OPERATION_TYPE_STORE_PREIMAGE_SHARE),
			DecisionPayload: anyOperation(t),
		}),
		gossipHandler: handler.NewGossipHandler(cfg),
	}
	requireStuckRowsErr(t, r.reconcile(ctx), "PARTICIPANT")

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
		knobs:  knobs.NewEmptyFixedKnobs(),
		query: stubQuery(&pbinternal.ConsensusQueryOutcomeResponse{
			Outcome:         pbinternal.ConsensusQueryOutcomeResponse_OUTCOME_ROLLED_BACK,
			OpType:          int32(pbgossip.ConsensusOperationType_CONSENSUS_OPERATION_TYPE_STORE_PREIMAGE_SHARE),
			DecisionPayload: anyOperation(t),
		}),
		gossipHandler: handler.NewGossipHandler(cfg),
	}
	requireStuckRowsErr(t, r.reconcile(ctx), "PARTICIPANT")

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
		knobs:  knobs.NewEmptyFixedKnobs(),
		query: stubQuery(&pbinternal.ConsensusQueryOutcomeResponse{
			Outcome: pbinternal.ConsensusQueryOutcomeResponse_OUTCOME_IN_FLIGHT,
		}),
		gossipHandler: handler.NewGossipHandler(cfg),
	}
	requireStuckRowsErr(t, r.reconcile(ctx), "PARTICIPANT")

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
	// Legacy row: no prepare_payload. Models a row created before the
	// presumed-abort recovery shipped. UNSPECIFIED should fall through to
	// the legacy "log and skip" path even at 1h age.
	stuckParticipantRow(t, ctx, id, 0)

	r := &participantReconciler{
		config: cfg,
		knobs:  knobs.NewEmptyFixedKnobs(),
		query: stubQuery(&pbinternal.ConsensusQueryOutcomeResponse{
			Outcome: pbinternal.ConsensusQueryOutcomeResponse_OUTCOME_UNSPECIFIED,
		}),
		gossipHandler: handler.NewGossipHandler(cfg),
	}
	requireStuckRowsErr(t, r.reconcile(ctx), "PARTICIPANT")

	client, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)
	row, err := client.FlowExecution.Get(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, st.FlowExecutionStatusInFlight, row.Status,
		"UNSPECIFIED with no persisted prepare_payload must not prematurely terminate the row")
}

func TestReconcile_CoordinatorUnspecified_OldRowWithPayload_PresumedAbortsToRolledBack(t *testing.T) {
	ctx, _ := db.ConnectToTestPostgres(t)
	cfg := testConfigWithOperator(t, 0)
	id := uuid.New()
	// Row is past the presumed-abort age and carries the persisted prepare
	// op — this is the bug case we're recovering: coordinator's request tx
	// rolled back so its FlowExecution row never landed, leaving the
	// participant stuck IN_FLIGHT with no decision available via gossip or
	// query. The reconciler should synthesize a rollback locally.
	insertStaleParticipantRowWithPayload(t, ctx, id, 0, 1*time.Hour, preparePayloadBytes(t))

	r := &participantReconciler{
		config: cfg,
		knobs:  knobs.NewEmptyFixedKnobs(),
		query: stubQuery(&pbinternal.ConsensusQueryOutcomeResponse{
			Outcome: pbinternal.ConsensusQueryOutcomeResponse_OUTCOME_UNSPECIFIED,
		}),
		gossipHandler: handler.NewGossipHandler(cfg),
	}
	requireStuckRowsErr(t, r.reconcile(ctx), "PARTICIPANT")

	client, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)
	row, err := client.FlowExecution.Get(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, st.FlowExecutionStatusRolledBack, row.Status,
		"UNSPECIFIED past presumed-abort age with persisted prepare_payload must roll back locally")
}

func TestReconcile_CoordinatorUnspecified_YoungRowWithPayload_LeavesRowInFlight(t *testing.T) {
	ctx, _ := db.ConnectToTestPostgres(t)
	cfg := testConfigWithOperator(t, 0)
	id := uuid.New()
	// Old enough to be picked up by the stuck-threshold sweep (300s) but
	// younger than the presumed-abort age (1200s). Guards against firing
	// presumed-abort while the coordinator may still be deciding.
	insertStaleParticipantRowWithPayload(t, ctx, id, 0, 10*time.Minute, preparePayloadBytes(t))

	r := &participantReconciler{
		config: cfg,
		knobs:  knobs.NewEmptyFixedKnobs(),
		query: stubQuery(&pbinternal.ConsensusQueryOutcomeResponse{
			Outcome: pbinternal.ConsensusQueryOutcomeResponse_OUTCOME_UNSPECIFIED,
		}),
		gossipHandler: handler.NewGossipHandler(cfg),
	}
	requireStuckRowsErr(t, r.reconcile(ctx), "PARTICIPANT")

	client, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)
	row, err := client.FlowExecution.Get(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, st.FlowExecutionStatusInFlight, row.Status,
		"UNSPECIFIED below presumed-abort age must not roll back even with a payload — coordinator may still be deciding")
}

// Regression for the codex P1 review comment: Ent represents a SQL NULL
// bytea as either a nil pointer OR a non-nil pointer to an empty slice
// depending on driver/version. The original guard only checked nil, so a
// row holding an empty (but non-nil) prepare_payload would slip past it
// and fail later on proto.Unmarshal of zero bytes. Both shapes must take
// the legacy log-and-skip path.
func TestReconcile_CoordinatorUnspecified_EmptyPayload_LeavesRowInFlight(t *testing.T) {
	ctx, _ := db.ConnectToTestPostgres(t)
	cfg := testConfigWithOperator(t, 0)
	id := uuid.New()
	// Old enough to trigger presumed-abort if not for the empty-payload
	// guard. Empty []byte distinguishes the "non-nil pointer to empty
	// slice" case from the "nil pointer" case the legacy test covers.
	insertStaleParticipantRowWithPayload(t, ctx, id, 0, 1*time.Hour, []byte{})

	r := &participantReconciler{
		config: cfg,
		knobs:  knobs.NewEmptyFixedKnobs(),
		query: stubQuery(&pbinternal.ConsensusQueryOutcomeResponse{
			Outcome: pbinternal.ConsensusQueryOutcomeResponse_OUTCOME_UNSPECIFIED,
		}),
		gossipHandler: handler.NewGossipHandler(cfg),
	}
	requireStuckRowsErr(t, r.reconcile(ctx), "PARTICIPANT")

	client, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)
	row, err := client.FlowExecution.Get(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, st.FlowExecutionStatusInFlight, row.Status,
		"empty (non-nil) prepare_payload must take the legacy log-and-skip path, not attempt to unmarshal zero bytes")
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
		knobs:         knobs.NewEmptyFixedKnobs(),
		query:         failingThenSucceedingQuery,
		gossipHandler: handler.NewGossipHandler(cfg),
	}
	requireStuckRowsErr(t, r.reconcile(ctx), "PARTICIPANT")
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

	requireStuckRowsErr(t, SweepStaleCoordinatorFlows(ctx, sparktesting.TestConfig(t), knobs.NewEmptyFixedKnobs()), "COORDINATOR")

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
			knobs.KnobFlowExecutionSweepBatchLimit: batchLimit,
		}),
		query: stubQuery(&pbinternal.ConsensusQueryOutcomeResponse{
			Outcome:         pbinternal.ConsensusQueryOutcomeResponse_OUTCOME_COMMITTED,
			OpType:          int32(pbgossip.ConsensusOperationType_CONSENSUS_OPERATION_TYPE_STORE_PREIMAGE_SHARE),
			DecisionPayload: anyOperation(t),
		}),
		gossipHandler: handler.NewGossipHandler(cfg),
	}
	requireStuckRowsErr(t, r.reconcile(ctx), "PARTICIPANT")

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

	requireStuckRowsErr(t, SweepStaleCoordinatorFlows(ctx, sparktesting.TestConfig(t),
		knobs.NewFixedKnobs(map[string]float64{
			knobs.KnobFlowExecutionSweepBatchLimit: batchLimit,
		})), "COORDINATOR")

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

// ---------- Stuck-row alert: steady-state (no rows) and error-format ----------

// TestReconcile_NoStuckRows_ReturnsNil is the steady-state assertion: when
// the threshold-based query finds no participant rows, the task must NOT
// return an error — otherwise every healthy operator would page on every
// 30s tick.
func TestReconcile_NoStuckRows_ReturnsNil(t *testing.T) {
	ctx, _ := db.ConnectToTestPostgres(t)
	cfg := testConfigWithOperator(t, 0)

	r := &participantReconciler{
		config:        cfg,
		knobs:         knobs.NewEmptyFixedKnobs(),
		query:         stubQuery(nil),
		gossipHandler: handler.NewGossipHandler(cfg),
	}
	require.NoError(t, r.reconcile(ctx),
		"healthy steady-state (no stuck rows) must return nil so the alert pipeline stays quiet")
}

// TestSweepStaleCoordinatorFlows_NoStaleRows_ReturnsNil is the
// steady-state companion for the coordinator sweep.
func TestSweepStaleCoordinatorFlows_NoStaleRows_ReturnsNil(t *testing.T) {
	ctx, _ := db.ConnectToTestPostgres(t)
	require.NoError(t, SweepStaleCoordinatorFlows(ctx, sparktesting.TestConfig(t), knobs.NewEmptyFixedKnobs()),
		"healthy steady-state (no stale rows) must return nil so the alert pipeline stays quiet")
}

// TestStuckFlowExecutionError_IncludesActionableContext checks the error
// message format the Slack alert will surface — count + role + a sample of
// (id, op_type, age) so on-call has enough to start an investigation
// without paging logs first.
func TestStuckFlowExecutionError_IncludesActionableContext(t *testing.T) {
	ctx, _ := db.ConnectToTestPostgres(t)
	cfg := testConfigWithOperator(t, 0)
	// Five stuck rows so the "+more in batch" suffix is exercised (sample limit is 3).
	ids := make([]uuid.UUID, 5)
	for i := range ids {
		ids[i] = uuid.New()
		insertStaleParticipantRow(t, ctx, ids[i], 0, time.Hour+time.Duration(i)*time.Second)
	}

	r := &participantReconciler{
		config:        cfg,
		knobs:         knobs.NewEmptyFixedKnobs(),
		query:         stubQuery(&pbinternal.ConsensusQueryOutcomeResponse{Outcome: pbinternal.ConsensusQueryOutcomeResponse_OUTCOME_IN_FLIGHT}),
		gossipHandler: handler.NewGossipHandler(cfg),
	}
	err := r.reconcile(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "found 5 stuck PARTICIPANT flow_execution rows",
		"error must lead with the role-qualified count for Slack alert legibility")
	assert.Contains(t, err.Error(), "op_type=CONSENSUS_OPERATION_TYPE_STORE_PREIMAGE_SHARE",
		"op_type must be the human-readable proto enum name, not the numeric value, so on-call doesn't have to cross-reference the proto file")
	assert.Contains(t, err.Error(), "+2 more in batch",
		"sample suffix must indicate how many rows beyond the sample exist within this batch")
}

// TestReconcile_RecoveryPersistsAcrossAlertError is the codex-P1 regression:
// the alert error must NOT cause the task middleware to roll back recovery
// work the reconciler just persisted via the session tx. If the commit-
// before-error path inside reconcile() ever regresses, this test fails
// because (a) the session's tx is still open after reconcile returns, and
// (b) production rollback would have undone the COMMITTED transition.
func TestReconcile_RecoveryPersistsAcrossAlertError(t *testing.T) {
	ctx, dbCtx := db.ConnectToTestPostgres(t)
	cfg := testConfigWithOperator(t, 0)
	id := uuid.New()
	stuckParticipantRow(t, ctx, id, 0)

	r := &participantReconciler{
		config: cfg,
		knobs:  knobs.NewEmptyFixedKnobs(),
		query: stubQuery(&pbinternal.ConsensusQueryOutcomeResponse{
			Outcome:         pbinternal.ConsensusQueryOutcomeResponse_OUTCOME_COMMITTED,
			OpType:          int32(pbgossip.ConsensusOperationType_CONSENSUS_OPERATION_TYPE_STORE_PREIMAGE_SHARE),
			DecisionPayload: anyOperation(t),
		}),
		gossipHandler: handler.NewGossipHandler(cfg),
	}

	// reconcile must return the alert error (rows were found) but
	// also persist the recovery transition.
	err := r.reconcile(ctx)
	requireStuckRowsErr(t, err, "PARTICIPANT")

	// Session has no live tx — proves DbCommit ran inside reconcile
	// before the error return. In production this is what makes
	// DatabaseMiddleware's deferred rollback a no-op; without it,
	// the COMMITTED transition would be rolled back and the same
	// row would alert (and re-fail to recover) every tick forever.
	assert.Nil(t, dbCtx.Session.GetTxIfExists(),
		"reconcile must DbCommit recovery work before returning the alert error")

	// Recovery actually persisted: re-read via the bare client (no
	// session tx in scope) and assert the row is COMMITTED. This
	// would fail if the deferred rollback semantics weren't honored.
	row, getErr := dbCtx.Client.FlowExecution.Get(t.Context(), id)
	require.NoError(t, getErr)
	assert.Equal(t, st.FlowExecutionStatusCommitted, row.Status,
		"recovery transition must remain on disk across the alert-error return")
}

// TestReconcile_BacklogScaleShownInAlertWhenLargerThanBatch — the greptile
// P2 regression. When the stuck backlog exceeds the sweep batchLimit,
// the alert message must surface "<batch>/<total>" so on-call sees actual
// scale and isn't misled by a steady "found 50" forever.
func TestReconcile_BacklogScaleShownInAlertWhenLargerThanBatch(t *testing.T) {
	ctx, _ := db.ConnectToTestPostgres(t)
	cfg := testConfigWithOperator(t, 0)
	const totalRows = 5
	const batchLimit = 2
	for i := range totalRows {
		insertStaleParticipantRow(t, ctx, uuid.New(), 0, time.Hour+time.Duration(i)*time.Second)
	}

	r := &participantReconciler{
		config: cfg,
		knobs: knobs.NewFixedKnobs(map[string]float64{
			knobs.KnobFlowExecutionSweepBatchLimit: batchLimit,
		}),
		query: stubQuery(&pbinternal.ConsensusQueryOutcomeResponse{
			Outcome: pbinternal.ConsensusQueryOutcomeResponse_OUTCOME_IN_FLIGHT,
		}),
		gossipHandler: handler.NewGossipHandler(cfg),
	}
	err := r.reconcile(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "found 2/5 (batch limit reached) stuck PARTICIPANT",
		"alert must show <batch>/<total> when the backlog exceeds batchLimit so on-call sees true scale")
}
