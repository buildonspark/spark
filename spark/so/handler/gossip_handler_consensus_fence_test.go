package handler

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/google/uuid"
	pbgossip "github.com/lightsparkdev/spark/proto/gossip"
	pb "github.com/lightsparkdev/spark/proto/spark"
	pbinternal "github.com/lightsparkdev/spark/proto/spark_internal"
	"github.com/lightsparkdev/spark/so/consensus"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
)

// prepareBoundFakeFlowHandler implements consensus.FlowHandler plus
// PrepareBoundFlowHandler, recording invocations so the tests can assert
// whether the payload fence let the decision through. Its binding rule
// mirrors the real flows': the decision's transfer id must match the
// prepared transfer id.
type prepareBoundFakeFlowHandler struct {
	commitCalls   int
	rollbackCalls int
	validateErr   error
}

var (
	_ consensus.FlowHandler             = (*prepareBoundFakeFlowHandler)(nil)
	_ consensus.PrepareBoundFlowHandler = (*prepareBoundFakeFlowHandler)(nil)
)

func (h *prepareBoundFakeFlowHandler) Prepare(context.Context, proto.Message) (proto.Message, error) {
	return nil, nil
}

func (h *prepareBoundFakeFlowHandler) Commit(context.Context, proto.Message) error {
	h.commitCalls++
	return nil
}

func (h *prepareBoundFakeFlowHandler) Rollback(context.Context, proto.Message) error {
	h.rollbackCalls++
	return nil
}

func (h *prepareBoundFakeFlowHandler) ValidateDecisionAgainstPrepare(prepareOp proto.Message, decisionOp proto.Message) error {
	if h.validateErr != nil {
		return h.validateErr
	}
	prepare, ok := prepareOp.(*pbinternal.SendTransferPrepareRequest)
	if !ok {
		return fmt.Errorf("unexpected prepare op type %T", prepareOp)
	}
	var decisionTransferID string
	switch d := decisionOp.(type) {
	case *pbinternal.SendTransferCommitRequest:
		decisionTransferID = d.GetTransferId()
	case *pbinternal.SendTransferRollbackRequest:
		decisionTransferID = d.GetTransferId()
	case *pbinternal.SendTransferPrepareRequest:
		// The reconciler's presumed-abort path echoes the prepare op itself as
		// the decision.
		decisionTransferID = d.GetOriginalRequest().GetTransferId()
	default:
		return fmt.Errorf("unexpected decision op type %T", decisionOp)
	}
	if prepare.GetOriginalRequest().GetTransferId() != decisionTransferID {
		return fmt.Errorf("decision transfer id %s does not match prepared transfer id", decisionTransferID)
	}
	return nil
}

// unboundFakeFlowHandler does NOT implement PrepareBoundFlowHandler; flows
// without the optional interface must dispatch exactly as before.
type unboundFakeFlowHandler struct {
	rollbackCalls int
}

var _ consensus.FlowHandler = (*unboundFakeFlowHandler)(nil)

func (h *unboundFakeFlowHandler) Prepare(context.Context, proto.Message) (proto.Message, error) {
	return nil, nil
}
func (h *unboundFakeFlowHandler) Commit(context.Context, proto.Message) error { return nil }
func (h *unboundFakeFlowHandler) Rollback(context.Context, proto.Message) error {
	h.rollbackCalls++
	return nil
}

// seedFenceParticipantRow inserts an IN_FLIGHT participant FlowExecution row
// whose prepare payload wraps a SendTransferPrepareRequest for transferID —
// the same marshalled-Any shape DispatchPrepare persists.
func seedFenceParticipantRow(t *testing.T, ctx context.Context, transferID string) uuid.UUID {
	t.Helper()
	dbClient, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	prepareAny, err := anypb.New(&pbinternal.SendTransferPrepareRequest{
		OriginalRequest: &pb.StartTransferV3Request{TransferId: transferID},
	})
	require.NoError(t, err)
	prepareBytes, err := proto.Marshal(prepareAny)
	require.NoError(t, err)

	flowID := uuid.New()
	_, err = dbClient.FlowExecution.Create().
		SetID(flowID).
		SetRole(st.FlowExecutionRoleParticipant).
		SetOpType(int32(pbgossip.ConsensusOperationType_CONSENSUS_OPERATION_TYPE_SEND_TRANSFER)).
		SetCoordinatorIndex(1).
		SetPreparePayload(prepareBytes).
		Save(ctx)
	require.NoError(t, err)
	return flowID
}

func assertFenceRowStatus(t *testing.T, ctx context.Context, flowID uuid.UUID, want st.FlowExecutionStatus) {
	t.Helper()
	dbClient, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)
	row, err := dbClient.FlowExecution.Get(ctx, flowID)
	require.NoError(t, err)
	assert.Equal(t, want, row.Status)
}

// TestConsensusDecisionFence_RollbackPayloadBinding proves the
// PrepareBoundFlowHandler fence: a rollback whose payload disagrees with the
// prepare op this SO persisted is skipped (handler not invoked) and the
// participant row stays IN_FLIGHT so the flow's real decision can still land;
// a matching payload dispatches normally and the row goes terminal.
func TestConsensusDecisionFence_RollbackPayloadBinding(t *testing.T) {
	ctx, _ := db.ConnectToTestPostgres(t)

	preparedTransferID := uuid.NewString()
	flowID := seedFenceParticipantRow(t, ctx, preparedTransferID)
	handler := &prepareBoundFakeFlowHandler{}

	err := runConsensusRollback(ctx, handler,
		pbgossip.ConsensusOperationType_CONSENSUS_OPERATION_TYPE_SEND_TRANSFER,
		flowID.String(),
		&pbinternal.SendTransferRollbackRequest{TransferId: uuid.NewString()})
	require.NoError(t, err)
	require.Equal(t, 0, handler.rollbackCalls, "mismatched rollback payload must not reach the handler")
	assertFenceRowStatus(t, ctx, flowID, st.FlowExecutionStatusInFlight)

	err = runConsensusRollback(ctx, handler,
		pbgossip.ConsensusOperationType_CONSENSUS_OPERATION_TYPE_SEND_TRANSFER,
		flowID.String(),
		&pbinternal.SendTransferRollbackRequest{TransferId: preparedTransferID})
	require.NoError(t, err)
	require.Equal(t, 1, handler.rollbackCalls, "matching rollback payload must dispatch")
	assertFenceRowStatus(t, ctx, flowID, st.FlowExecutionStatusRolledBack)
}

// TestConsensusDecisionFence_CommitPayloadBinding mirrors the rollback test
// for the commit path: a mismatched commit payload is fenced (row stays
// IN_FLIGHT), and a matching commit payload dispatches through the fence and
// drives the row terminal.
func TestConsensusDecisionFence_CommitPayloadBinding(t *testing.T) {
	ctx, _ := db.ConnectToTestPostgres(t)

	// Mismatch → fenced.
	flowID := seedFenceParticipantRow(t, ctx, uuid.NewString())
	rejectHandler := &prepareBoundFakeFlowHandler{validateErr: errors.New("payload does not match")}
	err := runConsensusCommit(ctx, rejectHandler,
		pbgossip.ConsensusOperationType_CONSENSUS_OPERATION_TYPE_SEND_TRANSFER,
		flowID.String(),
		&pbinternal.SendTransferRollbackRequest{TransferId: uuid.NewString()})
	require.NoError(t, err)
	require.Equal(t, 0, rejectHandler.commitCalls, "rejected commit payload must not reach the handler")
	assertFenceRowStatus(t, ctx, flowID, st.FlowExecutionStatusInFlight)

	// Match → dispatches through the fence and marks the row COMMITTED.
	preparedTransferID := uuid.NewString()
	matchFlowID := seedFenceParticipantRow(t, ctx, preparedTransferID)
	matchHandler := &prepareBoundFakeFlowHandler{}
	err = runConsensusCommit(ctx, matchHandler,
		pbgossip.ConsensusOperationType_CONSENSUS_OPERATION_TYPE_SEND_TRANSFER,
		matchFlowID.String(),
		&pbinternal.SendTransferCommitRequest{TransferId: preparedTransferID})
	require.NoError(t, err)
	require.Equal(t, 1, matchHandler.commitCalls, "matching commit payload must dispatch")
	assertFenceRowStatus(t, ctx, matchFlowID, st.FlowExecutionStatusCommitted)
}

// TestConsensusDecisionFence_MissingPreparePayloadFailsClosed proves a bound
// flow whose participant row carries no persisted prepare payload (an anomaly
// — DispatchPrepare always persists one) fails closed: the decision is not
// dispatched and the row stays IN_FLIGHT, rather than being treated as a
// passing bind check.
func TestConsensusDecisionFence_MissingPreparePayloadFailsClosed(t *testing.T) {
	ctx, _ := db.ConnectToTestPostgres(t)
	dbClient, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	flowID := uuid.New()
	_, err = dbClient.FlowExecution.Create().
		SetID(flowID).
		SetRole(st.FlowExecutionRoleParticipant).
		SetOpType(int32(pbgossip.ConsensusOperationType_CONSENSUS_OPERATION_TYPE_SEND_TRANSFER)).
		SetCoordinatorIndex(1).
		Save(ctx) // no SetPreparePayload — the anomalous empty-payload row
	require.NoError(t, err)

	handler := &prepareBoundFakeFlowHandler{}
	err = runConsensusRollback(ctx, handler,
		pbgossip.ConsensusOperationType_CONSENSUS_OPERATION_TYPE_SEND_TRANSFER,
		flowID.String(),
		&pbinternal.SendTransferRollbackRequest{TransferId: uuid.NewString()})
	require.NoError(t, err)
	require.Equal(t, 0, handler.rollbackCalls, "a bound flow with no prepare payload must not dispatch")
	assertFenceRowStatus(t, ctx, flowID, st.FlowExecutionStatusInFlight)
}

// TestConsensusDecisionFence_EmptyFlowIdBoundFailsClosed proves a bound flow
// whose decision arrives with an empty flow_execution_id (row comes back nil,
// bypassing the row lookup / op-type / terminal / binding fences) fails closed
// rather than dispatching.
func TestConsensusDecisionFence_EmptyFlowIdBoundFailsClosed(t *testing.T) {
	ctx, _ := db.ConnectToTestPostgres(t)
	handler := &prepareBoundFakeFlowHandler{}

	err := runConsensusRollback(ctx, handler,
		pbgossip.ConsensusOperationType_CONSENSUS_OPERATION_TYPE_SEND_TRANSFER,
		"", // empty flow_execution_id
		&pbinternal.SendTransferRollbackRequest{TransferId: uuid.NewString()})
	require.NoError(t, err)
	require.Equal(t, 0, handler.rollbackCalls, "a bound flow with an empty flow_execution_id must not dispatch")
}

// TestConsensusDecisionFence_PresumedAbortPrepareShape proves the reconciler's
// presumed-abort dispatch, where the decision op is the persisted prepare
// shape (not the canonical rollback shape), is bound and dispatched when it
// matches — and skipped when it names a different transfer.
func TestConsensusDecisionFence_PresumedAbortPrepareShape(t *testing.T) {
	ctx, _ := db.ConnectToTestPostgres(t)

	preparedTransferID := uuid.NewString()
	flowID := seedFenceParticipantRow(t, ctx, preparedTransferID)
	handler := &prepareBoundFakeFlowHandler{}

	// Matching prepare-shape decision (the presumed-abort echo) dispatches.
	err := runConsensusRollback(ctx, handler,
		pbgossip.ConsensusOperationType_CONSENSUS_OPERATION_TYPE_SEND_TRANSFER,
		flowID.String(),
		&pbinternal.SendTransferPrepareRequest{
			OriginalRequest: &pb.StartTransferV3Request{TransferId: preparedTransferID},
		})
	require.NoError(t, err)
	require.Equal(t, 1, handler.rollbackCalls, "matching prepare-shape decision must dispatch")
	assertFenceRowStatus(t, ctx, flowID, st.FlowExecutionStatusRolledBack)

	// A prepare-shape decision naming a different transfer is skipped.
	flowID2 := seedFenceParticipantRow(t, ctx, preparedTransferID)
	handler2 := &prepareBoundFakeFlowHandler{}
	err = runConsensusRollback(ctx, handler2,
		pbgossip.ConsensusOperationType_CONSENSUS_OPERATION_TYPE_SEND_TRANSFER,
		flowID2.String(),
		&pbinternal.SendTransferPrepareRequest{
			OriginalRequest: &pb.StartTransferV3Request{TransferId: uuid.NewString()},
		})
	require.NoError(t, err)
	require.Equal(t, 0, handler2.rollbackCalls, "mismatched prepare-shape decision must not dispatch")
	assertFenceRowStatus(t, ctx, flowID2, st.FlowExecutionStatusInFlight)
}

// TestConsensusDecisionFence_NonBoundHandlerUnaffected proves flows that do
// not implement PrepareBoundFlowHandler dispatch exactly as before.
func TestConsensusDecisionFence_NonBoundHandlerUnaffected(t *testing.T) {
	ctx, _ := db.ConnectToTestPostgres(t)

	flowID := seedFenceParticipantRow(t, ctx, uuid.NewString())
	handler := &unboundFakeFlowHandler{}

	err := runConsensusRollback(ctx, handler,
		pbgossip.ConsensusOperationType_CONSENSUS_OPERATION_TYPE_SEND_TRANSFER,
		flowID.String(),
		&pbinternal.SendTransferRollbackRequest{TransferId: uuid.NewString()})
	require.NoError(t, err)
	require.Equal(t, 1, handler.rollbackCalls)
	assertFenceRowStatus(t, ctx, flowID, st.FlowExecutionStatusRolledBack)
}
