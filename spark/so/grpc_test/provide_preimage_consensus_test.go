package grpctest

import (
	"testing"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/keys"
	pbgossip "github.com/lightsparkdev/spark/proto/gossip"
	sparkpb "github.com/lightsparkdev/spark/proto/spark"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	transferent "github.com/lightsparkdev/spark/so/ent/transfer"
	sparktesting "github.com/lightsparkdev/spark/testing"
	"github.com/lightsparkdev/spark/testing/wallet"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestProvidePreimage_Consensus_HappyPath drives a HODL preimage swap settled
// via ProvidePreimage through the 2PC engine end-to-end and verifies:
//   - the public ProvidePreimage RPC returns successfully
//   - the returned Transfer is SENDER_KEY_TWEAKED
//   - every operator's DB ends up with a Transfer row in SENDER_KEY_TWEAKED
//
// This is the load-bearing assertion that the 2PC provide_preimage path
// produces the expected observable end-state.
func TestProvidePreimage_Consensus_HappyPath(t *testing.T) {
	if !sparktesting.HasLocalSparkIngressHost() {
		t.Skip("skipping cross-operator integration test without minikube ingress (set SPARK_LOCAL_INGRESS_HOST)")
	}

	userConfig := wallet.NewTestWalletConfig(t)
	sspConfig := wallet.NewTestWalletConfig(t)

	amountSats := uint64(100)
	preimage, paymentHash := testPreimageHash(t, amountSats)
	defer cleanUp(t, userConfig, paymentHash)

	userLeafPrivKey := keys.GeneratePrivateKey()
	feeSats := uint64(2)
	nodeToSend, err := wallet.CreateNewTree(userConfig, faucet, userLeafPrivKey, 12347)
	require.NoError(t, err)

	newLeafPrivKey := keys.GeneratePrivateKey()
	leaves := []wallet.LeafKeyTweak{{
		Leaf:              nodeToSend,
		SigningPrivKey:    userLeafPrivKey,
		NewSigningPrivKey: newLeafPrivKey,
	}}

	response, err := wallet.SwapNodesForPreimage(
		t.Context(),
		userConfig,
		leaves,
		sspConfig.IdentityPublicKey(),
		paymentHash[:],
		new(testInvoice),
		feeSats,
		false,
		amountSats,
	)
	require.NoError(t, err)

	transfer, err := wallet.DeliverTransferPackage(t.Context(), userConfig, response.GetTransfer(), leaves, nil)
	require.NoError(t, err)
	require.Equal(t, sparkpb.TransferStatus_TRANSFER_STATUS_SENDER_KEY_TWEAK_PENDING, transfer.GetStatus())

	receiverTransfer, err := wallet.ProvidePreimage(t.Context(), sspConfig, preimage[:])
	require.NoError(t, err, "provide preimage should succeed through consensus path")
	assert.Equal(t, sparkpb.TransferStatus_TRANSFER_STATUS_SENDER_KEY_TWEAKED, receiverTransfer.GetStatus(),
		"transfer should be SENDER_KEY_TWEAKED after consensus path ProvidePreimage")
	assert.Equal(t, transfer.GetId(), receiverTransfer.GetId())

	// Every SO must have a Transfer row in SENDER_KEY_TWEAKED with the same
	// id. Without this, participants diverged from the coordinator.
	transferUUID, err := uuid.Parse(transfer.GetId())
	require.NoError(t, err)
	for _, i := range operatorIndicesFromConfig(userConfig) {
		entClient := db.NewPostgresEntClientForIntegrationTest(t, operatorDatabasePath(t, i))
		t.Cleanup(func() { _ = entClient.Close() })
		row, err := entClient.Transfer.Query().Where(transferent.IDEQ(transferUUID)).Only(t.Context())
		require.NoError(t, err, "operator %d missing transfer row", i)
		assert.Equal(t, st.TransferStatusSenderKeyTweaked, row.Status,
			"operator %d transfer status mismatch after provide preimage", i)
	}
}

// TestProvidePreimage_Consensus_WritesFlowExecutionRows asserts that every
// operator writes a PROVIDE_PREIMAGE FlowExecution row in COMMITTED state
// with role aligned to coordinator/participant.
func TestProvidePreimage_Consensus_WritesFlowExecutionRows(t *testing.T) {
	if !sparktesting.HasLocalSparkIngressHost() {
		t.Skip("skipping cross-operator integration test without minikube ingress (set SPARK_LOCAL_INGRESS_HOST)")
	}

	userConfig := wallet.NewTestWalletConfig(t)
	sspConfig := wallet.NewTestWalletConfig(t)
	// The coordinator for the ProvidePreimage RPC is the SSP's coordinator —
	// the SSP is the principal that calls ProvidePreimage in the HODL send
	// flow. The user's coordinator may differ.
	coordinatorIdx := int(sspConfig.SigningOperators[sspConfig.CoordinatorIdentifier].ID)
	operatorIndices := operatorIndicesFromConfig(sspConfig)

	amountSats := uint64(100)
	preimage, paymentHash := testPreimageHash(t, amountSats)
	defer cleanUp(t, userConfig, paymentHash)

	userLeafPrivKey := keys.GeneratePrivateKey()
	feeSats := uint64(2)
	nodeToSend, err := wallet.CreateNewTree(userConfig, faucet, userLeafPrivKey, 12347)
	require.NoError(t, err)
	newLeafPrivKey := keys.GeneratePrivateKey()
	leaves := []wallet.LeafKeyTweak{{
		Leaf:              nodeToSend,
		SigningPrivKey:    userLeafPrivKey,
		NewSigningPrivKey: newLeafPrivKey,
	}}
	response, err := wallet.SwapNodesForPreimage(
		t.Context(),
		userConfig,
		leaves,
		sspConfig.IdentityPublicKey(),
		paymentHash[:],
		new(testInvoice),
		feeSats,
		false,
		amountSats,
	)
	require.NoError(t, err)
	_, err = wallet.DeliverTransferPackage(t.Context(), userConfig, response.GetTransfer(), leaves, nil)
	require.NoError(t, err)

	// Snapshot pre-provide flow_execution ids per operator so the assertion
	// pass can isolate rows produced by the provide_preimage. Done AFTER the
	// swap so any swap-side rows (legacy or, if other consensus knobs are
	// also on, send-transfer / preimage-share rows) are excluded.
	preExistingIDs := make(map[int]map[uuid.UUID]struct{}, len(operatorIndices))
	for _, i := range operatorIndices {
		preExistingIDs[i] = snapshotFlowExecutionIDs(t, operatorDatabasePath(t, i))
	}

	_, err = wallet.ProvidePreimage(t.Context(), sspConfig, preimage[:])
	require.NoError(t, err)

	// Every operator must have written exactly one new PROVIDE_PREIMAGE
	// FlowExecution row, all sharing the same id (the engine's executionID).
	newProvideRowsByOperator := make(map[int]*ent.FlowExecution, len(operatorIndices))
	for _, i := range operatorIndices {
		all := newFlowExecutionsSince(t, operatorDatabasePath(t, i), preExistingIDs[i])
		var provideRows []*ent.FlowExecution
		for _, r := range all {
			if r.OpType == opTypeProvidePreimage {
				provideRows = append(provideRows, r)
			}
		}
		require.Lenf(t, provideRows, 1, "operator %d must write exactly one new PROVIDE_PREIMAGE FlowExecution row", i)
		newProvideRowsByOperator[i] = provideRows[0]
	}
	sharedID := newProvideRowsByOperator[coordinatorIdx].ID
	for _, i := range operatorIndices {
		row := newProvideRowsByOperator[i]
		assert.Equal(t, sharedID, row.ID, "operator %d FlowExecution id must match coordinator's", i)
		assert.Equal(t, st.FlowExecutionStatusCommitted, row.Status,
			"operator %d FlowExecution must be COMMITTED after a successful consensus provide_preimage", i)
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

// opTypeProvidePreimage mirrors ent.FlowExecution.OpType's int32 storage of
// the consensus op-type enum. Using the proto enum constant means a renumber
// of ConsensusOperationType surfaces as a compile error here rather than a
// silent miscompare against a hardcoded literal.
const opTypeProvidePreimage = int32(pbgossip.ConsensusOperationType_CONSENSUS_OPERATION_TYPE_PROVIDE_PREIMAGE)
