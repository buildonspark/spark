package consensus

import (
	"context"

	pbgossip "github.com/lightsparkdev/spark/proto/gossip"
	"github.com/lightsparkdev/spark/so/ent"
	"google.golang.org/protobuf/proto"
)

// OperationPhase represents the phase of a consensus operation.
type OperationPhase int

const (
	phaseUnknown OperationPhase = iota // zero value; hits the default error path in Dispatch
	PhasePrepare
	PhaseCommit
	PhaseRollback
)

// FlowHandler defines the domain logic for a consensus flow. Every SO
// (including the coordinator) runs the same FlowHandler for a given flow.
//
// Implementors focus only on validation and state mutation. The consensus
// engine manages fan-out, DB transactions, status tracking, and delivery.
//
// Each consensus flow (renew, transfer, coop exit, etc.) implements this
// interface and registers it with the engine at startup.
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

// GossipSender abstracts gossip message creation and delivery.
// Implemented by SendGossipHandler.
type GossipSender interface {
	CreateCommitAndSendGossipMessage(ctx context.Context, msg *pbgossip.GossipMessage, participants []string) (*ent.Gossip, error)
}
