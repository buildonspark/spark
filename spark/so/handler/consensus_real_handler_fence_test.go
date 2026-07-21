package handler

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/keys"
	pbgossip "github.com/lightsparkdev/spark/proto/gossip"
	pb "github.com/lightsparkdev/spark/proto/spark"
	pbinternal "github.com/lightsparkdev/spark/proto/spark_internal"
	"github.com/lightsparkdev/spark/so/consensus"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	sparktesting "github.com/lightsparkdev/spark/testing"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
)

// seedRealFencePrepareRow persists an IN_FLIGHT participant FlowExecution row
// whose prepare payload wraps prepareMsg (the marshalled-Any shape
// DispatchPrepare persists) for opType, so a decision dispatched via
// runConsensusCommit/runConsensusRollback runs the *real* handler's
// ValidateDecisionAgainstPrepare against it.
func seedRealFencePrepareRow(t *testing.T, ctx context.Context, opType pbgossip.ConsensusOperationType, prepareMsg proto.Message) uuid.UUID {
	t.Helper()
	dbClient, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	prepareAny, err := anypb.New(prepareMsg)
	require.NoError(t, err)
	prepareBytes, err := proto.Marshal(prepareAny)
	require.NoError(t, err)

	flowID := uuid.New()
	_, err = dbClient.FlowExecution.Create().
		SetID(flowID).
		SetRole(st.FlowExecutionRoleParticipant).
		SetOpType(int32(opType)).
		SetCoordinatorIndex(1).
		SetPreparePayload(prepareBytes).
		Save(ctx)
	require.NoError(t, err)
	return flowID
}

// TestConsensusDecisionFence_RealHandlersFenceForeignPayload proves the binding
// fence end-to-end through the real production handlers: with a matching
// flow_execution_id but a decision payload naming a different resource than the
// persisted prepare op, the dispatch path (runConsensusRollback/Commit →
// validateDecisionAgainstPreparedOp → handler.ValidateDecisionAgainstPrepare)
// fences the decision — the handler's Commit/Rollback never runs and the row
// stays IN_FLIGHT. This complements the direct ValidateDecisionAgainstPrepare
// unit tests and the synthetic prepareBoundFakeFlowHandler dispatch tests by
// exercising the actual wiring of each real handler through the fence.
func TestConsensusDecisionFence_RealHandlersFenceForeignPayload(t *testing.T) {
	cfg := sparktesting.TestConfig(t)
	preparedID := uuid.NewString()
	foreignID := uuid.NewString()
	receiver := keys.GeneratePrivateKey().Public().Serialize()

	cases := []struct {
		name          string
		opType        pbgossip.ConsensusOperationType
		handler       consensus.FlowHandler
		prepare       proto.Message
		foreignCommit proto.Message
		foreignRoll   proto.Message
	}{
		{
			name:          "send_transfer",
			opType:        pbgossip.ConsensusOperationType_CONSENSUS_OPERATION_TYPE_SEND_TRANSFER,
			handler:       NewSendTransferFlowHandler(cfg),
			prepare:       &pbinternal.SendTransferPrepareRequest{OriginalRequest: &pb.StartTransferV3Request{TransferId: preparedID}},
			foreignCommit: &pbinternal.SendTransferCommitRequest{TransferId: foreignID},
			foreignRoll:   &pbinternal.SendTransferRollbackRequest{TransferId: foreignID},
		},
		{
			name:          "claim_transfer",
			opType:        pbgossip.ConsensusOperationType_CONSENSUS_OPERATION_TYPE_CLAIM_TRANSFER,
			handler:       NewClaimTransferFlowHandler(cfg),
			prepare:       &pbinternal.ClaimTransferPrepareRequest{OriginalRequest: &pb.ClaimTransferRequest{TransferId: preparedID, OwnerIdentityPublicKey: receiver}},
			foreignCommit: &pbinternal.ClaimTransferCommitRequest{TransferId: foreignID, ReceiverIdentityPublicKey: receiver},
			foreignRoll:   &pbinternal.ClaimTransferRollbackRequest{TransferId: foreignID, ReceiverIdentityPublicKey: receiver},
		},
		{
			name:          "coop_exit",
			opType:        pbgossip.ConsensusOperationType_CONSENSUS_OPERATION_TYPE_COOP_EXIT,
			handler:       NewCoopExitFlowHandler(cfg),
			prepare:       &pbinternal.CoopExitPrepareRequest{OriginalRequest: &pb.CooperativeExitRequest{Transfer: &pb.StartTransferRequest{TransferId: preparedID}}},
			foreignCommit: &pbinternal.CoopExitCommitRequest{TransferId: foreignID},
			foreignRoll:   &pbinternal.CoopExitRollbackRequest{TransferId: foreignID},
		},
		{
			name:          "initiate_preimage_swap",
			opType:        pbgossip.ConsensusOperationType_CONSENSUS_OPERATION_TYPE_INITIATE_PREIMAGE_SWAP,
			handler:       NewInitiatePreimageSwapFlowHandler(cfg),
			prepare:       &pbinternal.InitiatePreimageSwapPrepareRequest{OriginalRequest: &pb.InitiatePreimageSwapRequest{TransferRequest: &pb.StartTransferRequest{TransferId: preparedID}}},
			foreignCommit: &pbinternal.InitiatePreimageSwapCommitRequest{TransferId: foreignID},
			foreignRoll:   &pbinternal.InitiatePreimageSwapRollbackRequest{TransferId: foreignID},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Both phases: a foreign decision (matching flow id, mismatched
			// payload) must be skipped by the fence — no error to loop, handler not
			// invoked, row still IN_FLIGHT for the real decision to land. Commit is
			// where a money-losing bug would hide, so it is covered too.
			t.Run("rollback", func(t *testing.T) {
				ctx, _ := db.ConnectToTestPostgres(t)
				flowID := seedRealFencePrepareRow(t, ctx, tc.opType, tc.prepare)
				require.NoError(t, runConsensusRollback(ctx, tc.handler, tc.opType, flowID.String(), tc.foreignRoll),
					"a foreign rollback payload must be skipped, not errored")
				assertFenceRowStatus(t, ctx, flowID, st.FlowExecutionStatusInFlight)
			})
			t.Run("commit", func(t *testing.T) {
				ctx, _ := db.ConnectToTestPostgres(t)
				flowID := seedRealFencePrepareRow(t, ctx, tc.opType, tc.prepare)
				require.NoError(t, runConsensusCommit(ctx, tc.handler, tc.opType, flowID.String(), tc.foreignCommit),
					"a foreign commit payload must be skipped, not errored")
				assertFenceRowStatus(t, ctx, flowID, st.FlowExecutionStatusInFlight)
			})
		})
	}
}

// TestConsensusDecisionFence_ClaimForeignReceiverFenced drives claim's
// receiver-identity binding — a security property distinct from the transfer_id
// bind — through the real dispatch path. The decision carries the PREPARED
// transfer_id but a foreign receiver_identity_public_key (which selects a
// different MIMO TransferReceiver row), so the fence must skip it: no error, and
// the row stays IN_FLIGHT. The table above only varies transfer_id, so this
// closes that gap end-to-end (the receiver bind was otherwise exercised only in
// the direct ValidateDecisionAgainstPrepare unit test).
func TestConsensusDecisionFence_ClaimForeignReceiverFenced(t *testing.T) {
	cfg := sparktesting.TestConfig(t)
	handler := NewClaimTransferFlowHandler(cfg)
	opType := pbgossip.ConsensusOperationType_CONSENSUS_OPERATION_TYPE_CLAIM_TRANSFER
	transferID := uuid.NewString()
	preparedReceiver := keys.GeneratePrivateKey().Public().Serialize()
	foreignReceiver := keys.GeneratePrivateKey().Public().Serialize()
	prepare := &pbinternal.ClaimTransferPrepareRequest{
		OriginalRequest: &pb.ClaimTransferRequest{TransferId: transferID, OwnerIdentityPublicKey: preparedReceiver},
	}

	t.Run("rollback", func(t *testing.T) {
		ctx, _ := db.ConnectToTestPostgres(t)
		flowID := seedRealFencePrepareRow(t, ctx, opType, prepare)
		require.NoError(t, runConsensusRollback(ctx, handler, opType, flowID.String(),
			&pbinternal.ClaimTransferRollbackRequest{TransferId: transferID, ReceiverIdentityPublicKey: foreignReceiver}),
			"a foreign receiver (matching transfer_id) must be skipped, not errored")
		assertFenceRowStatus(t, ctx, flowID, st.FlowExecutionStatusInFlight)
	})
	t.Run("commit", func(t *testing.T) {
		ctx, _ := db.ConnectToTestPostgres(t)
		flowID := seedRealFencePrepareRow(t, ctx, opType, prepare)
		require.NoError(t, runConsensusCommit(ctx, handler, opType, flowID.String(),
			&pbinternal.ClaimTransferCommitRequest{TransferId: transferID, ReceiverIdentityPublicKey: foreignReceiver}),
			"a foreign receiver (matching transfer_id) must be skipped, not errored")
		assertFenceRowStatus(t, ctx, flowID, st.FlowExecutionStatusInFlight)
	})
}
