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

// enableConsensusClaimKnobs sets KnobUseConsensusClaim (the routing knob) so
// the consensus claim path is exercised end-to-end. Mirrors
// enableConsensusTransferKnobs in send_transfer_consensus_test.go.
func enableConsensusClaimKnobs(t *testing.T, kc *sparktesting.KnobController) {
	t.Helper()
	require.NoError(t, kc.SetKnob(t, knobs.KnobUseConsensusClaim, 100))
}

// TestClaimTransfer_Consensus_HappyPath drives a claim_transfer through the 2PC
// engine end-to-end with KnobUseConsensusClaim set, and verifies:
//   - the public ClaimTransfer RPC returns successfully
//   - the returned Transfer is in COMPLETED state
//   - every operator's DB ends up with a Transfer row in COMPLETED
//
// This is the load-bearing assertion that the 2PC claim path produces the same
// observable end-state as the legacy settleReceiverKeyTweakWithClaimPackage +
// finalize gossip fanout.
func TestClaimTransfer_Consensus_HappyPath(t *testing.T) {
	if !sparktesting.HasLocalSparkIngressHost() {
		t.Skip("skipping cross-operator integration test without minikube ingress (set SPARK_LOCAL_INGRESS_HOST)")
	}
	kc, err := sparktesting.NewKnobController(t)
	if err != nil {
		t.Skipf("knob controller unavailable, cannot route through consensus engine: %v", err)
	}
	// KnobController registers its own t.Cleanup that restores the full
	// ConfigMap snapshot — no per-knob reset needed here.
	enableConsensusClaimKnobs(t, kc)

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
	require.NoError(t, err, "failed to send V3 transfer")
	require.Equal(t, sparkpb.TransferStatus_TRANSFER_STATUS_SENDER_KEY_TWEAKED, senderTransfer.GetStatus())

	receiverConfig := wallet.NewTestWalletConfigWithIdentityKey(t, receiverPrivKey)
	receiverToken, err := wallet.AuthenticateWithServer(t.Context(), receiverConfig)
	require.NoError(t, err)
	receiverCtx := wallet.ContextWithToken(t.Context(), receiverToken)

	pending, err := wallet.QueryPendingTransfers(receiverCtx, receiverConfig)
	require.NoError(t, err)
	require.Len(t, pending.GetTransfers(), 1)

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
	require.NoError(t, err, "claim should succeed through consensus path")
	assert.Equal(t, sparkpb.TransferStatus_TRANSFER_STATUS_COMPLETED, claimed.GetStatus(),
		"transfer should be COMPLETED after consensus path ClaimTransfer")

	// Each SO must have a Transfer row in COMPLETED state with the same id.
	// Without this, participants diverged from the coordinator.
	transferUUID, err := uuid.Parse(senderTransfer.GetId())
	require.NoError(t, err)
	for _, i := range operatorIndicesFromConfig(senderConfig) {
		entClient := db.NewPostgresEntClientForIntegrationTest(t, operatorDatabasePath(t, i))
		t.Cleanup(func() { _ = entClient.Close() })
		row, err := entClient.Transfer.Query().Where(transferent.IDEQ(transferUUID)).Only(t.Context())
		require.NoError(t, err, "operator %d missing transfer row", i)
		assert.Equal(t, st.TransferStatusCompleted, row.Status,
			"operator %d transfer status mismatch after claim", i)
	}
}

// TestClaimTransfer_Consensus_WritesFlowExecutionRows asserts that every
// operator writes a CLAIM_TRANSFER FlowExecution row in COMMITTED state with
// role aligned to coordinator/participant.
func TestClaimTransfer_Consensus_WritesFlowExecutionRows(t *testing.T) {
	if !sparktesting.HasLocalSparkIngressHost() {
		t.Skip("skipping cross-operator integration test without minikube ingress (set SPARK_LOCAL_INGRESS_HOST)")
	}
	kc, err := sparktesting.NewKnobController(t)
	if err != nil {
		t.Skipf("knob controller unavailable: %v", err)
	}
	// KnobController registers its own t.Cleanup that restores the full
	// ConfigMap snapshot — no per-knob reset needed here.
	enableConsensusClaimKnobs(t, kc)

	senderConfig := wallet.NewTestWalletConfig(t)
	coordinatorIdx := int(senderConfig.SigningOperators[senderConfig.CoordinatorIdentifier].ID)
	operatorIndices := operatorIndicesFromConfig(senderConfig)

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

	_, err = wallet.SendTransferV3WithKeyTweaks(
		t.Context(), senderConfig, leavesToTransfer, leafReceiverMap,
		time.Now().Add(10*time.Minute),
	)
	require.NoError(t, err)

	// Snapshot pre-claim flow_execution ids per operator so we can identify
	// the rows produced by the claim. Done AFTER the send so any send-transfer
	// rows (legacy path or, if consensus-send is also on, a SEND_TRANSFER row)
	// are excluded from the "new" set.
	preExistingIDs := make(map[int]map[uuid.UUID]struct{}, len(operatorIndices))
	for _, i := range operatorIndices {
		preExistingIDs[i] = snapshotFlowExecutionIDs(t, operatorDatabasePath(t, i))
	}

	receiverConfig := wallet.NewTestWalletConfigWithIdentityKey(t, receiverPrivKey)
	receiverToken, err := wallet.AuthenticateWithServer(t.Context(), receiverConfig)
	require.NoError(t, err)
	receiverCtx := wallet.ContextWithToken(t.Context(), receiverToken)
	pending, err := wallet.QueryPendingTransfers(receiverCtx, receiverConfig)
	require.NoError(t, err)
	require.Len(t, pending.GetTransfers(), 1)
	_, err = wallet.VerifyPendingTransfer(t.Context(), receiverConfig, pending.GetTransfers()[0])
	require.NoError(t, err)
	finalLeafPrivKey := keys.GeneratePrivateKey()
	claimLeaves := []wallet.LeafKeyTweak{{
		Leaf:              pending.GetTransfers()[0].GetLeaves()[0].GetLeaf(),
		SigningPrivKey:    newLeafPrivKey,
		NewSigningPrivKey: finalLeafPrivKey,
	}}
	claimed, err := wallet.ClaimTransferV2(receiverCtx, pending.GetTransfers()[0], receiverConfig, claimLeaves)
	require.NoError(t, err)
	require.Equal(t, sparkpb.TransferStatus_TRANSFER_STATUS_COMPLETED, claimed.GetStatus())

	// Every operator must have written exactly one new CLAIM_TRANSFER
	// FlowExecution row, all sharing the same id (the engine's executionID).
	newClaimRowsByOperator := make(map[int]*ent.FlowExecution, len(operatorIndices))
	for _, i := range operatorIndices {
		all := newFlowExecutionsSince(t, operatorDatabasePath(t, i), preExistingIDs[i])
		var claimRows []*ent.FlowExecution
		for _, r := range all {
			if r.OpType == opTypeClaimTransfer {
				claimRows = append(claimRows, r)
			}
		}
		require.Lenf(t, claimRows, 1, "operator %d must write exactly one new CLAIM_TRANSFER FlowExecution row", i)
		newClaimRowsByOperator[i] = claimRows[0]
	}
	sharedID := newClaimRowsByOperator[coordinatorIdx].ID
	for _, i := range operatorIndices {
		row := newClaimRowsByOperator[i]
		assert.Equal(t, sharedID, row.ID, "operator %d FlowExecution id must match coordinator's", i)
		assert.Equal(t, st.FlowExecutionStatusCommitted, row.Status,
			"operator %d FlowExecution must be COMMITTED after a successful consensus claim", i)
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

// TestClaimTransfer_Consensus_KnobOffUsesLegacyPath verifies the knob actually
// gates routing: with the knob at 0 (default), the claim goes through the
// legacy settleReceiverKeyTweakWithClaimPackage + finalize gossip fanout, which
// writes no CLAIM_TRANSFER FlowExecution rows. Guards against the routing check
// silently flipping under us.
func TestClaimTransfer_Consensus_KnobOffUsesLegacyPath(t *testing.T) {
	if !sparktesting.HasLocalSparkIngressHost() {
		t.Skip("skipping cross-operator integration test without minikube ingress (set SPARK_LOCAL_INGRESS_HOST)")
	}
	kc, err := sparktesting.NewKnobController(t)
	if err != nil {
		// Without the controller we can't guarantee the knob is 0, so the
		// "uses legacy path" assertion below would be unreliable — a prior
		// test leaving knob=non-zero would silently exercise the consensus
		// path and pass against the wrong code. Skip instead of guessing.
		t.Skipf("knob controller unavailable, cannot pin KnobUseConsensusClaim=0: %v", err)
	}
	require.NoError(t, kc.SetKnob(t, knobs.KnobUseConsensusClaim, 0))

	senderConfig := wallet.NewTestWalletConfig(t)
	operatorIndices := operatorIndicesFromConfig(senderConfig)

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

	_, err = wallet.SendTransferV3WithKeyTweaks(
		t.Context(), senderConfig, leavesToTransfer, leafReceiverMap,
		time.Now().Add(10*time.Minute),
	)
	require.NoError(t, err)

	preExistingIDs := make(map[int]map[uuid.UUID]struct{}, len(operatorIndices))
	for _, i := range operatorIndices {
		preExistingIDs[i] = snapshotFlowExecutionIDs(t, operatorDatabasePath(t, i))
	}

	receiverConfig := wallet.NewTestWalletConfigWithIdentityKey(t, receiverPrivKey)
	receiverToken, err := wallet.AuthenticateWithServer(t.Context(), receiverConfig)
	require.NoError(t, err)
	receiverCtx := wallet.ContextWithToken(t.Context(), receiverToken)
	pending, err := wallet.QueryPendingTransfers(receiverCtx, receiverConfig)
	require.NoError(t, err)
	require.Len(t, pending.GetTransfers(), 1)
	_, err = wallet.VerifyPendingTransfer(t.Context(), receiverConfig, pending.GetTransfers()[0])
	require.NoError(t, err)
	finalLeafPrivKey := keys.GeneratePrivateKey()
	claimLeaves := []wallet.LeafKeyTweak{{
		Leaf:              pending.GetTransfers()[0].GetLeaves()[0].GetLeaf(),
		SigningPrivKey:    newLeafPrivKey,
		NewSigningPrivKey: finalLeafPrivKey,
	}}
	claimed, err := wallet.ClaimTransferV2(receiverCtx, pending.GetTransfers()[0], receiverConfig, claimLeaves)
	require.NoError(t, err, "legacy claim path should still succeed with knob off")
	require.Equal(t, sparkpb.TransferStatus_TRANSFER_STATUS_COMPLETED, claimed.GetStatus())

	for _, i := range operatorIndices {
		rows := newFlowExecutionsSince(t, operatorDatabasePath(t, i), preExistingIDs[i])
		var claimRows []*ent.FlowExecution
		for _, r := range rows {
			if r.OpType == opTypeClaimTransfer {
				claimRows = append(claimRows, r)
			}
		}
		assert.Empty(t, claimRows,
			"operator %d should NOT have written a CLAIM_TRANSFER FlowExecution row when the knob is off", i)
	}
}

// opTypeClaimTransfer mirrors ent.FlowExecution.OpType's int32 storage of
// the consensus op-type enum. Using the proto enum constant means a renumber
// of ConsensusOperationType surfaces as a compile error here rather than a
// silent miscompare against a hardcoded literal.
const opTypeClaimTransfer = int32(pbgossip.ConsensusOperationType_CONSENSUS_OPERATION_TYPE_CLAIM_TRANSFER)
