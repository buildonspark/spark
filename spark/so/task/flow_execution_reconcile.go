package task

import (
	"context"
	"fmt"
	"time"

	"github.com/lightsparkdev/spark/common/logging"
	pbgossip "github.com/lightsparkdev/spark/proto/gossip"
	pbinternal "github.com/lightsparkdev/spark/proto/spark_internal"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/ent/flowexecution"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/handler"
	"github.com/lightsparkdev/spark/so/knobs"
	"go.uber.org/zap"
)

const (
	defaultFlowExecutionStuckThresholdSec            = 300
	defaultFlowExecutionCoordinatorStallThresholdSec = 600
	defaultFlowExecutionSweepBatchLimit              = 50
)

// outcomeQueryFunc calls the coordinator's ConsensusQueryOutcome RPC.
// Extracted as a function type so tests can inject a stub at the system
// boundary (network RPC) without spinning up a gRPC server. The gossip
// dispatch on the receiving side is an internal seam and runs for real in
// tests so the full participant-side commit/rollback path is exercised.
type outcomeQueryFunc func(ctx context.Context, operator *so.SigningOperator, flowExecutionID string) (*pbinternal.ConsensusQueryOutcomeResponse, error)

// participantReconciler owns the per-operator state for a reconciliation pass.
// Only the gRPC client is injectable (system boundary); the gossip handler
// runs for real.
type participantReconciler struct {
	config        *so.Config
	knobs         knobs.Knobs
	query         outcomeQueryFunc
	gossipHandler *handler.GossipHandler
}

// newParticipantReconciler wires the production gRPC path + real gossip handler.
func newParticipantReconciler(config *so.Config, knobsService knobs.Knobs) *participantReconciler {
	return &participantReconciler{
		config:        config,
		knobs:         knobsService,
		query:         defaultQueryOutcome,
		gossipHandler: handler.NewGossipHandler(config),
	}
}

// ReconcileStuckParticipantFlows is the scheduler-facing entry point. It
// finds PARTICIPANT FlowExecution rows that have been IN_FLIGHT past the
// configured threshold, queries each row's coordinator for the outcome, and
// dispatches the result through the normal gossip path. Failures on a single
// row are logged and skipped so one flaky coordinator connection can't stop
// the whole sweep.
func ReconcileStuckParticipantFlows(ctx context.Context, config *so.Config, knobsService knobs.Knobs) error {
	return newParticipantReconciler(config, knobsService).reconcile(ctx)
}

func (r *participantReconciler) reconcile(ctx context.Context) error {
	logger := logging.GetLoggerFromContext(ctx)
	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return fmt.Errorf("failed to get db: %w", err)
	}

	thresholdSec := r.knobs.GetValue(knobs.KnobFlowExecutionStuckThreshold, float64(defaultFlowExecutionStuckThresholdSec))
	batchLimit := int(r.knobs.GetValue(knobs.KnobFlowExecutionSweepBatchLimit, float64(defaultFlowExecutionSweepBatchLimit)))
	cutoff := time.Now().Add(-time.Duration(thresholdSec) * time.Second)

	// Order by update_time ASC so the oldest stuck rows are always
	// prioritized: if a few rows have a permanently-down coordinator and
	// keep failing, newer rows still get a turn once the older ones either
	// resolve or simply rotate out of the limit window.
	rows, err := db.FlowExecution.Query().
		Where(
			flowexecution.RoleEQ(st.FlowExecutionRoleParticipant),
			flowexecution.StatusEQ(st.FlowExecutionStatusInFlight),
			flowexecution.UpdateTimeLT(cutoff),
		).
		Order(ent.Asc(flowexecution.FieldUpdateTime)).
		Limit(batchLimit).
		All(ctx)
	if err != nil {
		return fmt.Errorf("failed to query stuck participant rows: %w", err)
	}

	for _, row := range rows {
		if err := r.reconcileOne(ctx, row); err != nil {
			logger.With(zap.Error(err)).Sugar().Warnf(
				"flow_execution reconcile: failed to resolve %s (coordinator=%d, op_type=%d)",
				row.ID, row.CoordinatorIndex, row.OpType)
		}
	}
	return nil
}

func (r *participantReconciler) reconcileOne(ctx context.Context, row *ent.FlowExecution) error {
	logger := logging.GetLoggerFromContext(ctx)

	operator, err := r.config.GetOperatorByID(uint64(row.CoordinatorIndex))
	if err != nil {
		return fmt.Errorf("resolve coordinator %d: %w", row.CoordinatorIndex, err)
	}

	resp, err := r.query(ctx, operator, row.ID.String())
	if err != nil {
		return fmt.Errorf("ConsensusQueryOutcome for %s: %w", row.ID, err)
	}

	switch resp.Outcome {
	case pbinternal.ConsensusQueryOutcomeResponse_OUTCOME_COMMITTED:
		return r.gossipHandler.HandleGossipMessage(ctx, &pbgossip.GossipMessage{
			Message: &pbgossip.GossipMessage_ConsensusCommit{
				ConsensusCommit: &pbgossip.GossipMessageConsensusCommit{
					OpType:          pbgossip.ConsensusOperationType(resp.OpType),
					Operation:       resp.DecisionPayload,
					FlowExecutionId: row.ID.String(),
				},
			},
		}, false /* forCoordinator */)
	case pbinternal.ConsensusQueryOutcomeResponse_OUTCOME_ROLLED_BACK:
		return r.gossipHandler.HandleGossipMessage(ctx, &pbgossip.GossipMessage{
			Message: &pbgossip.GossipMessage_ConsensusRollback{
				ConsensusRollback: &pbgossip.GossipMessageConsensusRollback{
					OpType:          pbgossip.ConsensusOperationType(resp.OpType),
					Operation:       resp.DecisionPayload,
					FlowExecutionId: row.ID.String(),
				},
			},
		}, false /* forCoordinator */)
	case pbinternal.ConsensusQueryOutcomeResponse_OUTCOME_IN_FLIGHT:
		// Coordinator still deciding. Leave the row IN_FLIGHT and try again next tick.
		return nil
	case pbinternal.ConsensusQueryOutcomeResponse_OUTCOME_UNSPECIFIED:
		// Under normal operation the coordinator writes its row before fan-out
		// and retains it through the terminal transition. Seeing UNSPECIFIED
		// here means the coordinator has no record — most likely data loss.
		// Leave the row IN_FLIGHT; operator attention is needed. Next sweep
		// tick will retry in case the condition is transient.
		logger.Sugar().Errorf(
			"flow_execution reconcile: coordinator %d has no record of %s (op_type=%d); possible data loss",
			row.CoordinatorIndex, row.ID, row.OpType)
		return nil
	default:
		return fmt.Errorf("unexpected outcome %v for %s", resp.Outcome, row.ID)
	}
}

// defaultQueryOutcome issues the ConsensusQueryOutcome RPC to a remote
// coordinator. Split out from reconcileOne so tests can inject a stub via
// participantReconciler.query.
func defaultQueryOutcome(ctx context.Context, operator *so.SigningOperator, flowExecutionID string) (*pbinternal.ConsensusQueryOutcomeResponse, error) {
	conn, err := operator.NewOperatorGRPCConnection()
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	client := pbinternal.NewSparkInternalServiceClient(conn)
	return client.ConsensusQueryOutcome(ctx, &pbinternal.ConsensusQueryOutcomeRequest{
		FlowExecutionId: flowExecutionID,
	})
}

// SweepStaleCoordinatorFlows transitions COORDINATOR rows that have been
// IN_FLIGHT past the configured stall threshold to ROLLED_BACK. The
// decision_payload column was pre-populated with the rollback bytes at row
// creation (see TwoPCEngine.Execute), so no payload update is needed — the
// row is now serviceable by ConsensusQueryOutcome as ROLLED_BACK with the
// correct rollback payload.
//
// This is the presumed-abort path for the case where the coordinator crashed
// between Prepare fan-out and the commit/rollback decision. Participants
// reconciling against this coordinator will now get a real rollback outcome
// instead of being stuck awaiting a decision that will never come.
func SweepStaleCoordinatorFlows(ctx context.Context, _ *so.Config, knobsService knobs.Knobs) error {
	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return fmt.Errorf("failed to get db: %w", err)
	}
	thresholdSec := knobsService.GetValue(knobs.KnobFlowExecutionCoordinatorStallThreshold, float64(defaultFlowExecutionCoordinatorStallThresholdSec))
	batchLimit := int(knobsService.GetValue(knobs.KnobFlowExecutionSweepBatchLimit, float64(defaultFlowExecutionSweepBatchLimit)))
	cutoff := time.Now().Add(-time.Duration(thresholdSec) * time.Second)

	// Cap blast radius: pick the oldest batchLimit IDs first, then UPDATE
	// only those rows. An unbounded UPDATE would hold many row locks in a
	// single statement during a mass-stuck recovery; this fans the work
	// across sweep ticks and lets newer rows rotate in once the oldest
	// batch is processed.
	ids, err := db.FlowExecution.Query().
		Where(
			flowexecution.RoleEQ(st.FlowExecutionRoleCoordinator),
			flowexecution.StatusEQ(st.FlowExecutionStatusInFlight),
			flowexecution.UpdateTimeLT(cutoff),
		).
		Order(ent.Asc(flowexecution.FieldUpdateTime)).
		Limit(batchLimit).
		IDs(ctx)
	if err != nil {
		return fmt.Errorf("failed to query stale coordinator rows: %w", err)
	}
	if len(ids) == 0 {
		return nil
	}

	_, err = db.FlowExecution.Update().
		Where(
			flowexecution.IDIn(ids...),
			// Re-assert the status filter so a row that was concurrently
			// transitioned (e.g., a slow coordinator finally committed) is
			// not clobbered.
			flowexecution.StatusEQ(st.FlowExecutionStatusInFlight),
		).
		SetStatus(st.FlowExecutionStatusRolledBack).
		Save(ctx)
	if err != nil {
		return fmt.Errorf("failed to sweep stale coordinator rows: %w", err)
	}
	return nil
}
