package handler

import (
	"context"
	"fmt"

	pbgossip "github.com/lightsparkdev/spark/proto/gossip"
	"github.com/lightsparkdev/spark/so"
	"google.golang.org/protobuf/types/known/anypb"
)

// ConsensusHandler dispatches incoming ConsensusPrepare RPCs to the appropriate
// FlowHandler.Prepare based on operation type.
type ConsensusHandler struct {
	config *so.Config
}

// NewConsensusHandler creates a new ConsensusHandler.
func NewConsensusHandler(config *so.Config) *ConsensusHandler {
	return &ConsensusHandler{config: config}
}

// DispatchPrepare routes an incoming ConsensusPrepare RPC to the appropriate
// FlowHandler.Prepare based on operation type. Called by SparkInternalServer
// when the coordinator fans out prepare via the engine.
// Returns the prepare result as *anypb.Any for the gRPC response.
func (h *ConsensusHandler) DispatchPrepare(_ context.Context, opType pbgossip.ConsensusOperationType, op *anypb.Any) (*anypb.Any, error) {
	_, err := op.UnmarshalNew()
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal consensus prepare operation: %w", err)
	}
	switch opType {
	// TODO: Add cases here as domain flows are migrated to the consensus engine.
	// Each case unmarshals op, calls FlowHandler.Prepare, and wraps the result in Any.
	default:
		return nil, fmt.Errorf("unknown consensus operation type for prepare: %d", opType)
	}
}
