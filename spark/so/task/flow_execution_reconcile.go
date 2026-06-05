package task

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
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
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
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
	// Minimum age before a participant row whose coordinator returns
	// UNSPECIFIED is presumed-aborted and rolled back locally. Set well
	// above defaultFlowExecutionCoordinatorStallThresholdSec so a
	// coordinator that's still in mid-decision (and hasn't yet been swept
	// to ROLLED_BACK by SweepStaleCoordinatorFlows) is never raced — by
	// 1200s, either the coordinator's row exists with a real outcome or it
	// truly never persisted (request tx aborted), making presumed-abort
	// the correct call.
	defaultFlowExecutionPresumedAbortAgeSec = 1200
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

	recoveryEnabled := r.recoveryEnabled()
	for _, row := range rows {
		if !recoveryEnabled {
			logger.Sugar().Infof(
				"flow_execution reconcile (monitor-only): stuck PARTICIPANT row %s coordinator=%d op_type=%s age=%s — recovery disabled by knob",
				row.ID, row.CoordinatorIndex, pbgossip.ConsensusOperationType(row.OpType), time.Since(row.UpdateTime))
			continue
		}
		if err := r.reconcileOne(ctx, row); err != nil {
			logger.With(zap.Error(err)).Sugar().Warnf(
				"flow_execution reconcile: failed to resolve %s (coordinator=%d, op_type=%s)",
				row.ID, row.CoordinatorIndex, pbgossip.ConsensusOperationType(row.OpType))
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

	// Surface the existence of stuck rows as a task-level error so the
	// scheduler's failure-logging pipeline picks it up and routes it to
	// Slack. The error fires whether or not we successfully reconciled
	// the rows this tick — the goal is to alert that participant-side
	// flows reached the stuck threshold at all (which they shouldn't,
	// once the engine cleanup-ctx fix is in place), so on-call can
	// investigate the underlying cause rather than wait for follow-on
	// effects.
	//
	// CRITICAL: commit the reconcile loop's recovery work BEFORE
	// returning the alert error. DatabaseMiddleware only commits the
	// task's session tx when the task returns nil — a non-nil return
	// triggers the deferred rollback, which would undo every
	// markParticipantFlowExecutionTerminal write the gossip dispatch
	// path just persisted. Without this commit the alert would fire
	// every tick on the same rows forever and never actually drain.
	if len(rows) > 0 {
		// Count must run BEFORE DbCommit — once the session's tx is
		// committed, queries through the same tx-bound client fail.
		// Counts the unrecovered backlog (rows still IN_FLIGHT after
		// the reconcile loop) so the alert reflects what's left to
		// drain, not the pre-recovery snapshot.
		totalCount, countErr := countStuckParticipantRows(ctx, db, cutoff)
		if countErr != nil {
			// Fall back to batch length — degraded alert is better
			// than no alert. Log so operators know the count is
			// approximate.
			logger.With(zap.Error(countErr)).Warn("flow_execution reconcile: failed to count total stuck rows; alert message uses batch size")
			totalCount = int64(len(rows))
		}
		if commitErr := ent.DbCommit(ctx); commitErr != nil {
			return fmt.Errorf("commit recovery work before alert: %w", commitErr)
		}
		return stuckFlowExecutionError("PARTICIPANT", rows, totalCount)
	}
	return nil
}

// countStuckParticipantRows / countStuckCoordinatorRows return the true
// total of rows past the stuck threshold so the alert message can reflect
// actual scale even when the batch-limited recovery query returns only the
// first N. Without this an unrelenting backlog would always look like
// exactly batchLimit (default 50), masking 5×/10×-larger problems.
func countStuckParticipantRows(ctx context.Context, db *ent.Client, cutoff time.Time) (int64, error) {
	c, err := db.FlowExecution.Query().
		Where(
			flowexecution.RoleEQ(st.FlowExecutionRoleParticipant),
			flowexecution.StatusEQ(st.FlowExecutionStatusInFlight),
			flowexecution.UpdateTimeLT(cutoff),
		).
		Count(ctx)
	return int64(c), err
}

func countStuckCoordinatorRows(ctx context.Context, db *ent.Client, cutoff time.Time) (int64, error) {
	c, err := db.FlowExecution.Query().
		Where(
			flowexecution.RoleEQ(st.FlowExecutionRoleCoordinator),
			flowexecution.StatusEQ(st.FlowExecutionStatusInFlight),
			flowexecution.UpdateTimeLT(cutoff),
		).
		Count(ctx)
	return int64(c), err
}

// stuckFlowExecutionError formats the task-level error returned by
// reconcile() and SweepStaleCoordinatorFlows() when stuck rows are
// found. Recovery work has already been committed before this fires
// (see the explicit DbCommit in each task body), but the existence of
// stuck rows is itself alert-worthy — the error propagates up through
// the task middleware so the scheduler's LogMiddleware logs it at
// ERROR and Slack fans the notification out. Manual TriggerTask
// callers see the same error as codes.Internal carrying the alert
// message.
//
// Format: "found <N>[/<total> (batch limit reached)] stuck <ROLE>
// flow_execution rows: {id=… op_type=… age=…}, … [+M more in batch]".
// Sample size capped at 3; rows beyond that show as "(+M more in
// batch)". When the batch-limited query returned fewer rows than the
// uncapped count, the message includes "<batch>/<total>" so on-call
// sees true backlog scale.
func stuckFlowExecutionError(role string, rows []*ent.FlowExecution, totalCount int64) error {
	const sampleLimit = 3
	sample := rows
	if len(sample) > sampleLimit {
		sample = sample[:sampleLimit]
	}
	parts := make([]string, 0, len(sample))
	for _, r := range sample {
		parts = append(parts, fmt.Sprintf("{id=%s op_type=%s age=%s}",
			r.ID,
			pbgossip.ConsensusOperationType(r.OpType),
			time.Since(r.UpdateTime).Truncate(time.Second)))
	}
	more := ""
	if len(rows) > sampleLimit {
		more = fmt.Sprintf(" (+%d more in batch)", len(rows)-sampleLimit)
	}
	scale := fmt.Sprintf("%d", len(rows))
	if totalCount > int64(len(rows)) {
		scale = fmt.Sprintf("%d/%d (batch limit reached)", len(rows), totalCount)
	}
	return fmt.Errorf("found %s stuck %s flow_execution rows: %s%s", scale, role, strings.Join(parts, ", "), more)
}

// recoveryEnabled reports whether the active recovery path (gossip dispatch
// for participant rows, ROLLED_BACK transition for coordinator rows) is on.
// Defaults to off so operators can run the tasks in monitor-only mode and
// inspect what would be touched before authorizing mutations.
func (r *participantReconciler) recoveryEnabled() bool {
	return r.knobs.GetValue(knobs.KnobFlowExecutionReconcileEnabled, 0) > 0
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

	switch resp.GetOutcome() {
	case pbinternal.ConsensusQueryOutcomeResponse_OUTCOME_COMMITTED:
		if err := r.gossipHandler.HandleGossipMessage(ctx, &pbgossip.GossipMessage{
			Message: &pbgossip.GossipMessage_ConsensusCommit{
				ConsensusCommit: &pbgossip.GossipMessageConsensusCommit{
					OpType:          pbgossip.ConsensusOperationType(resp.GetOpType()),
					Operation:       resp.GetDecisionPayload(),
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
					OpType:          pbgossip.ConsensusOperationType(resp.GetOpType()),
					Operation:       resp.GetDecisionPayload(),
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
		// and retains it through the terminal transition. UNSPECIFIED means
		// the coordinator has no record — almost always because its request
		// tx aborted before commit (e.g., the gRPC client cancelled mid-flow
		// after Prepare fan-out succeeded). Without recovery the participant
		// is stuck IN_FLIGHT forever with locked resources.
		//
		// Recovery: if the row carries the persisted prepare op (rows
		// created before this field shipped won't), and it's been IN_FLIGHT
		// long enough that the coordinator's self-sweep would have written
		// a real ROLLED_BACK outcome had the row existed, presume-abort —
		// synthesize a local rollback gossip using the persisted prepare op
		// and dispatch through the same handler the real rollback path uses.
		// dispatchPresumedAbort returns true iff it actually fired.
		fired, err := r.dispatchPresumedAbort(ctx, row)
		if err != nil {
			return err
		}
		if fired {
			logger.Sugar().Warnf(
				"flow_execution reconcile: presumed-abort dispatched for %s (coordinator=%d, op_type=%d, age=%s); coordinator had no record",
				row.ID, row.CoordinatorIndex, row.OpType, time.Since(row.UpdateTime))
			r.recordReconciledOutcome(ctx, "presumed_abort")
			return nil
		}
		logger.Sugar().Errorf(
			"flow_execution reconcile: coordinator %d has no record of %s (op_type=%d); possible data loss",
			row.CoordinatorIndex, row.ID, row.OpType)
		r.recordReconciledOutcome(ctx, "unspecified")
		return nil
	default:
		return fmt.Errorf("unexpected outcome %v for %s", resp.GetOutcome(), row.ID)
	}
}

// dispatchPresumedAbort fires a synthesized local rollback for a PARTICIPANT
// row whose coordinator returned UNSPECIFIED. Returns (true, nil) when it
// dispatched, (false, nil) when the row is too young or has no persisted
// prepare op, and (false, err) on a fatal error.
//
// The rollback gossip is dispatched through gossipHandler.HandleGossipMessage
// so it goes through the same code path as a real rollback message — runs
// FlowHandler.Rollback to release locked resources, then transitions the row
// to ROLLED_BACK. No second sweep tick is required.
func (r *participantReconciler) dispatchPresumedAbort(ctx context.Context, row *ent.FlowExecution) (bool, error) {
	// Ent represents a SQL NULL bytea as either a nil pointer OR a
	// non-nil pointer to an empty slice depending on driver/version, so
	// both have to be treated as "no payload persisted" — otherwise a
	// legacy row would slip past this guard and fail later on
	// proto.Unmarshal of zero bytes. Mirrors the existing
	// decision_payload guard in consensus_query_handler.go.
	if row.PreparePayload == nil || len(*row.PreparePayload) == 0 {
		// Row predates the prepare_payload column (or stored NULL); can't
		// synthesize. Fall through to the legacy log path so on-call
		// still gets the signal.
		return false, nil
	}
	if time.Since(row.UpdateTime) < time.Duration(defaultFlowExecutionPresumedAbortAgeSec)*time.Second {
		// Too young — leave IN_FLIGHT and try again next tick. Guards
		// against firing while the coordinator is genuinely mid-decision.
		return false, nil
	}
	op := &anypb.Any{}
	if err := proto.Unmarshal(*row.PreparePayload, op); err != nil {
		return false, fmt.Errorf("unmarshal prepare_payload for %s: %w", row.ID, err)
	}
	msg := &pbgossip.GossipMessage{
		// Stable, row-derived id so the "Handling gossip message with
		// ID …" log line emitted by the gossip handler ties back to
		// the participant row being recovered. The "presumed-abort-"
		// prefix also disambiguates these from real coordinator-sent
		// rollbacks in dashboards.
		MessageId: "presumed-abort-" + row.ID.String(),
		Message: &pbgossip.GossipMessage_ConsensusRollback{
			ConsensusRollback: &pbgossip.GossipMessageConsensusRollback{
				OpType:          pbgossip.ConsensusOperationType(row.OpType),
				Operation:       op,
				FlowExecutionId: row.ID.String(),
			},
		},
	}
	if err := r.gossipHandler.HandleGossipMessage(ctx, msg, false /* forCoordinator */); err != nil {
		return false, fmt.Errorf("dispatch presumed-abort rollback for %s: %w", row.ID, err)
	}
	return true, nil
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
	logger := logging.GetLoggerFromContext(ctx)
	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return fmt.Errorf("failed to get db: %w", err)
	}
	thresholdSec := knobsService.GetValue(knobs.KnobFlowExecutionCoordinatorStallThreshold, float64(defaultFlowExecutionCoordinatorStallThresholdSec))
	batchLimit := int(knobsService.GetValue(knobs.KnobFlowExecutionSweepBatchLimit, float64(defaultFlowExecutionSweepBatchLimit)))
	cutoff := time.Now().Add(-time.Duration(thresholdSec) * time.Second)

	// Cap blast radius: pick the oldest batchLimit rows first, then UPDATE
	// only those. An unbounded UPDATE would hold many row locks in a single
	// statement during a mass-stuck recovery; this fans the work across
	// sweep ticks and lets newer rows rotate in once the oldest batch is
	// processed. Fetch full rows (not just IDs) so monitor-only mode can
	// log op_type and age per row.
	rows, err := db.FlowExecution.Query().
		Where(
			flowexecution.RoleEQ(st.FlowExecutionRoleCoordinator),
			flowexecution.StatusEQ(st.FlowExecutionStatusInFlight),
			flowexecution.UpdateTimeLT(cutoff),
		).
		Order(ent.Asc(flowexecution.FieldUpdateTime)).
		Limit(batchLimit).
		All(ctx)
	if err != nil {
		return fmt.Errorf("failed to query stale coordinator rows: %w", err)
	}
	if len(rows) == 0 {
		return nil
	}

	if knobsService.GetValue(knobs.KnobFlowExecutionReconcileEnabled, 0) <= 0 {
		for _, row := range rows {
			logger.Sugar().Infof(
				"flow_execution sweep (monitor-only): stale COORDINATOR row %s op_type=%s age=%s — recovery disabled by knob",
				row.ID, pbgossip.ConsensusOperationType(row.OpType), time.Since(row.UpdateTime))
		}
		// Even in monitor-only mode, surface the existence of stale
		// rows so the alerting pipeline picks it up. No tx work was
		// done so no commit is needed; the count can run on the
		// session client (still alive) before we return.
		totalCount, countErr := countStuckCoordinatorRows(ctx, db, cutoff)
		if countErr != nil {
			logger.With(zap.Error(countErr)).Warn("flow_execution sweep: failed to count total stale rows; alert message uses batch size")
			totalCount = int64(len(rows))
		}
		return stuckFlowExecutionError("COORDINATOR", rows, totalCount)
	}

	ids := make([]uuid.UUID, len(rows))
	for i, row := range rows {
		ids[i] = row.ID
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
	// Count the post-recovery backlog BEFORE committing — after the
	// commit the session's tx-bound client is dead and queries through
	// it fail. After this UPDATE the count reflects whatever's still
	// IN_FLIGHT after this tick's batch was transitioned.
	totalCount, countErr := countStuckCoordinatorRows(ctx, db, cutoff)
	if countErr != nil {
		logger.With(zap.Error(countErr)).Warn("flow_execution sweep: failed to count total stale rows; alert message uses batch size")
		totalCount = int64(len(rows))
	}
	// CRITICAL: commit the bulk transition before surfacing the alert
	// error. DatabaseMiddleware's deferred rollback fires whenever the
	// task returns non-nil, which would undo the SetStatus work above
	// and leave the rows IN_FLIGHT for the next tick to re-alert on.
	if commitErr := ent.DbCommit(ctx); commitErr != nil {
		return fmt.Errorf("commit recovery work before alert: %w", commitErr)
	}
	// Surface the existence of stale rows as a task-level error so the
	// scheduler's failure-logging pipeline routes it to Slack.
	return stuckFlowExecutionError("COORDINATOR", rows, totalCount)
}
