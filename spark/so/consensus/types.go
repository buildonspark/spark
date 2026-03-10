package consensus

import (
	"context"

	"google.golang.org/protobuf/proto"
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
	// The engine guarantees this runs inside a DB transaction.
	Commit(ctx context.Context, op proto.Message) error

	// Rollback reverts any state locked during Prepare.
	// Called if any participant rejects or the coordinator aborts.
	Rollback(ctx context.Context, op proto.Message) error
}
