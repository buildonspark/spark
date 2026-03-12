package consensus

import (
	"context"
	"fmt"
	"sync"

	pbgossip "github.com/lightsparkdev/spark/proto/gossip"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/helper"
	"google.golang.org/protobuf/proto"
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
	handlers map[string]FlowHandler
}

// NewTwoPCEngine creates a TwoPCEngine backed by synchronous operator
// fan-out for prepare and gossip for commit/rollback.
func NewTwoPCEngine(config *so.Config, gossip GossipSender) *TwoPCEngine {
	return &TwoPCEngine{
		config:   config,
		gossip:   gossip,
		handlers: make(map[string]FlowHandler),
	}
}

// Register adds a FlowHandler for the given operation type.
// Called at server startup for each domain flow.
// Returns an error if a handler is already registered for the given opType.
func (e *TwoPCEngine) Register(opType string, handler FlowHandler) error {
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
//     receipt of a consensus gossip message.
//
// Only PhasePrepare returns non-nil bytes; PhaseCommit and PhaseRollback
// always return nil bytes.
func (e *TwoPCEngine) Dispatch(ctx context.Context, opType string, phase OperationPhase, op proto.Message) ([]byte, error) {
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

// Commit sends a gossip message to all participants for durable async delivery.
// The gossip record is committed to DB before network delivery so background
// retry can pick it up if delivery fails.
//
// Currently identical to Rollback — both delegate to gossip. These will diverge
// when ConsensusCommit/ConsensusRollback gossip types are added and the engine
// builds the wrapper message internally.
func (e *TwoPCEngine) Commit(ctx context.Context, msg *pbgossip.GossipMessage, participants []string) error {
	_, err := e.gossip.CreateCommitAndSendGossipMessage(ctx, msg, participants)
	return err
}

// Rollback sends a gossip message to all participants for durable async delivery.
// See Commit for why these are currently identical.
func (e *TwoPCEngine) Rollback(ctx context.Context, msg *pbgossip.GossipMessage, participants []string) error {
	_, err := e.gossip.CreateCommitAndSendGossipMessage(ctx, msg, participants)
	return err
}
