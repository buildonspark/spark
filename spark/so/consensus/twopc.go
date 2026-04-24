package consensus

import (
	"context"
	"fmt"

	"github.com/lightsparkdev/spark/common/logging"
	pbgossip "github.com/lightsparkdev/spark/proto/gossip"
	pbinternal "github.com/lightsparkdev/spark/proto/spark_internal"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/helper"
	"go.uber.org/zap"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
)

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
}

// NewTwoPCEngine creates a TwoPCEngine backed by synchronous operator
// fan-out for prepare and gossip for commit/rollback.
func NewTwoPCEngine(config *so.Config, gossip GossipSender) *TwoPCEngine {
	return &TwoPCEngine{
		config: config,
		gossip: gossip,
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

	participants, err := selection.OperatorIdentifierList(e.config)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve participants: %w", err)
	}

	row, err := e.createCoordinatorRow(ctx, opType, flow)
	if err != nil {
		return nil, fmt.Errorf("failed to create FlowExecution row: %w", err)
	}
	executionID := row.ID.String()

	// Wrap prepareTask: remote operators use DefaultPrepareTask (gRPC),
	// self uses flow.Prepare locally to avoid deadlock.
	// Both return proto.Message which is marshaled into *anypb.Any for the results map.
	prepareTask := func(ctx context.Context, operator *so.SigningOperator) (*anypb.Any, error) {
		var result proto.Message
		var err error
		if operator.Identifier == e.config.Identifier {
			result, err = flow.Prepare(ctx, flow.PrepareOp())
		} else {
			result, err = DefaultPrepareTask(ctx, operator, opType, flow.PrepareOp(), executionID)
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
		if markErr := e.markRolledBack(ctx, row); markErr != nil {
			logger.With(zap.Error(markErr)).Sugar().Errorf(
				"failed to mark FlowExecution rolled back for op type %d", opType)
		}
		if rollbackErr := e.rollback(ctx, opType, flow.RollbackPayload(), executionID, participants); rollbackErr != nil {
			logger.With(zap.Error(rollbackErr)).Sugar().Errorf(
				"failed to send consensus rollback gossip for op type %d", opType)
		}
		return nil, fmt.Errorf("prepare failed: %w", err)
	}
	logger.Sugar().Infof("2PC prepare: all %d participants ready for op type %d", len(participants), opType)

	commitOp, err := flow.BuildCommitPayload(ctx, results)
	if err != nil {
		logger.Sugar().Infof("2PC build-commit: failed for op type %d, sending rollback", opType)
		if markErr := e.markRolledBack(ctx, row); markErr != nil {
			logger.With(zap.Error(markErr)).Sugar().Errorf(
				"failed to mark FlowExecution rolled back for op type %d", opType)
		}
		if rollbackErr := e.rollback(ctx, opType, flow.RollbackPayload(), executionID, participants); rollbackErr != nil {
			logger.With(zap.Error(rollbackErr)).Sugar().Errorf(
				"failed to send consensus rollback gossip for op type %d", opType)
		}
		return nil, fmt.Errorf("build-commit failed: %w", err)
	}

	if err := e.markCommitted(ctx, row, commitOp); err != nil {
		return nil, fmt.Errorf("failed to mark FlowExecution committed: %w", err)
	}

	logger.Sugar().Infof("2PC commit: sending gossip for op type %d to %d participants", opType, len(participants))
	if err := e.commit(ctx, opType, commitOp, executionID, participants); err != nil {
		logger.With(zap.Error(err)).Sugar().Errorf(
			"failed to send consensus commit gossip for op type %d", opType)
		return nil, fmt.Errorf("commit gossip failed: %w", err)
	}
	logger.Sugar().Infof("2PC commit: complete for op type %d", opType)
	return commitOp, nil
}

// createCoordinatorRow inserts the coordinator's FlowExecution row with the
// rollback payload pre-populated in decision_payload. If the coordinator later
// commits, that field is overwritten with the commit bytes; if the coordinator
// crashes before deciding, the self-sweep task transitions the row to
// ROLLED_BACK and the rollback bytes already in decision_payload become the
// answer served to reconciling participants.
func (e *TwoPCEngine) createCoordinatorRow(
	ctx context.Context,
	opType pbgossip.ConsensusOperationType,
	flow CoordinatorFlow,
) (*ent.FlowExecution, error) {
	rollbackBytes, err := marshalAny(flow.RollbackPayload())
	if err != nil {
		return nil, fmt.Errorf("failed to marshal rollback payload: %w", err)
	}
	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, err
	}
	self, ok := e.config.SigningOperatorMap[e.config.Identifier]
	if !ok || self == nil {
		return nil, fmt.Errorf("self operator %q not found in SigningOperatorMap", e.config.Identifier)
	}
	return db.FlowExecution.Create().
		SetRole(st.FlowExecutionRoleCoordinator).
		SetOpType(int32(opType)).
		SetCoordinatorIndex(uint(self.ID)).
		SetDecisionPayload(rollbackBytes).
		Save(ctx)
}

// markCommitted updates the coordinator row with the commit payload bytes and
// the COMMITTED status. Called before commit gossip is sent so a late crash
// leaves the row in COMMITTED state with the correct payload.
func (e *TwoPCEngine) markCommitted(ctx context.Context, row *ent.FlowExecution, commitOp proto.Message) error {
	commitBytes, err := marshalAny(commitOp)
	if err != nil {
		return fmt.Errorf("failed to marshal commit payload: %w", err)
	}
	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return err
	}
	_, err = db.FlowExecution.UpdateOne(row).
		SetStatus(st.FlowExecutionStatusCommitted).
		SetDecisionPayload(commitBytes).
		Save(ctx)
	return err
}

// markRolledBack transitions the coordinator row to ROLLED_BACK. decision_payload
// already contains the rollback bytes from row creation, so no payload update
// is needed.
func (e *TwoPCEngine) markRolledBack(ctx context.Context, row *ent.FlowExecution) error {
	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return err
	}
	_, err = db.FlowExecution.UpdateOne(row).
		SetStatus(st.FlowExecutionStatusRolledBack).
		Save(ctx)
	return err
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
// participants for durable async delivery.
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
	_, err = e.gossip.CreateCommitAndSendGossipMessage(ctx, msg, participants)
	return err
}

// rollback builds a ConsensusRollback gossip message and sends it to all
// participants for durable async delivery.
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
	_, err = e.gossip.CreateCommitAndSendGossipMessage(ctx, msg, participants)
	return err
}

// DefaultPrepareTask sends a ConsensusPrepare RPC to a remote operator.
// This is the common implementation for CoordinatorFlow.PrepareTask — every
// flow does the same thing, just with a different opType, prepareOp, and
// executionID.
func DefaultPrepareTask(ctx context.Context, operator *so.SigningOperator, opType pbgossip.ConsensusOperationType, prepareOp proto.Message, executionID string) (proto.Message, error) {
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
		OpType:          int32(opType),
		Operation:       anyOp,
		FlowExecutionId: executionID,
	})
	if err != nil {
		return nil, err
	}
	if resp.Result == nil {
		return nil, nil
	}
	return resp.Result.UnmarshalNew()
}
