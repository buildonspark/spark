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

// SendTransferFlowHandler implements consensus.FlowHandler for the
// CONSENSUS_OPERATION_TYPE_SEND_TRANSFER op (v3 send-transfer-with-transfer-package).
//
// Plumbing only: this stub compiles and wires into the dispatch switch so
// gossip carrying op_type=SEND_TRANSFER routes here, but every method returns
// an error until the body lands in a follow-up change. No caller drives this
// code path yet — StartTransferV3 still runs the legacy fanout.
type SendTransferFlowHandler struct {
	config *so.Config
}

var _ consensus.FlowHandler = (*SendTransferFlowHandler)(nil)

func NewSendTransferFlowHandler(config *so.Config) *SendTransferFlowHandler {
	return &SendTransferFlowHandler{config: config}
}

func (h *SendTransferFlowHandler) Prepare(_ context.Context, _ proto.Message) (proto.Message, error) {
	return nil, fmt.Errorf("send transfer 2pc Prepare not yet implemented")
}

func (h *SendTransferFlowHandler) Commit(_ context.Context, _ proto.Message) error {
	return fmt.Errorf("send transfer 2pc Commit not yet implemented")
}

func (h *SendTransferFlowHandler) Rollback(_ context.Context, _ proto.Message) error {
	return fmt.Errorf("send transfer 2pc Rollback not yet implemented")
}

// sendTransferCoordinatorFlow — coordinator side.
//
// Stub: declares the type so the dispatch + interface assertions compile, but
// PrepareOp/BuildCommitPayload return zero values / errors. The constructor
// (buildSendTransferCoordinatorFlow) and the StartTransferV3 wiring land in a
// follow-up change.
type sendTransferCoordinatorFlow struct {
	*SendTransferFlowHandler

	prepareReq *pbinternal.SendTransferPrepareRequest
}

var _ consensus.CoordinatorFlow = (*sendTransferCoordinatorFlow)(nil)

func (f *sendTransferCoordinatorFlow) PrepareOp() proto.Message {
	return f.prepareReq
}

func (f *sendTransferCoordinatorFlow) BuildCommitPayload(_ context.Context, _ map[string]*anypb.Any) (proto.Message, error) {
	return nil, fmt.Errorf("send transfer 2pc BuildCommitPayload not yet implemented")
}

func (f *sendTransferCoordinatorFlow) RollbackPayload() proto.Message {
	return &pbinternal.SendTransferRollbackRequest{}
}
