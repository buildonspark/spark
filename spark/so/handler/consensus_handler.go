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
	case pbgossip.ConsensusOperationType_CONSENSUS_OPERATION_TYPE_CLAIM_TRANSFER:
		return NewClaimTransferFlowHandler(config), nil
	case pbgossip.ConsensusOperationType_CONSENSUS_OPERATION_TYPE_PROVIDE_PREIMAGE:
		return NewProvidePreimageFlowHandler(config), nil
	case pbgossip.ConsensusOperationType_CONSENSUS_OPERATION_TYPE_COOP_EXIT:
		return NewCoopExitFlowHandler(config), nil
	case pbgossip.ConsensusOperationType_CONSENSUS_OPERATION_TYPE_INITIATE_PREIMAGE_SWAP:
		return NewInitiatePreimageSwapFlowHandler(config), nil
	case pbgossip.ConsensusOperationType_CONSENSUS_OPERATION_TYPE_STATIC_DEPOSIT_UTXO_REFUND:
		return NewStaticDepositUtxoRefundFlowHandler(config), nil
	case pbgossip.ConsensusOperationType_CONSENSUS_OPERATION_TYPE_STATIC_DEPOSIT_UTXO_SWAP:
		return NewStaticDepositUtxoSwapFlowHandler(config), nil
	case pbgossip.ConsensusOperationType_CONSENSUS_OPERATION_TYPE_RESERVE_INSTANT_STATIC_DEPOSIT_UTXO_SWAP:
		return NewReserveInstantStaticDepositFlowHandler(config), nil
	case pbgossip.ConsensusOperationType_CONSENSUS_OPERATION_TYPE_CLAIM_INSTANT_STATIC_DEPOSIT_UTXO_SWAP:
		return NewClaimInstantStaticDepositFlowHandler(config), nil
	case pbgossip.ConsensusOperationType_CONSENSUS_OPERATION_TYPE_INITIATE_SWAP_PRIMARY_TRANSFER:
		return NewSwapPrimaryTransferFlowHandler(config), nil
	case pbgossip.ConsensusOperationType_CONSENSUS_OPERATION_TYPE_INITIATE_COUNTER_TRANSFER:
		return NewSwapCounterTransferFlowHandler(config), nil
	case pbgossip.ConsensusOperationType_CONSENSUS_OPERATION_TYPE_AGGREGATE_LEAVES:
		return NewAggregateLeavesFlowHandler(config), nil
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
// All coordinators have populated flowExecutionID and coordinatorIndex since
// the April 2026 rollout (#6288/#6293), so every request is expected to carry
// a resolvable coordinator_index and the validation below runs
// unconditionally. The empty-flowExecutionID skip on the FlowExecution write
// is a dormant leftover of that rollout, kept only as a harmless guard and
// paired with classifyConsensusOp's empty-flow_execution_id branch
// (gossip_handler.go) — remove both together. Do not gate coordinator_index
// validation on it — a request could omit the row id to dodge validation, and
// with no coordinator identity on ctx, flow handlers would fall back to
// recording the receiving SO as coordinator, breaking the
// participant-rows-never-record-self invariant.
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
	// Resolve the coordinator's identity from the caller-declared index and attach
	// it to ctx so flow handlers can record the coordinator on durable state
	// without trusting a self-declared payload field. Fail closed: an index that
	// does not resolve to a known operator rejects the Prepare rather than letting
	// a flow handler silently proceed (or fall back to recording itself as
	// coordinator). Note the index proves membership, not possession — the
	// IP-restricted channel bounds callers to operators, but a misbehaving
	// operator can still declare another operator's index; cross-checking against
	// the authenticated peer identity is tracked as an engine-wide follow-up.
	coordinator, err := h.config.GetOperatorByID(uint64(coordinatorIndex))
	if err != nil {
		return nil, fmt.Errorf("consensus prepare for op type %d declares unknown coordinator_index %d: %w", opType, coordinatorIndex, err)
	}
	// A coordinator runs its own Prepare locally inside the engine (twopc.go) and
	// never sends ConsensusPrepare to itself, so a request naming the receiving SO
	// as coordinator is always forged or misrouted. Rejecting it also keeps
	// UtxoSwap.CoordinatorIdentityPublicKey == self impossible on participant rows,
	// which the complete_utxo_swap sweep relies on to never touch consensus swaps.
	if coordinator.Identifier == h.config.Identifier {
		return nil, fmt.Errorf("consensus prepare for op type %d declares the receiving SO (coordinator_index %d) as coordinator; coordinators prepare locally and never call ConsensusPrepare on themselves", opType, coordinatorIndex)
	}
	ctx = consensus.WithCoordinatorIdentity(ctx, coordinator.IdentityPublicKey)

	// Prepare-side match to validateDecisionAgainstPreparedOp's empty-id
	// fail-closed: a bound flow with no flow_execution_id writes no participant
	// row (the write below is skipped for an empty id) and the decision-side fence
	// rejects an empty id, so running Prepare would lock resources with nothing to
	// commit, roll back, or reconcile against. Every live coordinator populates the
	// id (post-#6288), so an empty id on a bound op is forged or misrouted. Reject
	// (rather than run Prepare) — and reject with an ERROR, not a nil result:
	// unlike the gossip decision path (where an error would loop redelivery), a
	// ConsensusPrepare RPC error drives the coordinator's rollback path, whereas a
	// (nil, nil) return reads as a successful prepare and could let the coordinator
	// record and broadcast COMMITTED for the other participants.
	if _, bound := handler.(consensus.PrepareBoundFlowHandler); bound && flowExecutionID == "" {
		return nil, fmt.Errorf("consensus prepare for bound op type %d requires a flow_execution_id; refusing to prepare a bound flow with no row to commit/rollback/reconcile against", opType)
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
