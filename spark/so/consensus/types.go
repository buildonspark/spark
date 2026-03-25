package consensus

import (
	"context"

	pbgossip "github.com/lightsparkdev/spark/proto/gossip"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/ent"
	"google.golang.org/protobuf/proto"
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
	// Returns domain-specific result bytes (e.g., signature shares) or error to reject.
	Prepare(ctx context.Context, op proto.Message) ([]byte, error)

	// Commit applies the final state change after all participants have prepared.
	// Called via gossip dispatch on each participant.
	Commit(ctx context.Context, op proto.Message) error

	// Rollback reverts any state locked during Prepare.
	// Called via gossip dispatch if any participant rejects or the coordinator aborts.
	Rollback(ctx context.Context, op proto.Message) error
}

// CoordinatorFlow defines the coordinator-side behavior for a consensus operation.
// Each consensus flow (renew, transfer, coop exit, etc.) implements this interface.
// The engine fans out PrepareTask to all participants, then sends a commit or
// rollback gossip message.
type CoordinatorFlow interface {
	// PrepareTask is fanned out to all selected operators during the prepare phase.
	PrepareTask(ctx context.Context, operator *so.SigningOperator) ([]byte, error)

	// BuildCommitPayload produces the commit gossip payload from prepare results.
	// For aggregating flows (e.g., FROST signing), this aggregates signature shares
	// into a finalized transaction. For simple flows, this ignores results and
	// returns a static message.
	BuildCommitPayload(ctx context.Context, results map[string][]byte) (proto.Message, error)

	// RollbackPayload returns the gossip payload sent on rollback.
	RollbackPayload() proto.Message
}

// GossipSender abstracts gossip message creation and delivery.
// Implemented by SendGossipHandler.
type GossipSender interface {
	CreateCommitAndSendGossipMessage(ctx context.Context, msg *pbgossip.GossipMessage, participants []string) (*ent.Gossip, error)
}
