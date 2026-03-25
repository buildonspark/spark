package consensus

import (
	"context"

	pbgossip "github.com/lightsparkdev/spark/proto/gossip"
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

// PostPrepare is optionally implemented by a FlowHandler to process prepare
// results on the coordinator before commit. For example, a renew flow uses
// this to aggregate FROST signature shares into finalized transactions.
//
// The returned proto.Message becomes the commit gossip payload. If PostPrepare
// fails, the engine sends a rollback instead.
type PostPrepare interface {
	PostPrepare(ctx context.Context, results map[string][]byte) (commitOp proto.Message, err error)
}

// GossipSender abstracts gossip message creation and delivery.
// Implemented by SendGossipHandler.
type GossipSender interface {
	CreateCommitAndSendGossipMessage(ctx context.Context, msg *pbgossip.GossipMessage, participants []string) (*ent.Gossip, error)
}
