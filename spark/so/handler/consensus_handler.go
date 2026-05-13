package handler

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	pbgossip "github.com/lightsparkdev/spark/proto/gossip"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/consensus"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"google.golang.org/protobuf/proto"
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
	case pbgossip.ConsensusOperationType_CONSENSUS_OPERATION_TYPE_SEND_TRANSFER:
		return NewSendTransferFlowHandler(config), nil
	default:
		return nil, fmt.Errorf("unknown consensus operation type: %d", opType)
	}
}

// DispatchPrepare routes an incoming ConsensusPrepare RPC to the appropriate
// FlowHandler.Prepare based on operation type. After a successful Prepare,
// writes a PARTICIPANT FlowExecution row (keyed by flowExecutionID, tagged
// with coordinatorIndex) so the reconciliation task can later query the
// coordinator for the outcome if commit/rollback gossip is lost.
//
// When flowExecutionID is empty the caller is a pre-upgrade coordinator that
// does not supply a row id yet; the handler dispatches as before and skips
// the FlowExecution write. Once all coordinators populate the field this
// branch becomes unreachable, but the skip keeps rollout compatible.
func (h *ConsensusHandler) DispatchPrepare(
	ctx context.Context,
	opType pbgossip.ConsensusOperationType,
	op *anypb.Any,
	flowExecutionID string,
	coordinatorIndex uint,
) (*anypb.Any, error) {
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

	if flowExecutionID != "" {
		if err := writeParticipantFlowExecutionRow(ctx, flowExecutionID, int32(opType), coordinatorIndex, op); err != nil {
			return nil, err
		}
	}

	if result == nil {
		return nil, nil
	}
	return anypb.New(result)
}

// writeParticipantFlowExecutionRow inserts a PARTICIPANT FlowExecution row with
// its id set to flowExecutionID. The row is written on the same DB tx as the
// flow's prepare work so the "prepared" state and the "we recorded prepare"
// state are atomic.
//
// The marshalled prepare op is persisted on the row so the participant
// reconciler can synthesize a local rollback if the coordinator's row never
// landed (e.g., its request tx aborted before commit) — without it, the
// participant would be stuck IN_FLIGHT awaiting a decision that will never
// arrive via gossip.
func writeParticipantFlowExecutionRow(ctx context.Context, flowExecutionID string, opType int32, coordinatorIndex uint, op *anypb.Any) error {
	id, err := uuid.Parse(flowExecutionID)
	if err != nil {
		return fmt.Errorf("invalid flow_execution_id: %w", err)
	}
	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return err
	}
	create := db.FlowExecution.Create().
		SetID(id).
		SetRole(st.FlowExecutionRoleParticipant).
		SetOpType(opType).
		SetCoordinatorIndex(coordinatorIndex)
	if op != nil {
		prepareBytes, err := proto.Marshal(op)
		if err != nil {
			return fmt.Errorf("failed to marshal prepare payload: %w", err)
		}
		create = create.SetPreparePayload(prepareBytes)
	}
	_, err = create.Save(ctx)
	return err
}
