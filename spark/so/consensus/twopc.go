package consensus

import (
	"context"
	"fmt"
	"sync"

	pbgossip "github.com/lightsparkdev/spark/proto/gossip"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/helper"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
)

// TwoPCEngine orchestrates consensus using two-phase commit.
//
// Prepare uses synchronous fan-out via ExecuteTaskWithAllOperators.
// Commit and Rollback use gossip for durable async delivery with retry.
//
// On the receiving side, incoming gossip messages are routed to registered
// FlowHandlers via Dispatch.
type TwoPCEngine struct {
	config *so.Config
	gossip GossipSender

	mu       sync.RWMutex
	handlers map[pbgossip.ConsensusOperationType]FlowHandler
}

// NewTwoPCEngine creates a TwoPCEngine backed by synchronous operator
// fan-out for prepare and gossip for commit/rollback.
func NewTwoPCEngine(config *so.Config, gossip GossipSender) *TwoPCEngine {
	return &TwoPCEngine{
		config:   config,
		gossip:   gossip,
		handlers: make(map[pbgossip.ConsensusOperationType]FlowHandler),
	}
}

// Register adds a FlowHandler for the given operation type.
// Called at server startup for each domain flow.
// Returns an error if a handler is already registered for the given opType.
func (e *TwoPCEngine) Register(opType pbgossip.ConsensusOperationType, handler FlowHandler) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, exists := e.handlers[opType]; exists {
		return fmt.Errorf("handler already registered for operation type %q", opType)
	}
	e.handlers[opType] = handler
	return nil
}

// Dispatch routes an incoming operation to the registered FlowHandler
// based on operation type and phase.
//   - PhasePrepare: called from the RPC handler on each participant during synchronous fan-out.
//   - PhaseCommit / PhaseRollback: called from the gossip handler on each participant upon
//     receipt of a ConsensusCommit or ConsensusRollback gossip message.
//
// Only PhasePrepare returns non-nil bytes; PhaseCommit and PhaseRollback
// always return nil bytes.
func (e *TwoPCEngine) Dispatch(ctx context.Context, opType pbgossip.ConsensusOperationType, phase OperationPhase, op proto.Message) ([]byte, error) {
	e.mu.RLock()
	h, ok := e.handlers[opType]
	e.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("no handler registered for operation type %q", opType)
	}
	switch phase {
	case PhasePrepare:
		return h.Prepare(ctx, op)
	case PhaseCommit:
		return nil, h.Commit(ctx, op)
	case PhaseRollback:
		return nil, h.Rollback(ctx, op)
	default:
		return nil, fmt.Errorf("unknown operation phase %d", phase)
	}
}

// Prepare fans out a task to all selected operators synchronously.
// Returns results keyed by operator identifier, or error if any operator rejects.
func (e *TwoPCEngine) Prepare(ctx context.Context, task func(ctx context.Context, operator *so.SigningOperator) ([]byte, error), selection *helper.OperatorSelection) (map[string][]byte, error) {
	return helper.ExecuteTaskWithAllOperators(ctx, e.config, selection, task)
}

// Commit builds a GossipMessageConsensusCommit gossip message and sends it to
// all participants for durable async delivery. The gossip record is committed
// to DB before network delivery so background retry can pick it up.
func (e *TwoPCEngine) Commit(ctx context.Context, opType pbgossip.ConsensusOperationType, op proto.Message, participants []string) error {
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

// Rollback builds a GossipMessageConsensusRollback gossip message and sends it
// to all participants for durable async delivery. Uses the same durable delivery
// as Commit to ensure rollback is retried on failure.
func (e *TwoPCEngine) Rollback(ctx context.Context, opType pbgossip.ConsensusOperationType, op proto.Message, participants []string) error {
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
