package handler

import (
	"context"
	"fmt"

	pbgossip "github.com/lightsparkdev/spark/proto/gossip"
	pbinternal "github.com/lightsparkdev/spark/proto/spark_internal"
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
// FlowHandler.Prepare based on operation type.
func (h *ConsensusHandler) DispatchPrepare(ctx context.Context, opType pbgossip.ConsensusOperationType, op *anypb.Any) (*anypb.Any, error) {
	switch opType {
	case pbgossip.ConsensusOperationType_CONSENSUS_OPERATION_TYPE_FINALIZE_DEPOSIT_TREE:
		msg := &pbinternal.DepositTreePrepareRequest{}
		if err := op.UnmarshalTo(msg); err != nil {
			return nil, fmt.Errorf("failed to unmarshal deposit tree prepare request: %w", err)
		}
		result, err := NewDepositTreeFlowHandler(h.config).Prepare(ctx, msg)
		if err != nil {
			return nil, err
		}
		if result == nil {
			return nil, nil
		}
		return anypb.New(result)
	case pbgossip.ConsensusOperationType_CONSENSUS_OPERATION_TYPE_STORE_PREIMAGE_SHARE:
		msg := &pbinternal.StorePreimageSharePrepareRequest{}
		if err := op.UnmarshalTo(msg); err != nil {
			return nil, fmt.Errorf("failed to unmarshal preimage share prepare request: %w", err)
		}
		result, err := NewPreimageShareFlowHandler(h.config).Prepare(ctx, msg)
		if err != nil {
			return nil, err
		}
		if result == nil {
			return nil, nil
		}
		return anypb.New(result)
	default:
		return nil, fmt.Errorf("unknown consensus operation type for prepare: %d", opType)
	}
}
