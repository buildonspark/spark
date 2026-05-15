package handler

import (
	"context"
	"fmt"

	pbinternal "github.com/lightsparkdev/spark/proto/spark_internal"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/consensus"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
)

// ClaimTransferFlowHandler implements consensus.FlowHandler for the
// CONSENSUS_OPERATION_TYPE_CLAIM_TRANSFER op (2PC claim-transfer).
//
// Plumbing only: this stub compiles and wires into the dispatch switch so
// gossip carrying op_type=CLAIM_TRANSFER routes here, but every method returns
// an error until the body lands in a follow-up change. No caller drives this
// code path yet — ClaimTransfer still runs the legacy fanout.
type ClaimTransferFlowHandler struct {
	config *so.Config
}

var _ consensus.FlowHandler = (*ClaimTransferFlowHandler)(nil)

func NewClaimTransferFlowHandler(config *so.Config) *ClaimTransferFlowHandler {
	return &ClaimTransferFlowHandler{config: config}
}

func (h *ClaimTransferFlowHandler) Prepare(_ context.Context, _ proto.Message) (proto.Message, error) {
	return nil, fmt.Errorf("claim transfer 2pc Prepare not yet implemented")
}

func (h *ClaimTransferFlowHandler) Commit(_ context.Context, _ proto.Message) error {
	return fmt.Errorf("claim transfer 2pc Commit not yet implemented")
}

func (h *ClaimTransferFlowHandler) Rollback(_ context.Context, _ proto.Message) error {
	return fmt.Errorf("claim transfer 2pc Rollback not yet implemented")
}

// claimTransferCoordinatorFlow — coordinator side.
//
// Stub: declares the type so the dispatch + interface assertions compile, but
// PrepareOp/BuildCommitPayload return zero values / errors. The constructor
// (buildClaimTransferCoordinatorFlow) and the ClaimTransfer wiring land in a
// follow-up change.
type claimTransferCoordinatorFlow struct {
	*ClaimTransferFlowHandler

	prepareReq *pbinternal.ClaimTransferPrepareRequest
}

var _ consensus.CoordinatorFlow = (*claimTransferCoordinatorFlow)(nil)

func (f *claimTransferCoordinatorFlow) PrepareOp() proto.Message {
	return f.prepareReq
}

func (f *claimTransferCoordinatorFlow) BuildCommitPayload(_ context.Context, _ map[string]*anypb.Any) (proto.Message, error) {
	return nil, fmt.Errorf("claim transfer 2pc BuildCommitPayload not yet implemented")
}

func (f *claimTransferCoordinatorFlow) RollbackPayload() proto.Message {
	// Populate transfer_id from the prepare request so the stub is safe if
	// reached before PR 3 wires the real coordinator flow — participants need
	// the transfer ID to revert receiver-side state.
	return &pbinternal.ClaimTransferRollbackRequest{
		TransferId: f.prepareReq.GetOriginalRequest().GetTransferId(),
	}
}
