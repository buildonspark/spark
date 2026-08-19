package consensus

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/logging"
	pbgossip "github.com/lightsparkdev/spark/proto/gossip"
	pbinternal "github.com/lightsparkdev/spark/proto/spark_internal"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/ent/flowexecution"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/helper"
	"go.uber.org/zap"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
)

// engineCleanupTimeout caps how long any single engine bookkeeping phase
// (createCoordinatorRow, markRolledBack, commit/rollback gossip dispatch) is
// allowed to run. Each call to inEngineSession derives a fresh WithTimeout from
// this value, so a long Prepare or BuildCommitPayload doesn't burn the
// cleanup-phase budget — the post-decision gossip path always gets the full
// window to drive participants to a terminal outcome, regardless of how long
// the request-cancellable phases took. (recordCommitDecision is the exception:
// it rides the request tx, not an engine session.)
const engineCleanupTimeout = 60 * time.Second

// ErrCoordinatorRowPreempted is surfaced by Execute when the coordinator's
// FlowExecution row was transitioned out of IN_FLIGHT (most likely
// presumed-aborted to ROLLED_BACK by SweepStaleCoordinatorFlows) before the
// engine recorded its commit decision. This is no longer a divergence: because
// the decision is written in the request tx and committed atomically with the
// coordinator's domain work, a preemption means the request tx is rolled back
// and the coordinator converges with the participants on rolled-back. The
// error signals that benign coordinated rollback to the caller.
var ErrCoordinatorRowPreempted = errors.New("coordinator FlowExecution row was preempted before recording the commit decision (likely swept to ROLLED_BACK by the self-sweep task); request tx rolled back")

// TwoPCEngine orchestrates consensus using two-phase commit.
//
// The coordinator calls Execute with a CoordinatorFlow to run the full lifecycle:
//  1. Create a FlowExecution row pre-populated with the rollback payload.
//  2. Prepare: synchronous fan-out of flow.PrepareTask via ExecuteTaskWithAllOperators,
//     passing the row's id as flow_execution_id so participants can create their own
//     rows with the same id on their own databases.
//  3. BuildCommitPayload: coordinator builds the commit payload from prepare results.
//  4. On success, record the COMMITTED decision (overwriting decision_payload with
//     the commit bytes) in the request tx and DbCommit it atomically with the
//     coordinator's domain work; on failure/abort, transition the row to
//     ROLLED_BACK in a detached engine session.
//  5. Commit or Rollback: durable async delivery via gossip, carrying the row's id.
//
// Because decision_payload is written at row creation with the rollback bytes,
// the row always holds a usable payload: if the coordinator crashes mid-flow,
// the self-sweep task transitions IN_FLIGHT → ROLLED_BACK and the already-populated
// rollback payload is served to reconciling participants via ConsensusQueryOutcome.
//
// On the receiving side, incoming ConsensusCommit/ConsensusRollback gossip
// messages are dispatched to FlowHandler methods by the gossip handler via a
// switch on ConsensusOperationType.
type TwoPCEngine struct {
	config *so.Config
	gossip GossipSender
	// sessionFactory mints a db.Session per engine bookkeeping phase
	// (createCoordinatorRow, markRolledBack, gossip
	// dispatch). The engine session is bound to a detached cleanup
	// context so the session — and its transaction — survive a
	// user-cancelled request. Sharing the SessionFactory abstraction
	// with the gRPC database middleware means engine writes go through
	// exactly the same Begin/Save/Commit machinery as request-tx writes
	// (notification flush hooks, panic-recovery rollback, metric
	// attribution, lazy tx-begin), with only the lifecycle differing.
	sessionFactory db.SessionFactory
	// commitCoordinatorTx commits the coordinator's request transaction — the
	// atomic point of no return that persists the COMMITTED decision together
	// with the coordinator's domain work. A field (defaulted to ent.DbCommit)
	// so tests can inject the ambiguous outcome where the tx durably applies
	// but Commit reports an error (lost ack from a DB failover, killed
	// connection, or request-ctx cancellation mid-commit).
	commitCoordinatorTx func(ctx context.Context) error
	// readBackDecisionStatus re-reads the coordinator row's durable status on a
	// fresh connection to resolve an ambiguous commit error. A field (defaulted
	// to the real reader) for the same testing reason as commitCoordinatorTx.
	readBackDecisionStatus func(ctx context.Context, id uuid.UUID) (st.FlowExecutionStatus, error)
}

// NewTwoPCEngine creates a TwoPCEngine backed by synchronous operator
// fan-out for prepare and gossip for commit/rollback.
//
// sessionFactory provides per-engine-call db sessions used for
// transactional bookkeeping writes that must outlive a user-cancelled
// request. The production engine is constructed once at server init
// (where the dbClient already lives) and shared across requests via the
// ConsensusEngineInterceptor; handlers fetch it through
// consensus.GetEngine(ctx). Tests construct an engine directly per test.
func NewTwoPCEngine(config *so.Config, gossip GossipSender, sessionFactory db.SessionFactory) *TwoPCEngine {
	e := &TwoPCEngine{
		config:              config,
		gossip:              gossip,
		sessionFactory:      sessionFactory,
		commitCoordinatorTx: ent.DbCommit,
	}
	e.readBackDecisionStatus = e.readBackDecisionStatusFromDB
	return e
}

// Execute runs the full two-phase commit lifecycle for a consensus operation.
//
// See the TwoPCEngine doc comment for the full lifecycle.
//
// If commit gossip fails after a successful prepare, Execute does not attempt
// a rollback. The gossip system persists the record to DB before network
// delivery, so the background retry task will eventually deliver it. Sending a
// competing rollback would create two conflicting gossip records.
//
// On success, returns the commit payload so the coordinator can use it to build
// its RPC response.
func (e *TwoPCEngine) Execute(
	ctx context.Context,
	opType pbgossip.ConsensusOperationType,
	selection *helper.OperatorSelection,
	flow CoordinatorFlow,
) (proto.Message, error) {
	logger := logging.GetLoggerFromContext(ctx)

	// detachedCtx carries the same values as ctx (logger, request_id,
	// etc., for log correlation) but is not propagated cancellation.
	// Each engine bookkeeping phase derives its own WithTimeout from
	// this base inside inEngineSession — so a long Prepare doesn't
	// burn the cleanup-phase budget. Without WithoutCancel here, a
	// user-cancelled request would strand participants in IN_FLIGHT.
	detachedCtx := context.WithoutCancel(ctx)

	participants, err := selection.OperatorIdentifierList(e.config)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve participants: %w", err)
	}

	row, err := e.createCoordinatorRow(detachedCtx, opType, flow)
	if err != nil {
		return nil, fmt.Errorf("failed to create FlowExecution row: %w", err)
	}
	executionID := row.ID.String()

	// Wrap prepareTask: remote operators use DefaultPrepareTask (gRPC),
	// self uses flow.Prepare locally to avoid deadlock.
	// Both return proto.Message which is marshaled into *anypb.Any for the results map.
	//
	// NOTE: the prepare task uses the user-cancellable ctx (not detachedCtx)
	// — coordinator's own flow.Prepare must run in the request transaction
	// so its domain work (e.g. locking a TreeNode) is tied to request
	// success, and remote peers must observe a fresh client cancel as
	// quickly as possible to avoid wasted work.
	prepareTask := func(ctx context.Context, operator *so.SigningOperator) (*anypb.Any, error) {
		var result proto.Message
		var err error
		if operator.Identifier == e.config.Identifier {
			result, err = flow.Prepare(ContextWithFlowExecutionID(ctx, row.ID), flow.PrepareOp())
		} else {
			result, err = DefaultPrepareTask(ctx, operator, opType, flow.PrepareOp(), executionID, uint32(row.CoordinatorIndex))
		}
		if err != nil {
			return nil, err
		}
		if result == nil {
			return nil, nil
		}
		return anypb.New(result)
	}

	logger.Sugar().Infof("2PC prepare: starting fan-out for op type %d to %d participants", opType, len(participants))
	results, err := helper.ExecuteTaskWithAllOperators(ctx, e.config, selection, prepareTask)
	if err != nil {
		logger.Sugar().Infof("2PC prepare: failed for op type %d, sending rollback", opType)
		e.attemptRollback(detachedCtx, row, opType, flow, executionID, participants)
		return nil, fmt.Errorf("prepare failed: %w", err)
	}
	logger.Sugar().Infof("2PC prepare: all %d participants ready for op type %d", len(participants), opType)

	commitOp, err := flow.BuildCommitPayload(ctx, results)
	if err != nil {
		logger.Sugar().Infof("2PC build-commit: failed for op type %d, sending rollback", opType)
		e.attemptRollback(detachedCtx, row, opType, flow, executionID, participants)
		return nil, fmt.Errorf("build-commit failed: %w", err)
	}

	// Write the commit decision (COMMITTED + commit payload) into the
	// coordinator's FlowExecution row through the REQUEST transaction —
	// the same tx that holds the coordinator's domain work (FlowHandler.Prepare
	// / BuildCommitPayload write coordinator-side domain state through the
	// request session: preimage_shares for StorePreimageShareV2, new tree
	// nodes for FinalizeDepositTreeCreation, sender/receiver key tweaks for
	// transfers, etc.). Committing the decision and the domain work in one
	// DbCommit makes them atomic, which is what eliminates the divergence:
	// there is no durable state in which the domain is committed but the
	// decision is still IN_FLIGHT, so a self-sweep firing concurrently can
	// never strand a committed coordinator against rolled-back peers.
	//
	// The decision write is a conditional update (status = IN_FLIGHT). If a
	// concurrent SweepStaleCoordinatorFlows already transitioned the row to
	// ROLLED_BACK it matches zero rows (preempted): we must NOT commit the
	// request tx — returning here lets the middleware roll it back, so the
	// coordinator's domain work is discarded and both sides converge on
	// rolled-back. The two writers serialize on the row lock, so exactly one
	// of {decision UPDATE, sweep UPDATE} wins; either outcome is consistent.
	//
	// Trade-off: the decision now rides the request tx rather than a detached
	// engine session, so a request-ctx cancellation before DbCommit rolls the
	// whole flow back. That is intentional — recovery here is roll-back only;
	// preserving a fully-prepared flow across a coordinator crash (roll-forward)
	// is deliberately out of scope (see SP-3195).
	preempted, err := e.recordCommitDecision(ctx, row, commitOp)
	if err != nil {
		logger.With(zap.Error(err)).Sugar().Infof(
			"2PC commit: recording decision failed for op type %d, sending rollback", opType)
		// The request tx is doomed; roll it back now to release its locks
		// (flow.Prepare's leaf/node ForUpdate locks, the decision CAS) before the
		// detached rollback bookkeeping, rather than holding them until the
		// middleware rolls back after Execute returns.
		_ = ent.DbRollback(ctx)
		e.attemptRollback(detachedCtx, row, opType, flow, executionID, participants)
		return nil, fmt.Errorf("failed to record commit decision: %w", err)
	}
	if preempted {
		// The self-sweep transitioned the row to ROLLED_BACK before we
		// recorded the decision. The request tx (with the coordinator's
		// domain work) is doomed; roll it back now to release its locks so the
		// coordinator converges with the participants on rolled-back — a benign
		// coordinated rollback, not a divergence. Rolling back before
		// attemptRollback also frees the row so the detached markRolledBack CAS
		// isn't contending with the request tx's own locks. Dispatch rollback
		// gossip so peers don't wait for the reconciler to drive them there.
		logger.Sugar().Warnf(
			"2PC commit: coordinator row preempted by sweep for op type %d, rolling back", opType)
		_ = ent.DbRollback(ctx)
		e.attemptRollback(detachedCtx, row, opType, flow, executionID, participants)
		return nil, fmt.Errorf("commit preempted: %w", ErrCoordinatorRowPreempted)
	}

	// Atomic point of no return: commits the coordinator's domain work and the
	// COMMITTED decision together. A commit error is NOT proof of abort (see
	// resolveAmbiguousCommit), so it is resolved from durable state rather than
	// assumed to be a rollback.
	if commitErr := e.commitCoordinatorTx(ctx); commitErr != nil {
		proceed, err := e.resolveAmbiguousCommit(ctx, detachedCtx, row, opType, flow, executionID, participants, commitErr)
		if !proceed {
			return nil, err
		}
		// proceed == true: the tx durably committed despite the error; fall
		// through to the commit-gossip path exactly as the clean-success case.
	}

	logger.Sugar().Infof("2PC commit: sending gossip for op type %d to %d participants", opType, len(participants))
	if err := e.commit(detachedCtx, opType, commitOp, executionID, participants); err != nil {
		logger.With(zap.Error(err)).Sugar().Errorf(
			"failed to send consensus commit gossip for op type %d", opType)
		return nil, fmt.Errorf("commit gossip failed: %w", err)
	}
	logger.Sugar().Infof("2PC commit: complete for op type %d", opType)
	return commitOp, nil
}

// resolveAmbiguousCommit disambiguates a request-tx commit error and drives the
// flow to a terminal outcome.
//
// A commit error is NOT proof of abort. PostgreSQL can durably apply the COMMIT
// and then lose the acknowledgement — a DB failover, a killed connection, or the
// request ctx being cancelled mid-commit (the commit rides the request ctx,
// unlike the detached bookkeeping) all surface the same error whether the tx
// aborted or committed-then-lost-its-ack. Treating the error as abort and
// rolling back would strand a committed coordinator against participants driven
// to ROLLED_BACK, with no path to reconcile (the reconciler only re-checks
// IN_FLIGHT rows).
//
// The COMMITTED decision was written inside the request tx (recordCommitDecision)
// while the row's IN_FLIGHT baseline was committed separately at row creation, so
// a fresh-connection read of the row's status is authoritative: COMMITTED means
// the tx applied; anything else means it did not.
//
// Returns proceed=true only when the decision is durably COMMITTED — whether the
// read-back saw it directly or the rollback CAS discovered it — and the caller
// must then dispatch commit gossip. For the aborted and unresolvable cases it
// performs the terminal action itself (rollback dispatch, or nothing) and returns
// proceed=false with the error to propagate.
func (e *TwoPCEngine) resolveAmbiguousCommit(
	ctx, detachedCtx context.Context,
	row *ent.FlowExecution,
	opType pbgossip.ConsensusOperationType,
	flow CoordinatorFlow,
	executionID string,
	participants []string,
	commitErr error,
) (proceed bool, err error) {
	logger := logging.GetLoggerFromContext(ctx)
	status, readErr := e.readBackDecisionStatus(detachedCtx, row.ID)
	switch {
	case readErr != nil:
		// Outcome genuinely unknown. Send NO gossip: a wrong rollback over a
		// durably-committed decision is unrecoverable, whereas silence is safe —
		// the participant reconciler drives peers to this coordinator's true
		// outcome via ConsensusQueryOutcome once the coordinator is reachable
		// again. (Trade-off: participants stay IN_FLIGHT with resources locked
		// until the reconciler/self-sweep window elapses; that bounded delay is
		// preferred over an unrecoverable wrong rollback.)
		logger.With(zap.Error(commitErr)).Sugar().Errorf(
			"2PC commit: request tx commit errored and decision read-back failed for op type %d (read-back err: %v); sending no gossip, leaving the reconciler to resolve peers",
			opType, readErr)
		return false, fmt.Errorf("request tx commit outcome unknown for op type %d (read-back failed: %w): %w", opType, readErr, commitErr)
	case status == st.FlowExecutionStatusCommitted:
		e.honorDurablyCommitted(ctx, logger, commitErr,
			"2PC commit: request tx commit errored but decision is durably COMMITTED for op type %d; honoring the commit and dispatching commit gossip",
			opType)
		return true, nil
	default:
		// IN_FLIGHT (or an externally-swept ROLLED_BACK): the read-back locks
		// the row FOR UPDATE, so it can only report IN_FLIGHT after the request
		// tx has fully resolved as aborted — the decision write was inside that
		// tx, and nothing else ever transitions the row to COMMITTED. Roll back,
		// with the CAS below as a belt-and-suspenders guard should that
		// invariant ever be violated (a stale read through a future replica, a
		// weakened lock).
		logger.With(zap.Error(commitErr)).Sugar().Infof(
			"2PC commit: request tx commit failed for op type %d (durable status %s), attempting rollback", opType, status)
		markErr := e.markRolledBack(detachedCtx, row)
		switch {
		case markErr == nil:
			e.dispatchRollbackGossip(detachedCtx, opType, flow.RollbackPayload(), executionID, participants)
			return false, fmt.Errorf("request tx commit failed: %w", commitErr)
		case errors.Is(markErr, errRollbackUnsafe):
			// The rollback CAS positively established the decision is durably
			// COMMITTED — the read-back served a stale snapshot. A known-committed
			// decision must be honored exactly as if the read-back had seen it:
			// dispatch commit gossip so participants converge now instead of
			// stranding IN_FLIGHT with locked resources until the reconciler
			// pulls the outcome.
			e.honorDurablyCommitted(ctx, logger, commitErr,
				"2PC commit: read-back reported IN_FLIGHT but the rollback CAS found the decision durably COMMITTED for op type %d; honoring the commit and dispatching commit gossip",
				opType)
			return true, nil
		default:
			// Rollback could not be confirmed safe, so the row's terminal
			// status — and with it the commit outcome — is unknown after all.
			// Same rule as the read-back-failure case above: send nothing,
			// report the outcome as unknown (NOT as a definite failure — the
			// tx may be durably committed), and let the reconciler resolve
			// peers.
			logger.With(zap.Error(markErr)).Sugar().Errorf(
				"2PC commit: request tx commit errored for op type %d and rollback could not be confirmed safe; sending no gossip, leaving the reconciler to resolve peers", opType)
			return false, fmt.Errorf("request tx commit outcome unknown for op type %d (rollback safety unconfirmed: %w): %w", opType, markErr, commitErr)
		}
	}
}

// honorDurablyCommitted finalizes an ambiguous commit whose decision has been
// proven durably COMMITTED: the caller must proceed to dispatch commit gossip
// (on the detached ctx), which is what makes participants converge on
// COMMITTED independent of the RPC result.
//
// The request tx is durably committed but the session's OnCommit hook did
// not clear its tracked handle (the error is neither nil, ErrTxDone, nor
// context.Canceled), so detach the spent tx here — otherwise the
// idempotency store and DatabaseSessionMiddleware would reuse the spent
// *sql.Tx and fail with ErrTxDone. With the handle detached, a still-live
// request ctx (e.g. a mid-commit DB failover) lets those interceptors
// begin a fresh tx and the RPC returns success. If the ambiguity came from
// the request ctx itself expiring (deadline/cancel), no further DB work is
// possible and the caller still sees an error — but consensus is already
// safe: the commit is durable and commit gossip has been dispatched.
func (e *TwoPCEngine) honorDurablyCommitted(ctx context.Context, logger *zap.Logger, commitErr error, format string, opType pbgossip.ConsensusOperationType) {
	ent.DiscardResolvedTx(ctx)
	logger.With(zap.Error(commitErr)).Sugar().Warnf(format, opType)
}

// dispatchRollbackGossip sends rollback gossip after the coordinator row has
// been positively confirmed ROLLED_BACK. A dispatch failure is logged, not
// returned: the gossip record is the durable retry mechanism, and the caller
// is already propagating a primary error.
func (e *TwoPCEngine) dispatchRollbackGossip(ctx context.Context, opType pbgossip.ConsensusOperationType, rollbackOp proto.Message, executionID string, participants []string) {
	logger := logging.GetLoggerFromContext(ctx)
	if rollbackErr := e.rollback(ctx, opType, rollbackOp, executionID, participants); rollbackErr != nil {
		logger.With(zap.Error(rollbackErr)).Sugar().Errorf(
			"failed to send consensus rollback gossip for op type %d", opType)
	}
}

// attemptRollback runs the abort path: mark the coordinator row ROLLED_BACK
// (CAS — benign no-op if the sweep has already done so) and send rollback gossip
// to participants. The error is not returned: the caller is already in an error
// path with a primary failure reason that should propagate, and best-effort
// cleanup of the row plus rollback gossip is what the system is designed for.
//
// Rollback gossip is dispatched ONLY when markRolledBack returns nil — i.e. it
// positively confirmed the coordinator row is ROLLED_BACK (we transitioned it, or
// it already was). On ANY markRolledBack error the gossip is withheld: whether the
// row is durably COMMITTED, its terminal status is unreadable, or the transition
// UPDATE itself failed, we have NOT confirmed the coordinator is uncommitted, and
// a rollback we can't justify risks contradicting a committed coordinator. This
// mirrors resolveAmbiguousCommit's "unknown outcome → send nothing" rule.
//
// Convergence is still guaranteed in the withheld case: the participant reconciler
// and SweepStaleCoordinatorFlows drive peers to the coordinator's true outcome via
// ConsensusQueryOutcome. The cost is a bounded delay releasing locked resources on
// the rare markRolledBack error, preferred over an unjustified rollback.
//
// For context, every current caller reaches attemptRollback with a row that is
// structurally NOT COMMITTED (prepare/build-commit failure and a failed or
// preempted recordCommitDecision all run before a COMMITTED decision can exist).
// The COMMITTED case is thus a defensive invariant guard. resolveAmbiguousCommit
// does NOT use this helper: its rollback attempt must distinguish the
// discovered-committed case (errRollbackUnsafe → honor the commit) from the
// unconfirmable one (withhold), so it drives markRolledBack directly.
func (e *TwoPCEngine) attemptRollback(
	ctx context.Context,
	row *ent.FlowExecution,
	opType pbgossip.ConsensusOperationType,
	flow CoordinatorFlow,
	executionID string,
	participants []string,
) {
	logger := logging.GetLoggerFromContext(ctx)
	if markErr := e.markRolledBack(ctx, row); markErr != nil {
		logger.With(zap.Error(markErr)).Sugar().Errorf(
			"2PC rollback: withholding rollback gossip for op type %d — could not confirm the coordinator is uncommitted; leaving convergence to the reconciler", opType)
		return
	}
	e.dispatchRollbackGossip(ctx, opType, flow.RollbackPayload(), executionID, participants)
}

// inEngineSession runs fn inside a fresh db.Session bound to ctx. The
// session is injected into a child context (so callees that fetch via
// ent.GetDbFromContext find it), fn runs against that context, and any
// transaction the session opened is committed if fn succeeds or rolled
// back if fn errors or panics.
//
// This is the engine's analogue of DatabaseSessionMiddleware: same
// session machinery (notification flush, panic-recovery rollback,
// metric attribution, lazy tx-begin), just with a per-engine-call
// lifecycle rooted at the engine's cleanup ctx instead of the request
// ctx. Letting downstream calls — including the unmodified
// CreateCommitAndSendGossipMessage handler — operate against a
// session-style ctx is what keeps the engine's writes transactional in
// the same shape the rest of the codebase uses.
func (e *TwoPCEngine) inEngineSession(parentCtx context.Context, fn func(sessionCtx context.Context) error) (err error) {
	// Each engine bookkeeping phase gets a fresh engineCleanupTimeout
	// window. Applying the timeout here (rather than once at Execute
	// start) means a long Prepare or BuildCommitPayload doesn't burn
	// the cleanup-phase budget — markRolledBack and
	// commit/rollback gossip always run with the full window even if
	// the user-cancellable phases ate up most of the request's
	// surrounding deadline.
	ctx, cancel := context.WithTimeout(parentCtx, engineCleanupTimeout)
	defer cancel()

	session := e.sessionFactory.NewSession(ctx)
	sessionCtx := ent.Inject(ctx, session)

	var committed bool
	defer func() {
		r := recover()
		if !committed {
			if tx := session.GetTxIfExists(); tx != nil {
				_ = tx.Rollback()
			}
		}
		if r != nil {
			panic(r)
		}
	}()

	if fnErr := fn(sessionCtx); fnErr != nil {
		return fnErr
	}
	if tx := session.GetTxIfExists(); tx != nil {
		if commitErr := tx.Commit(); commitErr != nil {
			return fmt.Errorf("commit engine session tx: %w", commitErr)
		}
	}
	committed = true
	return nil
}

// createCoordinatorRow inserts the coordinator's FlowExecution row with the
// rollback payload pre-populated in decision_payload. If the coordinator later
// commits, that field is overwritten with the commit bytes; if the coordinator
// crashes before deciding, the self-sweep task transitions the row to
// ROLLED_BACK and the rollback bytes already in decision_payload become the
// answer served to reconciling participants.
//
// Runs in its own engine session (not the request session) so the row is
// durable regardless of whether the originating request transaction
// commits — the load-bearing property that lets participants always
// reconcile to a real outcome via ConsensusQueryOutcome.
func (e *TwoPCEngine) createCoordinatorRow(
	ctx context.Context,
	opType pbgossip.ConsensusOperationType,
	flow CoordinatorFlow,
) (*ent.FlowExecution, error) {
	rollbackBytes, err := marshalAny(flow.RollbackPayload())
	if err != nil {
		return nil, fmt.Errorf("failed to marshal rollback payload: %w", err)
	}
	self, ok := e.config.SigningOperatorMap[e.config.Identifier]
	if !ok || self == nil {
		return nil, fmt.Errorf("self operator %q not found in SigningOperatorMap", e.config.Identifier)
	}
	var row *ent.FlowExecution
	if err := e.inEngineSession(ctx, func(sessionCtx context.Context) error {
		client, err := ent.GetDbFromContext(sessionCtx)
		if err != nil {
			return err
		}
		var saveErr error
		row, saveErr = client.FlowExecution.Create().
			SetRole(st.FlowExecutionRoleCoordinator).
			SetOpType(int32(opType)).
			SetCoordinatorIndex(uint(self.ID)).
			SetDecisionPayload(rollbackBytes).
			Save(sessionCtx)
		return saveErr
	}); err != nil {
		return nil, err
	}
	return row, nil
}

// recordCommitDecision updates the coordinator row with the commit payload
// bytes and the COMMITTED status using the REQUEST transaction (via the
// ctx-bound client), so the decision commits atomically with the coordinator's
// domain work at the caller's DbCommit. It does NOT commit on its own.
//
// Uses a conditional UPDATE (status=IN_FLIGHT) so a concurrent self-sweep that
// has already transitioned the row to ROLLED_BACK is not silently overwritten;
// the two UPDATEs serialize on the row lock. Returns preempted=true when the
// CAS matches zero rows — the caller must then abort (not commit) the request
// tx so the coordinator's domain work is rolled back and both sides converge
// on rolled-back.
func (e *TwoPCEngine) recordCommitDecision(ctx context.Context, row *ent.FlowExecution, commitOp proto.Message) (preempted bool, err error) {
	commitBytes, err := marshalAny(commitOp)
	if err != nil {
		return false, fmt.Errorf("failed to marshal commit payload: %w", err)
	}
	client, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return false, err
	}
	rowsAffected, err := client.FlowExecution.Update().
		Where(
			flowexecution.ID(row.ID),
			flowexecution.StatusEQ(st.FlowExecutionStatusInFlight),
		).
		SetStatus(st.FlowExecutionStatusCommitted).
		SetDecisionPayload(commitBytes).
		Save(ctx)
	if err != nil {
		return false, err
	}
	return rowsAffected == 0, nil
}

// readBackDecisionStatusFromDB re-reads the coordinator row's status on a fresh
// engine session (a new connection, not the request tx whose commit just
// errored). This is the authoritative oracle for an ambiguous commit: the
// COMMITTED decision was written inside the request tx, so it is durable iff
// that tx actually committed.
//
// The row is read FOR UPDATE, and that lock is load-bearing: when the commit
// error came from the request ctx being cancelled mid-COMMIT, Postgres may
// still be landing that COMMIT while this read runs. A plain read would serve
// the stale IN_FLIGHT snapshot under MVCC (READ COMMITTED reads don't block on
// writers) and wrongly conclude the tx aborted. The lock wait blocks until the
// in-flight tx resolves either way, so the status read here is the true
// terminal outcome. If the request tx somehow lingers unresolved past the
// engine cleanup window, the lock wait times out and the error propagates —
// resolveAmbiguousCommit then sends no gossip and leaves convergence to the
// reconciler, which is the safe side.
//
// The read MUST target the primary. Each SO owns its Postgres and the engine's
// session factory routes to that primary (no read-replica layer here), so this
// observes the just-committed status. If a lagging read replica were ever
// placed behind this client, a stale IN_FLIGHT read on a durably-committed row
// would wrongly roll back a committed coordinator — the whole disambiguation
// rests on reading the primary.
func (e *TwoPCEngine) readBackDecisionStatusFromDB(ctx context.Context, id uuid.UUID) (st.FlowExecutionStatus, error) {
	var status st.FlowExecutionStatus
	if err := e.inEngineSession(ctx, func(sessionCtx context.Context) error {
		client, err := ent.GetDbFromContext(sessionCtx)
		if err != nil {
			return err
		}
		row, err := client.FlowExecution.Query().
			Where(flowexecution.ID(id)).
			ForUpdate().
			Only(sessionCtx)
		if err != nil {
			return err
		}
		status = row.Status
		return nil
	}); err != nil {
		return "", err
	}
	return status, nil
}

// errRollbackUnsafe signals that markRolledBack's CAS missed and the row is
// durably COMMITTED — an invariant violation, since a rollback must never be
// attempted over a committed decision (attemptRollback callers never pass a
// committed row; see the doc there). It is a distinct sentinel mainly so tests
// and logs can single out this case; attemptRollback withholds gossip on ANY
// markRolledBack error regardless.
var errRollbackUnsafe = errors.New("cannot safely roll back FlowExecution: coordinator decision is durably COMMITTED")

// markRolledBack transitions the coordinator row to ROLLED_BACK.
// decision_payload already contains the rollback bytes from row creation,
// so no payload update is needed.
//
// Like recordCommitDecision, uses a conditional UPDATE (CAS on status=IN_FLIGHT)
// so a row that's already terminal isn't touched again. It returns nil ONLY when
// it has positively confirmed the row is ROLLED_BACK — either it transitioned the
// row, or a CAS-miss re-read showed it was already ROLLED_BACK. Every other
// outcome is a non-nil error on which attemptRollback withholds rollback gossip:
//   - CAS miss + re-read shows COMMITTED → errRollbackUnsafe (invariant violation).
//   - CAS miss + re-read fails → wrapped error (terminal status unconfirmable).
//   - UPDATE itself errors → the error (row state unconfirmed).
func (e *TwoPCEngine) markRolledBack(ctx context.Context, row *ent.FlowExecution) error {
	return e.inEngineSession(ctx, func(sessionCtx context.Context) error {
		client, err := ent.GetDbFromContext(sessionCtx)
		if err != nil {
			return err
		}
		rowsAffected, err := client.FlowExecution.Update().
			Where(
				flowexecution.ID(row.ID),
				flowexecution.StatusEQ(st.FlowExecutionStatusInFlight),
			).
			SetStatus(st.FlowExecutionStatusRolledBack).
			Save(sessionCtx)
		if err != nil {
			return err
		}
		if rowsAffected == 0 {
			current, getErr := client.FlowExecution.Get(sessionCtx, row.ID)
			if getErr != nil {
				// Terminal row (CAS missed) whose status we can't read — can't
				// prove it isn't COMMITTED, so treat as unconfirmed (withholds).
				return fmt.Errorf("re-read of terminal FlowExecution %s failed: %w", row.ID, getErr)
			}
			if current.Status == st.FlowExecutionStatusCommitted {
				return errRollbackUnsafe
			}
		}
		return nil
	})
}

// marshalAny marshals a proto message into the wire-format bytes of an
// *anypb.Any (type URL + value) so the bytes can later round-trip via
// proto.Unmarshal into *anypb.Any and then Any.UnmarshalNew.
func marshalAny(msg proto.Message) ([]byte, error) {
	anyMsg, err := anypb.New(msg)
	if err != nil {
		return nil, err
	}
	return proto.Marshal(anyMsg)
}

// commit builds a ConsensusCommit gossip message and sends it to all
// participants for durable async delivery. Runs in an engine session so
// the underlying CreateCommitAndSendGossipMessage call (which uses
// ent.GetDbFromContext + ent.DbCommit internally) is transactional in
// the same shape it is on the request-tx path, just bound to the
// engine's cleanup ctx instead of the user-cancellable request ctx.
func (e *TwoPCEngine) commit(ctx context.Context, opType pbgossip.ConsensusOperationType, op proto.Message, executionID string, participants []string) error {
	anyOp, err := anypb.New(op)
	if err != nil {
		return fmt.Errorf("failed to marshal operation to Any: %w", err)
	}
	msg := &pbgossip.GossipMessage{
		Message: &pbgossip.GossipMessage_ConsensusCommit{
			ConsensusCommit: &pbgossip.GossipMessageConsensusCommit{
				OpType:          opType,
				Operation:       anyOp,
				FlowExecutionId: executionID,
			},
		},
	}
	return e.inEngineSession(ctx, func(sessionCtx context.Context) error {
		_, sendErr := e.gossip.CreateCommitAndSendGossipMessage(sessionCtx, msg, participants)
		return sendErr
	})
}

// rollback builds a ConsensusRollback gossip message and sends it to all
// participants for durable async delivery. Same engine-session shape as
// commit().
func (e *TwoPCEngine) rollback(ctx context.Context, opType pbgossip.ConsensusOperationType, op proto.Message, executionID string, participants []string) error {
	logger := logging.GetLoggerFromContext(ctx)
	logger.Sugar().Infof("2PC rollback: sending gossip for op type %d to %d participants", opType, len(participants))
	anyOp, err := anypb.New(op)
	if err != nil {
		return fmt.Errorf("failed to marshal operation to Any: %w", err)
	}
	msg := &pbgossip.GossipMessage{
		Message: &pbgossip.GossipMessage_ConsensusRollback{
			ConsensusRollback: &pbgossip.GossipMessageConsensusRollback{
				OpType:          opType,
				Operation:       anyOp,
				FlowExecutionId: executionID,
			},
		},
	}
	return e.inEngineSession(ctx, func(sessionCtx context.Context) error {
		_, sendErr := e.gossip.CreateCommitAndSendGossipMessage(sessionCtx, msg, participants)
		return sendErr
	})
}

// DefaultPrepareTask sends a ConsensusPrepare RPC to a remote operator.
// This is the common implementation for CoordinatorFlow.PrepareTask — every
// flow does the same thing, just with a different opType, prepareOp,
// executionID, and coordinatorIndex.
func DefaultPrepareTask(ctx context.Context, operator *so.SigningOperator, opType pbgossip.ConsensusOperationType, prepareOp proto.Message, executionID string, coordinatorIndex uint32) (proto.Message, error) {
	conn, err := operator.NewOperatorInternalGRPCConnection(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	anyOp, err := anypb.New(prepareOp)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal prepare request: %w", err)
	}
	client := pbinternal.NewSparkInternalServiceClient(conn)
	resp, err := client.ConsensusPrepare(ctx, &pbinternal.ConsensusPrepareRequest{
		OpType:           int32(opType),
		Operation:        anyOp,
		FlowExecutionId:  executionID,
		CoordinatorIndex: coordinatorIndex,
	})
	if err != nil {
		return nil, err
	}
	if resp.GetResult() == nil {
		return nil, nil
	}
	return resp.GetResult().UnmarshalNew()
}
