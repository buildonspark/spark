package handler

import (
	"context"
	"errors"
	"testing"

	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/google/uuid"
	pbgossip "github.com/lightsparkdev/spark/proto/gossip"
	pb "github.com/lightsparkdev/spark/proto/spark"
	pbinternal "github.com/lightsparkdev/spark/proto/spark_internal"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	sparkerrors "github.com/lightsparkdev/spark/so/errors"
	sparktesting "github.com/lightsparkdev/spark/testing"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
)

func TestHandleCancelTransferGossipMessage_NonExistentTransfer_Succeeds(t *testing.T) {
	config := sparktesting.TestConfig(t)
	ctx, _ := db.ConnectToTestPostgres(t)

	handler := NewGossipHandler(config)

	nonExistentTransferID := uuid.New()
	cancelTransfer := &pbgossip.GossipMessageCancelTransfer{
		TransferId: nonExistentTransferID.String(),
	}

	err := handler.handleCancelTransferGossipMessage(ctx, cancelTransfer)

	require.NoError(t, err, "cancelling a non-existent transfer should succeed")
}

func TestHandleCancelTransferGossipMessage_InvalidTransferID_ReturnsError(t *testing.T) {
	config := sparktesting.TestConfig(t)
	ctx := t.Context()

	handler := NewGossipHandler(config)

	cancelTransfer := &pbgossip.GossipMessageCancelTransfer{
		TransferId: "not-a-valid-uuid",
	}

	err := handler.handleCancelTransferGossipMessage(ctx, cancelTransfer)

	require.Error(t, err, "cancelling with a malformed transfer ID should return an error")
}

func TestHandleRollbackTransfer_NonExistentTransfer_Succeeds(t *testing.T) {
	config := sparktesting.TestConfig(t)
	ctx, _ := db.ConnectToTestPostgres(t)

	handler := NewGossipHandler(config)

	nonExistentTransferID := uuid.New()
	rollbackTransfer := &pbgossip.GossipMessageRollbackTransfer{
		TransferId: nonExistentTransferID.String(),
	}

	err := handler.handleRollbackTransfer(ctx, rollbackTransfer)

	require.NoError(t, err, "rolling back a non-existent transfer should succeed")
}

func TestHandleRollbackTransfer_InvalidTransferID_ReturnsError(t *testing.T) {
	config := sparktesting.TestConfig(t)
	ctx := t.Context()

	handler := NewGossipHandler(config)

	rollbackTransfer := &pbgossip.GossipMessageRollbackTransfer{
		TransferId: "not-a-valid-uuid",
	}

	err := handler.handleRollbackTransfer(ctx, rollbackTransfer)

	require.Error(t, err, "rolling back with a malformed transfer ID should return an error")
}

func TestHandleSettleSenderKeyTweakGossipMessage_InvalidTransferID_ReturnsError(t *testing.T) {
	config := sparktesting.TestConfig(t)
	ctx := t.Context()

	handler := NewGossipHandler(config)

	settleSenderKeyTweak := &pbgossip.GossipMessageSettleSenderKeyTweak{
		TransferId: "not-a-valid-uuid",
	}

	err := handler.handleSettleSenderKeyTweakGossipMessage(ctx, settleSenderKeyTweak)

	require.Error(t, err, "settling sender key tweak with a malformed transfer ID should return an error")
}

func TestHandleRollbackUtxoSwapGossipMessage_NonExistentUtxo_Succeeds(t *testing.T) {
	ctx, _ := db.ConnectToTestPostgres(t)
	cfg := setUpTestConfigWithRegtestNoAuthz(t)
	handler := NewGossipHandler(cfg)

	nonExistentTxid := chainhash.DoubleHashB([]byte("nonexistent_txid_for_gossip_test"))
	rollbackRequest, err := GenerateRollbackStaticDepositUtxoSwapForUtxoRequest(ctx, cfg, &pb.UTXO{
		Txid:    nonExistentTxid,
		Vout:    0,
		Network: pb.Network_REGTEST,
	}, nil)
	require.NoError(t, err)

	gossipMsg := &pbgossip.GossipMessageRollbackUtxoSwap{
		OnChainUtxo:          rollbackRequest.OnChainUtxo,
		Signature:            rollbackRequest.Signature,
		CoordinatorPublicKey: rollbackRequest.CoordinatorPublicKey,
	}

	err = handler.handleRollbackUtxoSwapGossipMessage(ctx, gossipMsg)
	require.NoError(t, err, "rolling back a non-existent UTXO should succeed")
}

// --- Consensus commit / rollback row transitions ---

// sessionClient returns the Ent client backed by the same session-managed
// transaction the handlers use (via ent.GetDbFromContext). Tests must insert
// setup rows and read back via this client so writes are visible across
// handler boundaries without needing explicit commits.
func sessionClient(t *testing.T, ctx context.Context) *ent.Client {
	t.Helper()
	tx, err := ent.GetTxFromContext(ctx)
	require.NoError(t, err)
	return tx.Client()
}

// insertParticipantRow inserts a PARTICIPANT FlowExecution row keyed by id
// in IN_FLIGHT status. The op_type is fixed to STORE_PREIMAGE_SHARE because
// that flow's Commit and Rollback are no-ops, so the tests focus on the row
// transition rather than any domain-specific commit/rollback effect.
func insertParticipantRow(t *testing.T, ctx context.Context, id uuid.UUID) *ent.FlowExecution {
	t.Helper()
	row, err := sessionClient(t, ctx).FlowExecution.Create().
		SetID(id).
		SetRole(st.FlowExecutionRoleParticipant).
		SetOpType(int32(pbgossip.ConsensusOperationType_CONSENSUS_OPERATION_TYPE_STORE_PREIMAGE_SHARE)).
		SetCoordinatorIndex(1).
		Save(ctx)
	require.NoError(t, err)
	return row
}

// consensusCommitMessage builds a GossipMessage carrying a ConsensusCommit for
// the STORE_PREIMAGE_SHARE flow (no-op Commit) with the provided execution id.
func consensusCommitMessage(t *testing.T, executionID string) *pbgossip.GossipMessage {
	t.Helper()
	opAny, err := anypb.New(&pbinternal.StorePreimageSharePrepareRequest{})
	require.NoError(t, err)
	return &pbgossip.GossipMessage{
		MessageId: uuid.NewString(),
		Message: &pbgossip.GossipMessage_ConsensusCommit{
			ConsensusCommit: &pbgossip.GossipMessageConsensusCommit{
				OpType:          pbgossip.ConsensusOperationType_CONSENSUS_OPERATION_TYPE_STORE_PREIMAGE_SHARE,
				Operation:       opAny,
				FlowExecutionId: executionID,
			},
		},
	}
}

// consensusRollbackMessage mirrors consensusCommitMessage for the rollback side.
func consensusRollbackMessage(t *testing.T, executionID string) *pbgossip.GossipMessage {
	t.Helper()
	opAny, err := anypb.New(&pbinternal.StorePreimageSharePrepareRequest{})
	require.NoError(t, err)
	return &pbgossip.GossipMessage{
		MessageId: uuid.NewString(),
		Message: &pbgossip.GossipMessage_ConsensusRollback{
			ConsensusRollback: &pbgossip.GossipMessageConsensusRollback{
				OpType:          pbgossip.ConsensusOperationType_CONSENSUS_OPERATION_TYPE_STORE_PREIMAGE_SHARE,
				Operation:       opAny,
				FlowExecutionId: executionID,
			},
		},
	}
}

func TestHandleGossipMessage_ConsensusCommit_TransitionsParticipantRowToCommitted(t *testing.T) {
	ctx, _ := db.ConnectToTestPostgres(t)
	row := insertParticipantRow(t, ctx, uuid.New())

	h := NewGossipHandler(sparktesting.TestConfig(t))
	err := h.HandleGossipMessage(ctx, consensusCommitMessage(t, row.ID.String()), false /* forCoordinator */)
	require.NoError(t, err)

	updated, err := sessionClient(t, ctx).FlowExecution.Get(ctx, row.ID)
	require.NoError(t, err)
	assert.Equal(t, st.FlowExecutionStatusCommitted, updated.Status)
}

func TestHandleGossipMessage_ConsensusRollback_TransitionsParticipantRowToRolledBack(t *testing.T) {
	ctx, _ := db.ConnectToTestPostgres(t)
	row := insertParticipantRow(t, ctx, uuid.New())

	h := NewGossipHandler(sparktesting.TestConfig(t))
	err := h.HandleGossipMessage(ctx, consensusRollbackMessage(t, row.ID.String()), false)
	require.NoError(t, err)

	updated, err := sessionClient(t, ctx).FlowExecution.Get(ctx, row.ID)
	require.NoError(t, err)
	assert.Equal(t, st.FlowExecutionStatusRolledBack, updated.Status)
}

func TestHandleGossipMessage_ConsensusCommit_RedeliveredGossipIsIdempotent(t *testing.T) {
	ctx, _ := db.ConnectToTestPostgres(t)
	row := insertParticipantRow(t, ctx, uuid.New())
	h := NewGossipHandler(sparktesting.TestConfig(t))

	// First delivery transitions to COMMITTED.
	require.NoError(t, h.HandleGossipMessage(ctx, consensusCommitMessage(t, row.ID.String()), false))
	// Redelivery is a no-op and must not return an error.
	require.NoError(t, h.HandleGossipMessage(ctx, consensusCommitMessage(t, row.ID.String()), false))

	updated, err := sessionClient(t, ctx).FlowExecution.Get(ctx, row.ID)
	require.NoError(t, err)
	assert.Equal(t, st.FlowExecutionStatusCommitted, updated.Status)
}

func TestHandleGossipMessage_ConsensusCommit_MissingRowIsNoOp(t *testing.T) {
	ctx, _ := db.ConnectToTestPostgres(t)

	h := NewGossipHandler(sparktesting.TestConfig(t))
	err := h.HandleGossipMessage(ctx, consensusCommitMessage(t, uuid.NewString()), false)
	require.NoError(t, err, "missing FlowExecution row should be tolerated (pre-upgrade rollout)")
}

func TestHandleGossipMessage_ConsensusCommit_EmptyExecutionIDIsNoOp(t *testing.T) {
	ctx, _ := db.ConnectToTestPostgres(t)

	// Any existing row must remain untouched when the gossip carries no id.
	row := insertParticipantRow(t, ctx, uuid.New())

	h := NewGossipHandler(sparktesting.TestConfig(t))
	err := h.HandleGossipMessage(ctx, consensusCommitMessage(t, "" /* empty id */), false)
	require.NoError(t, err)

	unchanged, err := sessionClient(t, ctx).FlowExecution.Get(ctx, row.ID)
	require.NoError(t, err)
	assert.Equal(t, st.FlowExecutionStatusInFlight, unchanged.Status)
}

func TestHandleGossipMessage_ConsensusCommit_AtCoordinatorIsSkippedAndRowUntouched(t *testing.T) {
	ctx, _ := db.ConnectToTestPostgres(t)

	// Even if a row exists under the same id, the coordinator-side path
	// (forCoordinator=true) never transitions participant rows — the
	// coordinator already marked its COORDINATOR row terminal before sending.
	row := insertParticipantRow(t, ctx, uuid.New())

	h := NewGossipHandler(sparktesting.TestConfig(t))
	err := h.HandleGossipMessage(ctx, consensusCommitMessage(t, row.ID.String()), true /* forCoordinator */)
	require.NoError(t, err)

	unchanged, err := sessionClient(t, ctx).FlowExecution.Get(ctx, row.ID)
	require.NoError(t, err)
	assert.Equal(t, st.FlowExecutionStatusInFlight, unchanged.Status)
}

// --- runConsensusCommit / runConsensusRollback: AlreadyExists-as-success rule ---

// stubFlowHandler is a consensus.FlowHandler whose Commit and Rollback
// return pre-set errors. Used to exercise the dispatch wrappers without
// pulling in real handler side effects.
type stubFlowHandler struct {
	commitErr   error
	rollbackErr error
}

func (s *stubFlowHandler) Prepare(_ context.Context, _ proto.Message) (proto.Message, error) {
	return nil, nil
}
func (s *stubFlowHandler) Commit(_ context.Context, _ proto.Message) error   { return s.commitErr }
func (s *stubFlowHandler) Rollback(_ context.Context, _ proto.Message) error { return s.rollbackErr }

func TestRunConsensusCommit_AlreadyExists_MarksRowCommitted(t *testing.T) {
	ctx, _ := db.ConnectToTestPostgres(t)
	row := insertParticipantRow(t, ctx, uuid.New())

	staleErr := sparkerrors.AlreadyExistsDuplicateOperation(errors.New("stale finalize"))
	h := &stubFlowHandler{commitErr: staleErr}

	err := runConsensusCommit(ctx, h,
		pbgossip.ConsensusOperationType_CONSENSUS_OPERATION_TYPE_STORE_PREIMAGE_SHARE,
		row.ID.String(),
		&pbinternal.StorePreimageSharePrepareRequest{},
	)
	require.NoError(t, err, "AlreadyExists from handler.Commit must be treated as success")

	updated, err := sessionClient(t, ctx).FlowExecution.Get(ctx, row.ID)
	require.NoError(t, err)
	assert.Equal(t, st.FlowExecutionStatusCommitted, updated.Status,
		"row must transition to COMMITTED when the handler reports AlreadyExists")
}

func TestRunConsensusCommit_NonAlreadyExistsError_PropagatesAndLeavesRowInFlight(t *testing.T) {
	ctx, _ := db.ConnectToTestPostgres(t)
	row := insertParticipantRow(t, ctx, uuid.New())

	internalErr := sparkerrors.InternalDatabaseWriteError(errors.New("disk full"))
	h := &stubFlowHandler{commitErr: internalErr}

	err := runConsensusCommit(ctx, h,
		pbgossip.ConsensusOperationType_CONSENSUS_OPERATION_TYPE_STORE_PREIMAGE_SHARE,
		row.ID.String(),
		&pbinternal.StorePreimageSharePrepareRequest{},
	)
	require.Error(t, err, "non-AlreadyExists handler errors must propagate")

	unchanged, err := sessionClient(t, ctx).FlowExecution.Get(ctx, row.ID)
	require.NoError(t, err)
	assert.Equal(t, st.FlowExecutionStatusInFlight, unchanged.Status,
		"row must stay IN_FLIGHT when the handler returns a non-AlreadyExists error")
}

func TestRunConsensusRollback_AlreadyExists_MarksRowRolledBack(t *testing.T) {
	ctx, _ := db.ConnectToTestPostgres(t)
	row := insertParticipantRow(t, ctx, uuid.New())

	staleErr := sparkerrors.AlreadyExistsDuplicateOperation(errors.New("already rolled back"))
	h := &stubFlowHandler{rollbackErr: staleErr}

	err := runConsensusRollback(ctx, h,
		pbgossip.ConsensusOperationType_CONSENSUS_OPERATION_TYPE_STORE_PREIMAGE_SHARE,
		row.ID.String(),
		&pbinternal.StorePreimageSharePrepareRequest{},
	)
	require.NoError(t, err, "AlreadyExists from handler.Rollback must be treated as success")

	updated, err := sessionClient(t, ctx).FlowExecution.Get(ctx, row.ID)
	require.NoError(t, err)
	assert.Equal(t, st.FlowExecutionStatusRolledBack, updated.Status,
		"row must transition to ROLLED_BACK when the handler reports AlreadyExists")
}
