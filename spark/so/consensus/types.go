package consensus

import (
	"context"

	pbgossip "github.com/lightsparkdev/spark/proto/gossip"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/ent"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
)

// FlowHandler defines the domain logic for a consensus flow. Every SO
// (including the coordinator) runs the same FlowHandler for a given flow.
//
// Implementors focus only on validation and state mutation. The consensus
// engine manages fan-out, DB transactions, status tracking, and delivery.
//
// Each consensus flow (renew, transfer, coop exit, etc.) implements this
// interface. The gossip handler dispatches incoming commit/rollback messages
// to the appropriate handler via a switch on ConsensusOperationType.
type FlowHandler interface {
	// Prepare validates the operation and locks any required resources.
	// Called on every participant (including the coordinator).
	// Returns a domain-specific result proto (e.g., signature shares) or error to reject.
	// May return nil if no result is needed.
	Prepare(ctx context.Context, op proto.Message) (proto.Message, error)

	// Commit applies the final state change after all participants have prepared.
	// Called via gossip dispatch on each participant.
	Commit(ctx context.Context, op proto.Message) error

	// Rollback reverts any state locked during Prepare.
	// Called via gossip dispatch if any participant rejects or the coordinator aborts.
	Rollback(ctx context.Context, op proto.Message) error
}

// CoordinatorFlow defines the full behavior for a consensus operation,
// combining participant-side dispatch (FlowHandler) with coordinator-side
// orchestration. The engine uses:
//   - PrepareTask for remote operators (gRPC fan-out)
//   - FlowHandler.Prepare for self (local call, avoids gRPC deadlock)
//   - BuildCommitPayload after all prepares succeed
//   - RollbackPayload on any failure
type CoordinatorFlow interface {
	FlowHandler

	// PrepareOp returns the operation message passed to FlowHandler.Prepare
	// on each participant. Used by the engine for the local self-call and by
	// PrepareTask for the ConsensusPrepare RPC payload.
	PrepareOp() proto.Message

	// PrepareTask is fanned out to all remote operators during the prepare phase.
	// For the coordinator (self), the engine calls FlowHandler.Prepare locally instead.
	PrepareTask(ctx context.Context, operator *so.SigningOperator) (proto.Message, error)

	// BuildCommitPayload produces the commit gossip payload from prepare results.
	// For aggregating flows (e.g., FROST signing), this aggregates signature shares
	// into a finalized transaction. For simple flows, this ignores results and
	// returns a static message.
	BuildCommitPayload(ctx context.Context, results map[string]*anypb.Any) (proto.Message, error)

	// RollbackPayload returns the gossip payload sent on rollback.
	RollbackPayload() proto.Message
}

// GossipSender abstracts gossip message creation and delivery.
// Implemented by SendGossipHandler.
type GossipSender interface {
	CreateCommitAndSendGossipMessage(ctx context.Context, msg *pbgossip.GossipMessage, participants []string) (*ent.Gossip, error)
}
