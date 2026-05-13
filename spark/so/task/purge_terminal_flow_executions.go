package task

import (
	"context"
	"fmt"
	"time"

	"entgo.io/ent/dialect/sql"
	"github.com/lightsparkdev/spark/common/logging"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/ent/flowexecution"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
)

const (
	// purgeTerminalFlowExecutionsTTL is how long a terminal (COMMITTED or
	// ROLLED_BACK) PARTICIPANT FlowExecution row stays around before it
	// becomes eligible for purge. Sized to comfortably exceed any window in
	// which the reconciliation / sweep tasks could still need to consult the
	// row.
	purgeTerminalFlowExecutionsTTL = 7 * 24 * time.Hour

	// purgeTerminalFlowExecutionsBatchSize bounds a single DELETE so we don't
	// hold one giant transaction. Matches the purge_idempotency_keys pattern.
	purgeTerminalFlowExecutionsBatchSize = 10000
)

// purgeTerminalFlowExecutionsTimeout caps a single weekly run so the scheduler
// isn't blocked indefinitely while draining a large initial backlog. Matches
// the timeout used by other long-running bulk-delete tasks
// (delete_stale_tree_nodes, purge_signing_nonce_partitions). Declared as a
// var rather than a const so it's addressable for BaseTaskSpec.Timeout.
var purgeTerminalFlowExecutionsTimeout = 10 * time.Minute

// PurgeTerminalFlowExecutions deletes COMMITTED / ROLLED_BACK PARTICIPANT
// FlowExecution rows whose update_time is older than ttl.
//
// Two rows are kept regardless of age:
//
//   - IN_FLIGHT rows on any role: the reconcile / sweep tasks are responsible
//     for transitioning them, and a long-stuck IN_FLIGHT row is a bug worth
//     investigating — not data to silently drop.
//   - COORDINATOR rows in any status: ConsensusQueryOutcome reads them
//     authoritatively for late participant reconciliation. If we purged a
//     COMMITTED coordinator row and a participant later came back online with
//     a still-IN_FLIGHT row, the participant's reconciler would get
//     OUTCOME_UNSPECIFIED, fall through to the presumed-abort path, and roll
//     back a flow that actually committed.
//
// The role+status+update_time filter aligns with the existing
// (role, status, update_time) index on flow_executions so each batch lookup
// is a bounded index scan rather than a sequential scan.
//
// Each batch runs in its own DB transaction (committed inside the loop) so a
// week's accumulation can be drained over many small writes rather than one
// long-held lock.
func PurgeTerminalFlowExecutions(ctx context.Context, ttl time.Duration, batchSize int) error {
	cutoffTime := time.Now().Add(-ttl)
	logger := logging.GetLoggerFromContext(ctx)

	totalDeleted := 0
	for {
		db, err := ent.GetTxFromContext(ctx)
		if err != nil {
			return fmt.Errorf("failed to get current tx: %w", err)
		}

		idsToDelete, err := db.FlowExecution.Query().
			Where(
				flowexecution.RoleEQ(st.FlowExecutionRoleParticipant),
				flowexecution.StatusIn(
					st.FlowExecutionStatusCommitted,
					st.FlowExecutionStatusRolledBack,
				),
				flowexecution.UpdateTimeLT(cutoffTime),
			).
			Limit(batchSize).
			ForUpdate(sql.WithLockAction(sql.SkipLocked)).
			IDs(ctx)
		if err != nil {
			return fmt.Errorf("failed to query terminal flow_executions to purge: %w", err)
		}

		if len(idsToDelete) == 0 {
			break
		}

		deleted, err := db.FlowExecution.Delete().
			Where(flowexecution.IDIn(idsToDelete...)).
			Exec(ctx)
		if err != nil {
			return fmt.Errorf("failed to purge terminal flow_executions: %w", err)
		}
		totalDeleted += deleted

		if err := db.Commit(); err != nil {
			return fmt.Errorf("failed to commit batch: %w", err)
		}

		// Last query returned a partial batch — no more eligible rows are
		// visible from this transaction snapshot.
		if len(idsToDelete) < batchSize {
			break
		}
	}

	if totalDeleted > 0 {
		logger.Sugar().Infof("purge_terminal_flow_executions: deleted %d rows older than %s", totalDeleted, cutoffTime)
	}
	return nil
}
