package task

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	pbgossip "github.com/lightsparkdev/spark/proto/gossip"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/ent/flowexecution"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// insertFlowExecutionWithAge inserts a FlowExecution with the given role and
// status, backdating its update_time so the purge cutoff comparison sees it
// as old.
func insertFlowExecutionWithAge(t *testing.T, ctx context.Context, role st.FlowExecutionRole, status st.FlowExecutionStatus, age time.Duration) uuid.UUID {
	t.Helper()
	client, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)
	id := uuid.New()
	_, err = client.FlowExecution.Create().
		SetID(id).
		SetRole(role).
		SetOpType(int32(pbgossip.ConsensusOperationType_CONSENSUS_OPERATION_TYPE_STORE_PREIMAGE_SHARE)).
		SetStatus(status).
		SetCoordinatorIndex(0).
		Save(ctx)
	require.NoError(t, err)
	// Ent's UpdateDefault(time.Now) sets update_time at insertion; rewrite it.
	_, err = client.FlowExecution.Update().
		Where(flowexecution.ID(id)).
		SetUpdateTime(time.Now().Add(-age)).
		Save(ctx)
	require.NoError(t, err)
	return id
}

// TestPurgeTerminalFlowExecutions_DeletesOldTerminalParticipantRows verifies
// the happy-path: PARTICIPANT rows in COMMITTED / ROLLED_BACK older than the
// TTL are purged.
func TestPurgeTerminalFlowExecutions_DeletesOldTerminalParticipantRows(t *testing.T) {
	ctx, _ := db.ConnectToTestPostgres(t)

	oldCommitted := insertFlowExecutionWithAge(t, ctx, st.FlowExecutionRoleParticipant, st.FlowExecutionStatusCommitted, 8*24*time.Hour)
	oldRolledBack := insertFlowExecutionWithAge(t, ctx, st.FlowExecutionRoleParticipant, st.FlowExecutionStatusRolledBack, 10*24*time.Hour)

	require.NoError(t, PurgeTerminalFlowExecutions(ctx, 7*24*time.Hour, 100))

	// Re-fetch the client after the purge: PurgeTerminalFlowExecutions
	// commits its transaction, so any client cached from before the call
	// would be pointing at a closed tx.
	client, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)
	for _, id := range []uuid.UUID{oldCommitted, oldRolledBack} {
		exists, err := client.FlowExecution.Query().Where(flowexecution.ID(id)).Exist(ctx)
		require.NoError(t, err)
		assert.False(t, exists, "row %s should have been purged", id)
	}
}

// TestPurgeTerminalFlowExecutions_NeverDeletesCoordinatorRows verifies the
// load-bearing invariant from the codex P1 review feedback: COORDINATOR rows
// are the authoritative source ConsensusQueryOutcome reads from for late
// participant reconciliation. Purging them would let a participant that
// stayed offline past the TTL come back, query the (now-missing) coordinator,
// hit the presumed-abort path, and roll back a flow that actually committed.
func TestPurgeTerminalFlowExecutions_NeverDeletesCoordinatorRows(t *testing.T) {
	ctx, _ := db.ConnectToTestPostgres(t)

	// Both terminal statuses, well past the TTL. None of these should be
	// touched by the purge.
	oldCommittedCoord := insertFlowExecutionWithAge(t, ctx, st.FlowExecutionRoleCoordinator, st.FlowExecutionStatusCommitted, 30*24*time.Hour)
	oldRolledBackCoord := insertFlowExecutionWithAge(t, ctx, st.FlowExecutionRoleCoordinator, st.FlowExecutionStatusRolledBack, 30*24*time.Hour)

	require.NoError(t, PurgeTerminalFlowExecutions(ctx, 7*24*time.Hour, 100))

	client, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)
	for _, id := range []uuid.UUID{oldCommittedCoord, oldRolledBackCoord} {
		exists, err := client.FlowExecution.Query().Where(flowexecution.ID(id)).Exist(ctx)
		require.NoError(t, err)
		assert.True(t, exists, "COORDINATOR row %s must never be purged (ConsensusQueryOutcome relies on it)", id)
	}
}

// TestPurgeTerminalFlowExecutions_PreservesYoungRows verifies that terminal
// rows newer than the TTL stay put — recent activity must not be touched.
func TestPurgeTerminalFlowExecutions_PreservesYoungRows(t *testing.T) {
	ctx, _ := db.ConnectToTestPostgres(t)

	youngCommitted := insertFlowExecutionWithAge(t, ctx, st.FlowExecutionRoleParticipant, st.FlowExecutionStatusCommitted, 1*time.Hour)
	youngRolledBack := insertFlowExecutionWithAge(t, ctx, st.FlowExecutionRoleParticipant, st.FlowExecutionStatusRolledBack, 6*24*time.Hour)

	require.NoError(t, PurgeTerminalFlowExecutions(ctx, 7*24*time.Hour, 100))

	client, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)
	for _, id := range []uuid.UUID{youngCommitted, youngRolledBack} {
		exists, err := client.FlowExecution.Query().Where(flowexecution.ID(id)).Exist(ctx)
		require.NoError(t, err)
		assert.True(t, exists, "row %s should NOT have been purged", id)
	}
}

// TestPurgeTerminalFlowExecutions_NeverDeletesInFlight is the load-bearing
// invariant: IN_FLIGHT rows must never be purged regardless of age. A
// long-stuck IN_FLIGHT row is a bug to investigate via the reconcile/sweep
// path, not data to silently drop.
func TestPurgeTerminalFlowExecutions_NeverDeletesInFlight(t *testing.T) {
	ctx, _ := db.ConnectToTestPostgres(t)

	// Cover both roles — neither should be touched.
	ancientInFlightParticipant := insertFlowExecutionWithAge(t, ctx, st.FlowExecutionRoleParticipant, st.FlowExecutionStatusInFlight, 30*24*time.Hour)
	ancientInFlightCoord := insertFlowExecutionWithAge(t, ctx, st.FlowExecutionRoleCoordinator, st.FlowExecutionStatusInFlight, 30*24*time.Hour)

	require.NoError(t, PurgeTerminalFlowExecutions(ctx, 7*24*time.Hour, 100))

	client, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)
	for _, id := range []uuid.UUID{ancientInFlightParticipant, ancientInFlightCoord} {
		exists, err := client.FlowExecution.Query().Where(flowexecution.ID(id)).Exist(ctx)
		require.NoError(t, err)
		assert.True(t, exists, "IN_FLIGHT row %s must never be purged regardless of age", id)
	}
}

// TestPurgeTerminalFlowExecutions_NoEligibleRows_ReturnsNil exercises the
// happy path when nothing needs purging — the task must return cleanly
// without error.
func TestPurgeTerminalFlowExecutions_NoEligibleRows_ReturnsNil(t *testing.T) {
	ctx, _ := db.ConnectToTestPostgres(t)
	require.NoError(t, PurgeTerminalFlowExecutions(ctx, 7*24*time.Hour, 100))
}

// TestPurgeTerminalFlowExecutions_BatchesAcrossMultiplePages verifies the
// loop drains a backlog larger than a single batch.
func TestPurgeTerminalFlowExecutions_BatchesAcrossMultiplePages(t *testing.T) {
	ctx, _ := db.ConnectToTestPostgres(t)

	const total = 25
	ids := make([]uuid.UUID, 0, total)
	for range total {
		ids = append(ids, insertFlowExecutionWithAge(t, ctx, st.FlowExecutionRoleParticipant, st.FlowExecutionStatusCommitted, 8*24*time.Hour))
	}

	// batchSize=10 → 3 iterations to drain 25 rows.
	require.NoError(t, PurgeTerminalFlowExecutions(ctx, 7*24*time.Hour, 10))

	client, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)
	for _, id := range ids {
		exists, err := client.FlowExecution.Query().Where(flowexecution.ID(id)).Exist(ctx)
		require.NoError(t, err)
		assert.False(t, exists, "row %s should have been purged", id)
	}
}
