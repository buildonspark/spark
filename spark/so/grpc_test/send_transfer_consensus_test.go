package grpctest

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/keys"
	pbgossip "github.com/lightsparkdev/spark/proto/gossip"
	sparkpb "github.com/lightsparkdev/spark/proto/spark"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	transferent "github.com/lightsparkdev/spark/so/ent/transfer"
	"github.com/lightsparkdev/spark/so/knobs"
	sparktesting "github.com/lightsparkdev/spark/testing"
	"github.com/lightsparkdev/spark/testing/wallet"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// opTypeSendTransfer is the int32 value of CONSENSUS_OPERATION_TYPE_SEND_TRANSFER,
// derived from the proto enum so renumbering the enum surfaces a compile error
// rather than vacuously passing the KnobOffUsesLegacyPath filter.
const opTypeSendTransfer = int32(pbgossip.ConsensusOperationType_CONSENSUS_OPERATION_TYPE_SEND_TRANSFER)

// enableConsensusTransferKnobs sets KnobUseConsensusTransfer (the routing knob)
// to route StartTransferV3 through the 2PC engine. Restoration is handled by
// KnobController's own t.Cleanup (registered in NewKnobController) which
// restores the entire ConfigMap to its pre-test state — no explicit per-knob
// reset needed.
func enableConsensusTransferKnobs(t *testing.T, kc *sparktesting.KnobController) {
	t.Helper()
	require.NoError(t, kc.SetKnob(t, knobs.KnobUseConsensusTransfer, 100))
}

// TestSendTransferV3_Consensus_HappyPath drives a v3 send-transfer through the
// 2PC engine end-to-end with KnobUseConsensusTransfer set, and verifies:
//   - the public StartTransferV3 RPC returns successfully
//   - the returned Transfer is in SENDER_KEY_TWEAKED state
//   - every operator's DB ends up with a Transfer row in SENDER_KEY_TWEAKED
//   - the receiver can complete the claim flow against that transfer
//
// This is the load-bearing assertion that the 2PC path produces the same
// observable end-state as the legacy syncTransferV3Init + syncSettleSenderKeyTweaks
// fanout. FlowExecution-level invariants are covered separately by
// TestSendTransferV3_Consensus_WritesFlowExecutionRows. Transfer-expiry
// rejection is covered by TestCreateTransferV3_ExpiredTransferRejected
// against the legacy path; the consensus path uses the same createTransferV3
// helper inside Prepare, so it is not re-tested here.
func TestSendTransferV3_Consensus_HappyPath(t *testing.T) {
	if !sparktesting.HasLocalSparkIngressHost() {
		t.Skip("skipping cross-operator integration test without minikube ingress (set SPARK_LOCAL_INGRESS_HOST)")
	}
	kc, err := sparktesting.NewKnobController(t)
	if err != nil {
		t.Skipf("knob controller unavailable, cannot route through consensus engine: %v", err)
	}
	enableConsensusTransferKnobs(t, kc)

	senderConfig := wallet.NewTestWalletConfig(t)
	leafPrivKey := keys.GeneratePrivateKey()
	rootNode, err := wallet.CreateNewTree(senderConfig, faucet, leafPrivKey, amountSatsToSend)
	require.NoError(t, err, "failed to create new tree")

	newLeafPrivKey := keys.GeneratePrivateKey()
	receiverPrivKey := keys.GeneratePrivateKey()

	leavesToTransfer := []wallet.LeafKeyTweak{{
		Leaf:              rootNode,
		SigningPrivKey:    leafPrivKey,
		NewSigningPrivKey: newLeafPrivKey,
	}}
	leafReceiverMap := map[string]keys.Public{
		rootNode.GetId(): receiverPrivKey.Public(),
	}

	senderTransfer, err := wallet.SendTransferV3WithKeyTweaks(
		t.Context(), senderConfig, leavesToTransfer, leafReceiverMap,
		time.Now().Add(10*time.Minute),
	)
	require.NoError(t, err, "failed to send V3 transfer via consensus path")
	require.Equal(t, sparkpb.TransferStatus_TRANSFER_STATUS_SENDER_KEY_TWEAKED, senderTransfer.GetStatus(),
		"transfer should be SENDER_KEY_TWEAKED after consensus path StartTransferV3")

	// Each SO must have a Transfer row in SENDER_KEY_TWEAKED state with the
	// same id. Without this, participants diverged from the coordinator.
	transferUUID, err := uuid.Parse(senderTransfer.GetId())
	require.NoError(t, err)
	for _, i := range operatorIndicesFromConfig(senderConfig) {
		entClient := db.NewPostgresEntClientForIntegrationTest(t, operatorDatabasePath(t, i))
		t.Cleanup(func() { _ = entClient.Close() })
		row, err := entClient.Transfer.Query().Where(transferent.IDEQ(transferUUID)).Only(t.Context())
		require.NoError(t, err, "operator %d missing transfer row", i)
		assert.Equal(t, st.TransferStatusSenderKeyTweaked, row.Status,
			"operator %d transfer status mismatch", i)
	}

	// Verify the receiver can complete the claim against the consensus-path
	// transfer just like a legacy-path transfer.
	receiverConfig := wallet.NewTestWalletConfigWithIdentityKey(t, receiverPrivKey)
	receiverToken, err := wallet.AuthenticateWithServer(t.Context(), receiverConfig)
	require.NoError(t, err)
	receiverCtx := wallet.ContextWithToken(t.Context(), receiverToken)

	pending, err := wallet.QueryPendingTransfers(receiverCtx, receiverConfig)
	require.NoError(t, err)
	require.Len(t, pending.GetTransfers(), 1)
	require.Equal(t, senderTransfer.GetId(), pending.GetTransfers()[0].GetId())

	leafPrivKeyMap, err := wallet.VerifyPendingTransfer(t.Context(), receiverConfig, pending.GetTransfers()[0])
	require.NoError(t, err)
	require.Equal(t, map[string]keys.Private{rootNode.GetId(): newLeafPrivKey}, leafPrivKeyMap)

	finalLeafPrivKey := keys.GeneratePrivateKey()
	claimLeaves := []wallet.LeafKeyTweak{{
		Leaf:              pending.GetTransfers()[0].GetLeaves()[0].GetLeaf(),
		SigningPrivKey:    newLeafPrivKey,
		NewSigningPrivKey: finalLeafPrivKey,
	}}
	claimed, err := wallet.ClaimTransferV2(receiverCtx, pending.GetTransfers()[0], receiverConfig, claimLeaves)
	require.NoError(t, err, "receiver claim should succeed against consensus-path transfer")
	assert.Equal(t, sparkpb.TransferStatus_TRANSFER_STATUS_COMPLETED, claimed.GetStatus())
}

// TestSendTransferV3_Consensus_WritesFlowExecutionRows asserts that every
// operator writes a FlowExecution row in COMMITTED state with role aligned to
// coordinator/participant. This is the engine-level check that the 2PC
// bookkeeping ran (parallel to the renew_leaf flow-execution test).
//
// Cannot be t.Parallel()'d: snapshot-delta filtering by op type handles
// concurrent renew_leaf or other 2PC flows, but two concurrent SEND_TRANSFER
// tests would both write rows and break the Len == 1 assertion. Scoping the
// snapshot by transfer ID would be required for parallelism.
func TestSendTransferV3_Consensus_WritesFlowExecutionRows(t *testing.T) {
	if !sparktesting.HasLocalSparkIngressHost() {
		t.Skip("skipping cross-operator integration test without minikube ingress (set SPARK_LOCAL_INGRESS_HOST)")
	}
	kc, err := sparktesting.NewKnobController(t)
	if err != nil {
		t.Skipf("knob controller unavailable: %v", err)
	}
	enableConsensusTransferKnobs(t, kc)

	senderConfig := wallet.NewTestWalletConfig(t)
	coordinatorIdx := int(senderConfig.SigningOperators[senderConfig.CoordinatorIdentifier].ID)
	operatorIndices := operatorIndicesFromConfig(senderConfig)

	// Snapshot pre-existing flow_execution ids per operator so we can
	// identify the rows produced by this test.
	preExistingIDs := make(map[int]map[uuid.UUID]struct{}, len(operatorIndices))
	for _, i := range operatorIndices {
		preExistingIDs[i] = snapshotFlowExecutionIDs(t, operatorDatabasePath(t, i))
	}

	leafPrivKey := keys.GeneratePrivateKey()
	rootNode, err := wallet.CreateNewTree(senderConfig, faucet, leafPrivKey, amountSatsToSend)
	require.NoError(t, err)

	newLeafPrivKey := keys.GeneratePrivateKey()
	receiverPrivKey := keys.GeneratePrivateKey()
	leavesToTransfer := []wallet.LeafKeyTweak{{
		Leaf:              rootNode,
		SigningPrivKey:    leafPrivKey,
		NewSigningPrivKey: newLeafPrivKey,
	}}
	leafReceiverMap := map[string]keys.Public{rootNode.GetId(): receiverPrivKey.Public()}

	senderTransfer, err := wallet.SendTransferV3WithKeyTweaks(
		t.Context(), senderConfig, leavesToTransfer, leafReceiverMap,
		time.Now().Add(10*time.Minute),
	)
	require.NoError(t, err)
	require.Equal(t, sparkpb.TransferStatus_TRANSFER_STATUS_SENDER_KEY_TWEAKED, senderTransfer.GetStatus())

	// Every operator must have written exactly one new SEND_TRANSFER
	// FlowExecution row, all sharing the same id (the engine's executionID).
	// Filter by op type so concurrent renew_leaf or other 2PC ops sharing the
	// minikube cluster don't poison the count assertion with unrelated rows.
	newRowsByOperator := make(map[int][]*ent.FlowExecution, len(operatorIndices))
	for _, i := range operatorIndices {
		allNew := newFlowExecutionsSince(t, operatorDatabasePath(t, i), preExistingIDs[i])
		for _, r := range allNew {
			if r.OpType == opTypeSendTransfer {
				newRowsByOperator[i] = append(newRowsByOperator[i], r)
			}
		}
		require.Len(t, newRowsByOperator[i], 1, "operator %d must write exactly one new SEND_TRANSFER FlowExecution row", i)
	}
	require.NotEmpty(t, newRowsByOperator[coordinatorIdx], "coordinator must have at least one row before sharedID extraction")
	sharedID := newRowsByOperator[coordinatorIdx][0].ID
	for _, i := range operatorIndices {
		row := newRowsByOperator[i][0]
		assert.Equal(t, sharedID, row.ID, "operator %d FlowExecution id must match coordinator's", i)
		assert.Equal(t, st.FlowExecutionStatusCommitted, row.Status,
			"operator %d FlowExecution must be COMMITTED after a successful consensus transfer", i)
		assert.Equal(t, uint(coordinatorIdx), row.CoordinatorIndex,
			"operator %d coordinator_index must point at the coordinator", i)
		if i == coordinatorIdx {
			assert.Equal(t, st.FlowExecutionRoleCoordinator, row.Role,
				"coordinator row must carry the COORDINATOR role")
		} else {
			assert.Equal(t, st.FlowExecutionRoleParticipant, row.Role,
				"operator %d should be PARTICIPANT", i)
		}
	}
}

// TestSendTransferV3_Consensus_KnobOffUsesLegacyPath verifies the knob actually
// gates routing: with the knob at 0 (default), the transfer goes through the
// legacy syncTransferV3Init + syncSettleSenderKeyTweaks fanout, which writes
// no FlowExecution rows. This guards against the routing check silently
// flipping under us.
func TestSendTransferV3_Consensus_KnobOffUsesLegacyPath(t *testing.T) {
	if !sparktesting.HasLocalSparkIngressHost() {
		t.Skip("skipping cross-operator integration test without minikube ingress (set SPARK_LOCAL_INGRESS_HOST)")
	}
	kc, err := sparktesting.NewKnobController(t)
	if err != nil {
		t.Skipf("knob controller unavailable, cannot guarantee knob=0: %v", err)
	}
	// Explicitly set to 0 so we don't pick up a previous test's leak.
	// KnobController's own t.Cleanup restores the original ConfigMap state.
	require.NoError(t, kc.SetKnob(t, knobs.KnobUseConsensusTransfer, 0))

	senderConfig := wallet.NewTestWalletConfig(t)
	operatorIndices := operatorIndicesFromConfig(senderConfig)
	preExistingIDs := make(map[int]map[uuid.UUID]struct{}, len(operatorIndices))
	for _, i := range operatorIndices {
		preExistingIDs[i] = snapshotFlowExecutionIDs(t, operatorDatabasePath(t, i))
	}

	leafPrivKey := keys.GeneratePrivateKey()
	rootNode, err := wallet.CreateNewTree(senderConfig, faucet, leafPrivKey, amountSatsToSend)
	require.NoError(t, err)

	newLeafPrivKey := keys.GeneratePrivateKey()
	receiverPrivKey := keys.GeneratePrivateKey()
	leavesToTransfer := []wallet.LeafKeyTweak{{
		Leaf:              rootNode,
		SigningPrivKey:    leafPrivKey,
		NewSigningPrivKey: newLeafPrivKey,
	}}
	leafReceiverMap := map[string]keys.Public{rootNode.GetId(): receiverPrivKey.Public()}

	senderTransfer, err := wallet.SendTransferV3WithKeyTweaks(
		t.Context(), senderConfig, leavesToTransfer, leafReceiverMap,
		time.Now().Add(10*time.Minute),
	)
	require.NoError(t, err, "legacy v3 path should still succeed with knob off")
	require.Equal(t, sparkpb.TransferStatus_TRANSFER_STATUS_SENDER_KEY_TWEAKED, senderTransfer.GetStatus())

	for _, i := range operatorIndices {
		rows := newFlowExecutionsSince(t, operatorDatabasePath(t, i), preExistingIDs[i])
		// Filter to send-transfer op type only — other flows (renew, etc.)
		// may write rows concurrently.
		var sendTransferRows []*ent.FlowExecution
		for _, r := range rows {
			if r.OpType == opTypeSendTransfer {
				sendTransferRows = append(sendTransferRows, r)
			}
		}
		assert.Empty(t, sendTransferRows,
			"operator %d should NOT have written a SEND_TRANSFER FlowExecution row when the knob is off", i)
	}
}
