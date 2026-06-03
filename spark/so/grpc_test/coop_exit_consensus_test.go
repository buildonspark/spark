package grpctest

import (
	"bytes"
	"encoding/hex"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/common/btcnetwork"
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

// opTypeCoopExit is the int32 value of CONSENSUS_OPERATION_TYPE_COOP_EXIT,
// derived from the proto enum so renumbering the enum surfaces a compile error
// rather than vacuously passing the KnobOffUsesLegacyPath filter.
const opTypeCoopExit = int32(pbgossip.ConsensusOperationType_CONSENSUS_OPERATION_TYPE_COOP_EXIT)

// enableConsensusCoopExitKnobs sets both KnobUseConsensusCoopExit (the routing
// knob) and KnobFlowExecutionReconcileEnabled (required by CooperativeExitV2's
// runtime guard). Restoration is handled by KnobController's own t.Cleanup.
func enableConsensusCoopExitKnobs(t *testing.T, kc *sparktesting.KnobController) {
	t.Helper()
	require.NoError(t, kc.SetKnob(t, knobs.KnobFlowExecutionReconcileEnabled, 100))
	require.NoError(t, kc.SetKnob(t, knobs.KnobUseConsensusCoopExit, 100))
}

// TestCoopExit_Consensus_HappyPath drives the single-call cooperative exit
// through the 2PC engine end-to-end with KnobUseConsensusCoopExit set, and
// verifies the same observable end-state as the legacy path (TestCoopExitSingleCall):
//   - the public CooperativeExitV2 RPC returns with the transfer in
//     SENDER_KEY_TWEAK_PENDING (Commit applied refund signatures but did NOT
//     apply key tweaks)
//   - every operator's DB converges on SENDER_KEY_TWEAK_PENDING (the distributed
//     convergence property is only observable per-operator)
//   - the key tweaks stay deferred until the exit tx confirms: only then does
//     the chain watcher promote the transfer to SENDER_KEY_TWEAKED and surface
//     it to the receiver, who can then claim. The "deferred" property is proven
//     through public behavior — the receiver sees no claimable transfer until
//     after confirmation.
func TestCoopExit_Consensus_HappyPath(t *testing.T) {
	if !sparktesting.HasLocalSparkIngressHost() {
		t.Skip("skipping cross-operator integration test without minikube ingress (set SPARK_LOCAL_INGRESS_HOST)")
	}
	kc, err := sparktesting.NewKnobController(t)
	if err != nil {
		t.Skipf("knob controller unavailable, cannot route through consensus engine: %v", err)
	}
	enableConsensusCoopExitKnobs(t, kc)

	client := sparktesting.GetBitcoinClient()
	coin, err := faucet.Fund()
	require.NoError(t, err)

	amountSats := int64(100_000)
	config, sspConfig, transferNode := setupUsers(t, amountSats)

	withdrawPrivKey := keys.GeneratePrivateKey()
	exitTx, connectorTx, connectorOutputs := createTestCoopExitAndConnectorOutputs(
		t, sspConfig, 1, coin.OutPoint, withdrawPrivKey.Public(), amountSats,
	)

	var connectorTxBuf bytes.Buffer
	require.NoError(t, connectorTx.Serialize(&connectorTxBuf))

	exitTxID, err := hex.DecodeString(exitTx.TxID())
	require.NoError(t, err)
	senderTransfer, err := wallet.GetConnectorRefundSignaturesV2WithTransferPackage(
		t.Context(),
		config,
		[]wallet.LeafKeyTweak{transferNode},
		exitTxID,
		connectorOutputs,
		sspConfig.IdentityPublicKey(),
		time.Now().Add(24*time.Hour),
		connectorTxBuf.Bytes(),
	)
	require.NoError(t, err, "failed to start coop exit via consensus path")
	require.Equal(t, sparkpb.TransferStatus_TRANSFER_STATUS_SENDER_KEY_TWEAK_PENDING, senderTransfer.Status,
		"coop exit should be SENDER_KEY_TWEAK_PENDING after consensus path (key tweaks deferred to chain watcher)")

	// Every SO must converge on SENDER_KEY_TWEAK_PENDING — the participant-side
	// effect of the 2PC commit is only observable per-operator (mirrors the
	// send-transfer consensus test).
	transferUUID, err := uuid.Parse(senderTransfer.Id)
	require.NoError(t, err)
	for _, i := range operatorIndicesFromConfig(config) {
		entClient := db.NewPostgresEntClientForIntegrationTest(t, operatorDatabasePath(t, i))
		t.Cleanup(func() { _ = entClient.Close() })
		row, err := entClient.Transfer.Query().Where(transferent.IDEQ(transferUUID)).Only(t.Context())
		require.NoError(t, err, "operator %d missing transfer row", i)
		assert.Equal(t, st.TransferStatusSenderKeyTweakPending, row.Status,
			"operator %d transfer status mismatch", i)
	}

	// SSP broadcasts the exit tx and confirms it past the threshold.
	signedExitTx, err := sparktesting.SignFaucetCoin(exitTx, coin.TxOut, coin.Key)
	require.NoError(t, err)
	_, err = client.SendRawTransaction(signedExitTx, true)
	require.NoError(t, err)

	randomKey := keys.GeneratePrivateKey()
	randomAddress, err := common.P2TRRawAddressFromPublicKey(randomKey.Public(), btcnetwork.Regtest)
	require.NoError(t, err)
	_, err = client.GenerateToAddress(3, randomAddress, nil)
	require.NoError(t, err)
	_, err = client.GenerateToAddress(knobs.CoopExitConfirmationThreshold+2, randomAddress, nil)
	require.NoError(t, err)

	sspToken, err := wallet.AuthenticateWithServer(t.Context(), sspConfig)
	require.NoError(t, err)
	sspCtx := wallet.ContextWithToken(t.Context(), sspToken)

	// The chain watcher applies the deferred key tweaks once the exit tx
	// confirms, promoting the transfer to SENDER_KEY_TWEAKED and surfacing it as
	// a pending transfer to the receiver.
	receiverTransfer := waitForPendingTransferToConfirm(sspCtx, t, sspConfig)
	assert.Equal(t, senderTransfer.Id, receiverTransfer.Id)
	assert.Equal(t, sparkpb.TransferStatus_TRANSFER_STATUS_SENDER_KEY_TWEAKED, receiverTransfer.Status)
	assert.Equal(t, sparkpb.TransferType_COOPERATIVE_EXIT, receiverTransfer.Type)

	leafPrivKeyMap, err := wallet.VerifyPendingTransfer(t.Context(), sspConfig, receiverTransfer)
	require.NoError(t, err)
	assert.Len(t, leafPrivKeyMap, 1)
	assert.Equal(t, leafPrivKeyMap[transferNode.Leaf.Id], sspConfig.IdentityPrivateKey)

	finalLeafPrivKey := keys.GeneratePrivateKey()
	claimingNode := wallet.LeafKeyTweak{
		Leaf:              senderTransfer.Leaves[0].Leaf,
		SigningPrivKey:    sspConfig.IdentityPrivateKey,
		NewSigningPrivKey: finalLeafPrivKey,
	}
	leavesToClaim := []wallet.LeafKeyTweak{claimingNode}
	startTime := time.Now()
	for {
		currentTransfer := receiverTransfer
		transfers, _, err := wallet.QueryAllTransfersWithTypes(
			sspCtx, sspConfig, 100, 0, []sparkpb.TransferType{sparkpb.TransferType_COOPERATIVE_EXIT},
		)
		require.NoError(t, err)
		for _, tr := range transfers {
			if tr.Id == receiverTransfer.Id {
				currentTransfer = tr
				break
			}
		}
		_, err = wallet.ClaimTransfer(sspCtx, currentTransfer, sspConfig, leavesToClaim)
		if err == nil {
			break
		}
		time.Sleep(200 * time.Millisecond)
		if time.Since(startTime) > 15*time.Second {
			t.Fatalf("timed out waiting for tx to confirm")
		}
	}
}

// TestCoopExit_Consensus_WritesFlowExecutionRows asserts that every operator
// writes a COOP_EXIT FlowExecution row in COMMITTED state with role aligned to
// coordinator/participant. This is the engine-level check that the 2PC
// bookkeeping ran (parallel to the send-transfer flow-execution test).
//
// Cannot be t.Parallel()'d: two concurrent COOP_EXIT tests would both write
// rows and break the Len == 1 assertion.
func TestCoopExit_Consensus_WritesFlowExecutionRows(t *testing.T) {
	if !sparktesting.HasLocalSparkIngressHost() {
		t.Skip("skipping cross-operator integration test without minikube ingress (set SPARK_LOCAL_INGRESS_HOST)")
	}
	kc, err := sparktesting.NewKnobController(t)
	if err != nil {
		t.Skipf("knob controller unavailable: %v", err)
	}
	enableConsensusCoopExitKnobs(t, kc)

	coin, err := faucet.Fund()
	require.NoError(t, err)
	amountSats := int64(100_000)
	config, sspConfig, transferNode := setupUsers(t, amountSats)

	coordinatorIdx := int(config.SigningOperators[config.CoordinatorIdentifier].ID)
	operatorIndices := operatorIndicesFromConfig(config)
	preExistingIDs := make(map[int]map[uuid.UUID]struct{}, len(operatorIndices))
	for _, i := range operatorIndices {
		preExistingIDs[i] = snapshotFlowExecutionIDs(t, operatorDatabasePath(t, i))
	}

	withdrawPrivKey := keys.GeneratePrivateKey()
	exitTx, connectorTx, connectorOutputs := createTestCoopExitAndConnectorOutputs(
		t, sspConfig, 1, coin.OutPoint, withdrawPrivKey.Public(), amountSats,
	)
	var connectorTxBuf bytes.Buffer
	require.NoError(t, connectorTx.Serialize(&connectorTxBuf))

	exitTxID, err := hex.DecodeString(exitTx.TxID())
	require.NoError(t, err)
	senderTransfer, err := wallet.GetConnectorRefundSignaturesV2WithTransferPackage(
		t.Context(),
		config,
		[]wallet.LeafKeyTweak{transferNode},
		exitTxID,
		connectorOutputs,
		sspConfig.IdentityPublicKey(),
		time.Now().Add(24*time.Hour),
		connectorTxBuf.Bytes(),
	)
	require.NoError(t, err)
	require.Equal(t, sparkpb.TransferStatus_TRANSFER_STATUS_SENDER_KEY_TWEAK_PENDING, senderTransfer.Status)

	newRowsByOperator := make(map[int][]*ent.FlowExecution, len(operatorIndices))
	for _, i := range operatorIndices {
		allNew := newFlowExecutionsSince(t, operatorDatabasePath(t, i), preExistingIDs[i])
		for _, r := range allNew {
			if r.OpType == opTypeCoopExit {
				newRowsByOperator[i] = append(newRowsByOperator[i], r)
			}
		}
		require.Len(t, newRowsByOperator[i], 1, "operator %d must write exactly one new COOP_EXIT FlowExecution row", i)
	}
	require.NotEmpty(t, newRowsByOperator[coordinatorIdx])
	sharedID := newRowsByOperator[coordinatorIdx][0].ID
	for _, i := range operatorIndices {
		row := newRowsByOperator[i][0]
		assert.Equal(t, sharedID, row.ID, "operator %d FlowExecution id must match coordinator's", i)
		assert.Equal(t, st.FlowExecutionStatusCommitted, row.Status,
			"operator %d FlowExecution must be COMMITTED after a successful consensus coop exit", i)
		assert.Equal(t, uint(coordinatorIdx), row.CoordinatorIndex,
			"operator %d coordinator_index must point at the coordinator", i)
		if i == coordinatorIdx {
			assert.Equal(t, st.FlowExecutionRoleCoordinator, row.Role)
		} else {
			assert.Equal(t, st.FlowExecutionRoleParticipant, row.Role, "operator %d should be PARTICIPANT", i)
		}
	}
}

// TestCoopExit_Consensus_KnobOffUsesLegacyPath verifies the knob actually gates
// routing: with the knob at 0 (default), the single-call coop exit goes through
// the legacy syncCoopExitInit fanout, which writes no FlowExecution rows. This
// guards against the routing check silently flipping under us.
func TestCoopExit_Consensus_KnobOffUsesLegacyPath(t *testing.T) {
	if !sparktesting.HasLocalSparkIngressHost() {
		t.Skip("skipping cross-operator integration test without minikube ingress (set SPARK_LOCAL_INGRESS_HOST)")
	}
	kc, err := sparktesting.NewKnobController(t)
	if err != nil {
		t.Skipf("knob controller unavailable, cannot guarantee knob=0: %v", err)
	}
	require.NoError(t, kc.SetKnob(t, knobs.KnobUseConsensusCoopExit, 0))

	coin, err := faucet.Fund()
	require.NoError(t, err)
	amountSats := int64(100_000)
	config, sspConfig, transferNode := setupUsers(t, amountSats)

	operatorIndices := operatorIndicesFromConfig(config)
	preExistingIDs := make(map[int]map[uuid.UUID]struct{}, len(operatorIndices))
	for _, i := range operatorIndices {
		preExistingIDs[i] = snapshotFlowExecutionIDs(t, operatorDatabasePath(t, i))
	}

	withdrawPrivKey := keys.GeneratePrivateKey()
	exitTx, connectorTx, connectorOutputs := createTestCoopExitAndConnectorOutputs(
		t, sspConfig, 1, coin.OutPoint, withdrawPrivKey.Public(), amountSats,
	)
	var connectorTxBuf bytes.Buffer
	require.NoError(t, connectorTx.Serialize(&connectorTxBuf))

	exitTxID, err := hex.DecodeString(exitTx.TxID())
	require.NoError(t, err)
	senderTransfer, err := wallet.GetConnectorRefundSignaturesV2WithTransferPackage(
		t.Context(),
		config,
		[]wallet.LeafKeyTweak{transferNode},
		exitTxID,
		connectorOutputs,
		sspConfig.IdentityPublicKey(),
		time.Now().Add(24*time.Hour),
		connectorTxBuf.Bytes(),
	)
	require.NoError(t, err, "legacy single-call coop exit should still succeed with knob off")
	require.Equal(t, sparkpb.TransferStatus_TRANSFER_STATUS_SENDER_KEY_TWEAK_PENDING, senderTransfer.Status)

	for _, i := range operatorIndices {
		rows := newFlowExecutionsSince(t, operatorDatabasePath(t, i), preExistingIDs[i])
		var coopExitRows []*ent.FlowExecution
		for _, r := range rows {
			if r.OpType == opTypeCoopExit {
				coopExitRows = append(coopExitRows, r)
			}
		}
		assert.Empty(t, coopExitRows,
			"operator %d should NOT have written a COOP_EXIT FlowExecution row when the knob is off", i)
	}
}
