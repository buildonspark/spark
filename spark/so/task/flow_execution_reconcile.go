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
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.uber.org/zap"
)

const (
	defaultFlowExecutionStuckThresholdSec            = 300
	defaultFlowExecutionCoordinatorStallThresholdSec = 600
	defaultFlowExecutionSweepBatchLimit              = 50
	// Default for KnobFlowExecutionMetricsMinAgeSeconds. Rows younger than
	// this don't appear in the in_flight gauges (still in gossip-retry
	// territory). 10 minutes is generous relative to the 20s gossip retry
	// interval so noisy "young IN_FLIGHT" rows don't reach dashboards.
	defaultFlowExecutionMetricsMinAgeSec = 600
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

	// Emit per-(role, op_type) gauges from the post-sweep state. Gated on a
	// knob (default off) and logged on failure but never returned — metric
	// emission must not abort the task.
	if r.metricsEnabled() {
		if err := r.emitInFlightGauges(ctx, db); err != nil {
			logger.With(zap.Error(err)).Warn("flow_execution reconcile: failed to emit in-flight gauges")
		}
	}
	return nil
}

// metricsEnabled reports whether flow_execution.* metric emission is on for
// this operator. Defaults to off so the feature can ship without spamming
// dashboards before alerting policies are in place; flipped on via
// KnobFlowExecutionMetricsEnabled when desired.
func (r *participantReconciler) metricsEnabled() bool {
	return r.knobs.GetValue(knobs.KnobFlowExecutionMetricsEnabled, 0) > 0
}

// recordReconciledOutcome increments flow_execution.reconciled_total with the
// given outcome label. No-op when metrics are disabled.
func (r *participantReconciler) recordReconciledOutcome(ctx context.Context, outcome string) {
	if !r.metricsEnabled() {
		return
	}
	flowExecutionReconciledTotal.Add(ctx, 1, metric.WithAttributes(attribute.String(outcomeAttribute, outcome)))
}

// inFlightAggregateRow is the schema scanned out of the per-role GROUP BY
// query that drives the gauge emission. JSON tags must match Ent's column
// aliases: "op_type" is the grouped field; "count" and "min" are the
// default aliases for ent.Count() and ent.Min(...). The role is fixed per
// query (not grouped) so it doesn't appear here.
type inFlightAggregateRow struct {
	OpType int32     `json:"op_type"`
	Count  int64     `json:"count"`
	Min    time.Time `json:"min"`
}

// emitInFlightGauges records flow_execution.in_flight_count and
// flow_execution.oldest_in_flight_age_ms per (role, op_type). Only rows
// older than KnobFlowExecutionMetricsMinAgeSeconds are counted — younger
// rows are expected to resolve via gossip retry and would just clutter
// dashboards. Groups with zero qualifying rows aren't emitted (they don't
// appear in the GROUP BY result); for gauge purposes "no data" carries the
// same meaning as "zero".
//
// The aggregation is issued as two per-role queries rather than one with a
// GROUP BY across roles. The (role, status, update_time) index requires
// role as the leading filter to be usable; a query that filters only by
// status+update_time and groups by role would fall back to a sequential
// scan of the IN_FLIGHT slice.
func (r *participantReconciler) emitInFlightGauges(ctx context.Context, db *ent.Client) error {
	minAgeSec := r.knobs.GetValue(knobs.KnobFlowExecutionMetricsMinAgeSeconds, float64(defaultFlowExecutionMetricsMinAgeSec))
	cutoff := time.Now().Add(-time.Duration(minAgeSec) * time.Second)
	now := time.Now()
	for _, role := range []st.FlowExecutionRole{st.FlowExecutionRoleCoordinator, st.FlowExecutionRoleParticipant} {
		var stats []inFlightAggregateRow
		if err := db.FlowExecution.Query().
			Where(
				flowexecution.RoleEQ(role),
				flowexecution.StatusEQ(st.FlowExecutionStatusInFlight),
				flowexecution.UpdateTimeLT(cutoff),
			).
			GroupBy(flowexecution.FieldOpType).
			Aggregate(ent.Count(), ent.Min(flowexecution.FieldUpdateTime)).
			Scan(ctx, &stats); err != nil {
			return fmt.Errorf("aggregate in-flight stats for role %s: %w", role, err)
		}
		for _, s := range stats {
			attrs := metric.WithAttributes(
				attribute.String("role", string(role)),
				attribute.Int("op_type", int(s.OpType)),
			)
			flowExecutionInFlightCountGauge.Record(ctx, s.Count, attrs)
			flowExecutionOldestInFlightAgeGauge.Record(ctx, now.Sub(s.Min).Milliseconds(), attrs)
		}
	}
	return nil
}

// outcomeAttribute is the attribute key used on flow_execution.reconciled_total.
const outcomeAttribute = "outcome"

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
		if err := r.gossipHandler.HandleGossipMessage(ctx, &pbgossip.GossipMessage{
			Message: &pbgossip.GossipMessage_ConsensusCommit{
				ConsensusCommit: &pbgossip.GossipMessageConsensusCommit{
					OpType:          pbgossip.ConsensusOperationType(resp.OpType),
					Operation:       resp.DecisionPayload,
					FlowExecutionId: row.ID.String(),
				},
			},
		}, false /* forCoordinator */); err != nil {
			return err
		}
		r.recordReconciledOutcome(ctx, "committed")
		return nil
	case pbinternal.ConsensusQueryOutcomeResponse_OUTCOME_ROLLED_BACK:
		if err := r.gossipHandler.HandleGossipMessage(ctx, &pbgossip.GossipMessage{
			Message: &pbgossip.GossipMessage_ConsensusRollback{
				ConsensusRollback: &pbgossip.GossipMessageConsensusRollback{
					OpType:          pbgossip.ConsensusOperationType(resp.OpType),
					Operation:       resp.DecisionPayload,
					FlowExecutionId: row.ID.String(),
				},
			},
		}, false /* forCoordinator */); err != nil {
			return err
		}
		r.recordReconciledOutcome(ctx, "rolled_back")
		return nil
	case pbinternal.ConsensusQueryOutcomeResponse_OUTCOME_IN_FLIGHT:
		// Coordinator still deciding. Leave the row IN_FLIGHT and try again next tick.
		r.recordReconciledOutcome(ctx, "in_flight")
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
		r.recordReconciledOutcome(ctx, "unspecified")
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
