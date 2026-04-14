package handler

import (
	"context"
	"fmt"

	pbgossip "github.com/lightsparkdev/spark/proto/gossip"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/consensus"
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

// consensusFlowHandler returns the FlowHandler for a given consensus operation
// type. Shared by prepare dispatch, commit dispatch, and rollback dispatch to
// avoid repeating the opType→handler mapping in three places.
func consensusFlowHandler(config *so.Config, opType pbgossip.ConsensusOperationType) (consensus.FlowHandler, error) {
	switch opType {
	case pbgossip.ConsensusOperationType_CONSENSUS_OPERATION_TYPE_FINALIZE_DEPOSIT_TREE:
		return NewDepositTreeFlowHandler(config), nil
	case pbgossip.ConsensusOperationType_CONSENSUS_OPERATION_TYPE_STORE_PREIMAGE_SHARE:
		return NewPreimageShareFlowHandler(config), nil
	case pbgossip.ConsensusOperationType_CONSENSUS_OPERATION_TYPE_RENEW_LEAF:
		return NewRenewLeafFlowHandler(config), nil
	default:
		return nil, fmt.Errorf("unknown consensus operation type: %d", opType)
	}
}

// DispatchPrepare routes an incoming ConsensusPrepare RPC to the appropriate
// FlowHandler.Prepare based on operation type.
func (h *ConsensusHandler) DispatchPrepare(ctx context.Context, opType pbgossip.ConsensusOperationType, op *anypb.Any) (*anypb.Any, error) {
	handler, err := consensusFlowHandler(h.config, opType)
	if err != nil {
		return nil, err
	}
	msg, err := op.UnmarshalNew()
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal prepare request for op type %d: %w", opType, err)
	}
	result, err := handler.Prepare(ctx, msg)
	if err != nil {
		return nil, err
	}
	if result == nil {
		return nil, nil
	}
	return anypb.New(result)
}
