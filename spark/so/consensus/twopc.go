package consensus

import (
	"context"
	"fmt"

	"github.com/lightsparkdev/spark/common/logging"
	pbgossip "github.com/lightsparkdev/spark/proto/gossip"
	pbinternal "github.com/lightsparkdev/spark/proto/spark_internal"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/helper"
	"go.uber.org/zap"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
)

// TwoPCEngine orchestrates consensus using two-phase commit.
//
// The coordinator calls Execute with a CoordinatorFlow to run the full lifecycle:
//  1. Prepare: synchronous fan-out of flow.PrepareTask via ExecuteTaskWithAllOperators
//  2. BuildCommitPayload: coordinator builds commit payload from prepare results
//  3. Commit or Rollback: durable async delivery via gossip
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
// Prepare is fanned out to all selected operators:
//   - Remote operators: via flow.PrepareTask (typically ConsensusPrepare gRPC)
//   - Self (coordinator): via flow.Prepare locally to avoid gRPC deadlocks
//     when the caller holds DB locks
//
// After all prepares succeed, flow.BuildCommitPayload produces the commit
// gossip payload. If any step fails, a rollback gossip is sent with
// flow.RollbackPayload().
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

	// Wrap prepareTask: remote operators use DefaultPrepareTask (gRPC),
	// self uses flow.Prepare locally to avoid deadlock.
	// Both return proto.Message which is marshaled into *anypb.Any for the results map.
	prepareTask := func(ctx context.Context, operator *so.SigningOperator) (*anypb.Any, error) {
		var result proto.Message
		var err error
		if operator.Identifier == e.config.Identifier {
			result, err = flow.Prepare(ctx, flow.PrepareOp())
		} else {
			result, err = DefaultPrepareTask(ctx, operator, opType, flow.PrepareOp())
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
		if rollbackErr := e.rollback(ctx, opType, flow.RollbackPayload(), participants); rollbackErr != nil {
			logger.With(zap.Error(rollbackErr)).Sugar().Errorf(
				"failed to send consensus rollback gossip for op type %d", opType)
		}
		return nil, fmt.Errorf("prepare failed: %w", err)
	}
	logger.Sugar().Infof("2PC prepare: all %d participants ready for op type %d", len(participants), opType)

	commitOp, err := flow.BuildCommitPayload(ctx, results)
	if err != nil {
		logger.Sugar().Infof("2PC build-commit: failed for op type %d, sending rollback", opType)
		if rollbackErr := e.rollback(ctx, opType, flow.RollbackPayload(), participants); rollbackErr != nil {
			logger.With(zap.Error(rollbackErr)).Sugar().Errorf(
				"failed to send consensus rollback gossip for op type %d", opType)
		}
		return nil, fmt.Errorf("build-commit failed: %w", err)
	}

	logger.Sugar().Infof("2PC commit: sending gossip for op type %d to %d participants", opType, len(participants))
	if err := e.commit(ctx, opType, commitOp, participants); err != nil {
		logger.With(zap.Error(err)).Sugar().Errorf(
			"failed to send consensus commit gossip for op type %d", opType)
		return nil, fmt.Errorf("commit gossip failed: %w", err)
	}
	logger.Sugar().Infof("2PC commit: complete for op type %d", opType)
	return commitOp, nil
}

// commit builds a ConsensusCommit gossip message and sends it to all
// participants for durable async delivery.
func (e *TwoPCEngine) commit(ctx context.Context, opType pbgossip.ConsensusOperationType, op proto.Message, participants []string) error {
	anyOp, err := anypb.New(op)
	if err != nil {
		return fmt.Errorf("failed to marshal operation to Any: %w", err)
	}
	msg := &pbgossip.GossipMessage{
		Message: &pbgossip.GossipMessage_ConsensusCommit{
			ConsensusCommit: &pbgossip.GossipMessageConsensusCommit{
				OpType:    opType,
				Operation: anyOp,
			},
		},
	}
	_, err = e.gossip.CreateCommitAndSendGossipMessage(ctx, msg, participants)
	return err
}

// rollback builds a ConsensusRollback gossip message and sends it to all
// participants for durable async delivery.
func (e *TwoPCEngine) rollback(ctx context.Context, opType pbgossip.ConsensusOperationType, op proto.Message, participants []string) error {
	logger := logging.GetLoggerFromContext(ctx)
	logger.Sugar().Infof("2PC rollback: sending gossip for op type %d to %d participants", opType, len(participants))
	anyOp, err := anypb.New(op)
	if err != nil {
		return fmt.Errorf("failed to marshal operation to Any: %w", err)
	}
	msg := &pbgossip.GossipMessage{
		Message: &pbgossip.GossipMessage_ConsensusRollback{
			ConsensusRollback: &pbgossip.GossipMessageConsensusRollback{
				OpType:    opType,
				Operation: anyOp,
			},
		},
	}
	_, err = e.gossip.CreateCommitAndSendGossipMessage(ctx, msg, participants)
	return err
}

// DefaultPrepareTask sends a ConsensusPrepare RPC to a remote operator.
// This is the common implementation for CoordinatorFlow.PrepareTask — every
// flow does the same thing, just with a different opType and prepareOp.
func DefaultPrepareTask(ctx context.Context, operator *so.SigningOperator, opType pbgossip.ConsensusOperationType, prepareOp proto.Message) (proto.Message, error) {
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
		OpType:    int32(opType),
		Operation: anyOp,
	})
	if err != nil {
		return nil, err
	}
	if resp.Result == nil {
		return nil, nil
	}
	return resp.Result.UnmarshalNew()
}
