package consensus

import (
	"context"
	"errors"
	"fmt"
	"time"

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
// (createCoordinatorRow, markCommitted/markRolledBack, commit/rollback
// gossip dispatch) is allowed to run. Each call to inEngineSession derives
// a fresh WithTimeout from this value, so a long Prepare or BuildCommitPayload
// doesn't burn the cleanup-phase budget — the post-decision path always
// gets the full window to drive participants to a terminal outcome,
// regardless of how long the request-cancellable phases took.
const engineCleanupTimeout = 60 * time.Second

// ErrCoordinatorRowPreempted is returned by markCommitted (and is the cause
// surfaced by Execute when applicable) if the coordinator's FlowExecution
// row has been transitioned out of IN_FLIGHT before the engine got to its
// commit-time write. Most likely cause: SweepStaleCoordinatorFlows raced
// the engine and presumed-aborted the row to ROLLED_BACK, in which case
// proceeding to commit gossip would diverge the coordinator's recorded
// outcome from what participants will see when they reconcile.
var ErrCoordinatorRowPreempted = errors.New("coordinator FlowExecution row was preempted before markCommitted (likely swept to ROLLED_BACK by the self-sweep task)")

// TwoPCEngine orchestrates consensus using two-phase commit.
//
// The coordinator calls Execute with a CoordinatorFlow to run the full lifecycle:
//  1. Create a FlowExecution row pre-populated with the rollback payload.
//  2. Prepare: synchronous fan-out of flow.PrepareTask via ExecuteTaskWithAllOperators,
//     passing the row's id as flow_execution_id so participants can create their own
//     rows with the same id on their own databases.
//  3. BuildCommitPayload: coordinator builds the commit payload from prepare results.
//  4. Update the row to its terminal status (COMMITTED or ROLLED_BACK), overwriting
//     decision_payload with commit bytes on success.
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
	// (createCoordinatorRow, markCommitted, markRolledBack, gossip
	// dispatch). The engine session is bound to a detached cleanup
	// context so the session — and its transaction — survive a
	// user-cancelled request. Sharing the SessionFactory abstraction
	// with the gRPC database middleware means engine writes go through
	// exactly the same Begin/Save/Commit machinery as request-tx writes
	// (notification flush hooks, panic-recovery rollback, metric
	// attribution, lazy tx-begin), with only the lifecycle differing.
	sessionFactory db.SessionFactory
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
	return &TwoPCEngine{
		config:         config,
		gossip:         gossip,
		sessionFactory: sessionFactory,
	}
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
			result, err = flow.Prepare(ctx, flow.PrepareOp())
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

	// Commit the coordinator's request transaction BEFORE telling
	// participants to commit. FlowHandler.Prepare (and BuildCommitPayload
	// for some flows) writes coordinator-side domain state through the
	// request session — preimage_shares for StorePreimageShareV2,
	// new tree nodes for FinalizeDepositTreeCreation, etc. If we
	// dispatched commit gossip while that work was still uncommitted
	// and the request tx subsequently failed to commit at handler
	// return, participants would commit on the strength of the
	// already-durable engine FlowExecution row (markCommitted fires
	// next) while the coordinator's local domain state is rolled back.
	// Mirrors the pre-refactor pattern in CreateCommitAndSendGossipMessage,
	// which committed the request tx mid-function before gossip dispatch.
	//
	// On failure here we treat it as a build-commit failure: roll back
	// the engine row and dispatch rollback gossip, so participants
	// don't end up committed against a coordinator that lost its
	// domain writes.
	if commitErr := ent.DbCommit(ctx); commitErr != nil {
		logger.With(zap.Error(commitErr)).Sugar().Infof(
			"2PC commit: request tx commit failed for op type %d, sending rollback", opType)
		e.attemptRollback(detachedCtx, row, opType, flow, executionID, participants)
		return nil, fmt.Errorf("request tx commit failed: %w", commitErr)
	}

	if err := e.markCommitted(detachedCtx, row, commitOp); err != nil {
		// CAS conflict: the row was already transitioned out of
		// IN_FLIGHT by some other path (most likely the self-sweep).
		// Don't send commit gossip; the participants will resolve to
		// the actually-recorded outcome via ConsensusQueryOutcome.
		// NOTE: the request tx committed above, so coordinator's
		// domain work persists. With the engine row eventually
		// transitioned to ROLLED_BACK by the sweep, participants
		// reconcile to ROLLED_BACK — a divergence (coordinator did
		// the work, participants think rolled-back), but recoverable
		// only at the application level.
		if errors.Is(err, ErrCoordinatorRowPreempted) {
			logger.With(zap.Error(err)).Sugar().Warnf(
				"2PC commit: coordinator row preempted for op type %d, skipping commit gossip", opType)
			return nil, fmt.Errorf("commit preempted: %w", err)
		}
		return nil, fmt.Errorf("failed to mark FlowExecution committed: %w", err)
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

// attemptRollback runs the abort path: mark the coordinator row
// ROLLED_BACK (CAS — benign no-op if the sweep has already done so) and
// send rollback gossip to participants. Errors on each step are logged but
// not returned: the caller is already in an error path with a primary
// failure reason that should propagate, and best-effort cleanup of the
// row plus rollback gossip is what the system is designed for.
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
			"failed to mark FlowExecution rolled back for op type %d", opType)
	}
	if rollbackErr := e.rollback(ctx, opType, flow.RollbackPayload(), executionID, participants); rollbackErr != nil {
		logger.With(zap.Error(rollbackErr)).Sugar().Errorf(
			"failed to send consensus rollback gossip for op type %d", opType)
	}
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
	// the cleanup-phase budget — markCommitted/markRolledBack and
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

// markCommitted updates the coordinator row with the commit payload bytes and
// the COMMITTED status. Called before commit gossip is sent so a late crash
// leaves the row in COMMITTED state with the correct payload.
//
// Uses a conditional UPDATE (status=IN_FLIGHT) rather than UpdateOne so a
// concurrent self-sweep that has already transitioned the row to
// ROLLED_BACK is not silently overwritten. Returns
// ErrCoordinatorRowPreempted when the CAS finds zero rows; the caller
// must abort the commit gossip in that case so the coordinator's recorded
// outcome stays consistent with what participants will see via
// ConsensusQueryOutcome.
func (e *TwoPCEngine) markCommitted(ctx context.Context, row *ent.FlowExecution, commitOp proto.Message) error {
	commitBytes, err := marshalAny(commitOp)
	if err != nil {
		return fmt.Errorf("failed to marshal commit payload: %w", err)
	}
	var rowsAffected int
	if err := e.inEngineSession(ctx, func(sessionCtx context.Context) error {
		client, err := ent.GetDbFromContext(sessionCtx)
		if err != nil {
			return err
		}
		var updErr error
		rowsAffected, updErr = client.FlowExecution.Update().
			Where(
				flowexecution.ID(row.ID),
				flowexecution.StatusEQ(st.FlowExecutionStatusInFlight),
			).
			SetStatus(st.FlowExecutionStatusCommitted).
			SetDecisionPayload(commitBytes).
			Save(sessionCtx)
		return updErr
	}); err != nil {
		return err
	}
	if rowsAffected == 0 {
		return ErrCoordinatorRowPreempted
	}
	return nil
}

// markRolledBack transitions the coordinator row to ROLLED_BACK.
// decision_payload already contains the rollback bytes from row creation,
// so no payload update is needed.
//
// Like markCommitted, uses a conditional UPDATE so a row that's already
// terminal (most likely already-rolled-back via the self-sweep) isn't
// touched again. Unlike markCommitted, a CAS conflict here is benign:
// the row is already in the rolled-back state we wanted, so the
// zero-rows-affected case is silently treated as success.
func (e *TwoPCEngine) markRolledBack(ctx context.Context, row *ent.FlowExecution) error {
	return e.inEngineSession(ctx, func(sessionCtx context.Context) error {
		client, err := ent.GetDbFromContext(sessionCtx)
		if err != nil {
			return err
		}
		_, err = client.FlowExecution.Update().
			Where(
				flowexecution.ID(row.ID),
				flowexecution.StatusEQ(st.FlowExecutionStatusInFlight),
			).
			SetStatus(st.FlowExecutionStatusRolledBack).
			Save(sessionCtx)
		return err
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
	conn, err := operator.NewOperatorGRPCConnection()
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
	if resp.Result == nil {
		return nil, nil
	}
	return resp.Result.UnmarshalNew()
}
