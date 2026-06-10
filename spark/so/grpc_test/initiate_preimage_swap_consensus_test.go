package grpctest

import (
	"testing"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/keys"
	pbgossip "github.com/lightsparkdev/spark/proto/gossip"
	spark "github.com/lightsparkdev/spark/proto/spark"
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

// opTypeInitiatePreimageSwap is the int32 value of
// CONSENSUS_OPERATION_TYPE_INITIATE_PREIMAGE_SWAP, derived from the proto enum so
// renumbering it surfaces a compile error rather than vacuously passing the
// op-type filter below.
const opTypeInitiatePreimageSwap = int32(pbgossip.ConsensusOperationType_CONSENSUS_OPERATION_TYPE_INITIATE_PREIMAGE_SWAP)

// enableConsensusInitiatePreimageSwapKnob routes InitiatePreimageSwapV3 through
// the 2PC engine. Restoration is handled by KnobController's own t.Cleanup.
func enableConsensusInitiatePreimageSwapKnob(t *testing.T, kc *sparktesting.KnobController) {
	t.Helper()
	require.NoError(t, kc.SetKnob(t, knobs.KnobUseConsensusInitiatePreimageSwap, 100))
}

// preimageSwapNodeValueSats is the leaf value the SEND tests create and assert
// against. Shared so the CreateNewTree call and the refund-sum assertion can't
// drift.
const preimageSwapNodeValueSats = int64(12347)

// TestInitiatePreimageSwapV3_Consensus_SendHappyPath drives a lightning-send
// preimage swap (REASON_SEND with a transfer package — the path that produces
// FROST refund-signature shares in Prepare and aggregates them in
// BuildCommitPayload) through the 2PC engine end-to-end with
// KnobUseConsensusInitiatePreimageSwap set, and verifies:
//   - InitiatePreimageSwapV3 returns a transfer in SENDER_KEY_TWEAK_PENDING
//     (key tweaks are deferred to ProvidePreimage, matching legacy)
//   - every operator's DB has the transfer row in SENDER_KEY_TWEAK_PENDING
//   - the SSP's user-signed refund query returns the SO-aggregated refund
//     signatures (proves Prepare→aggregate→Commit applied them on every SO)
//   - the downstream ProvidePreimage settles the transfer to SENDER_KEY_TWEAKED
//     and the receiver can claim it
//
// This is the load-bearing assertion that the 2PC path produces the same
// observable end-state as the legacy initiatePreimageSwap fanout. FlowExecution
// row invariants are covered by TestInitiatePreimageSwapV3_Consensus_WritesFlowExecutionRows.
func TestInitiatePreimageSwapV3_Consensus_SendHappyPath(t *testing.T) {
	if !sparktesting.HasLocalSparkIngressHost() {
		t.Skip("skipping cross-operator integration test without minikube ingress (set SPARK_LOCAL_INGRESS_HOST)")
	}
	kc, err := sparktesting.NewKnobController(t)
	if err != nil {
		t.Skipf("knob controller unavailable, cannot route through consensus engine: %v", err)
	}
	enableConsensusInitiatePreimageSwapKnob(t, kc)

	userConfig := wallet.NewTestWalletConfig(t)
	sspConfig := wallet.NewTestWalletConfig(t)

	amountSats := uint64(100)
	preimage, paymentHash := testPreimageHash(t, amountSats)
	defer cleanUp(t, userConfig, paymentHash)

	userLeafPrivKey := keys.GeneratePrivateKey()
	feeSats := uint64(2)
	nodeToSend, err := wallet.CreateNewTree(userConfig, faucet, userLeafPrivKey, preimageSwapNodeValueSats)
	require.NoError(t, err)
	newLeafPrivKey := keys.GeneratePrivateKey()
	leaves := []wallet.LeafKeyTweak{{
		Leaf:              nodeToSend,
		SigningPrivKey:    userLeafPrivKey,
		NewSigningPrivKey: newLeafPrivKey,
	}}

	response, err := wallet.SwapNodesForPreimageWithHTLC(
		t.Context(),
		userConfig,
		leaves,
		sspConfig.IdentityPublicKey(),
		paymentHash[:],
		new(testInvoice),
		feeSats,
		false, // isInboundPayment: this is a send
		amountSats,
		true, // useV3: route through the consensus engine
	)
	require.NoError(t, err, "InitiatePreimageSwapV3 should succeed through the consensus path")

	transfer := response.GetTransfer()
	require.Equal(t, spark.TransferStatus_TRANSFER_STATUS_SENDER_KEY_TWEAK_PENDING, transfer.GetStatus())

	// Every SO must have the transfer row in SENDER_KEY_TWEAK_PENDING — without
	// this, participants diverged from the coordinator during Prepare/Commit.
	transferUUID, err := uuid.Parse(transfer.GetId())
	require.NoError(t, err)
	for _, i := range operatorIndicesFromConfig(userConfig) {
		entClient := db.NewPostgresEntClientForIntegrationTest(t, operatorDatabasePath(t, i))
		t.Cleanup(func() { _ = entClient.Close() })
		row, err := entClient.Transfer.Query().Where(transferent.IDEQ(transferUUID)).Only(t.Context())
		require.NoError(t, err, "operator %d missing transfer row", i)
		assert.Equal(t, st.TransferStatusSenderKeyTweakPending, row.Status,
			"operator %d transfer status mismatch after consensus initiate preimage swap", i)
	}

	// The SO-aggregated refund signatures must be applied (Commit ran on every
	// SO). QueryUserSignedRefunds validates the signed refund txs; their output
	// values sum to the full node value (value is conserved — the fee is taken
	// out of the receiver's share, not added on top of the node value).
	refunds, err := wallet.QueryUserSignedRefunds(t.Context(), sspConfig, paymentHash[:])
	require.NoError(t, err)
	var totalValue int64
	for _, refund := range refunds {
		value, err := wallet.ValidateUserSignedRefund(refund)
		require.NoError(t, err)
		totalValue += value
	}
	assert.Equal(t, preimageSwapNodeValueSats, totalValue)

	// Downstream settlement: ProvidePreimage advances the transfer to
	// SENDER_KEY_TWEAKED, and the receiver claims it — proving the consensus
	// initiate path leaves the transfer in a state the rest of the flow accepts.
	receiverTransfer, err := wallet.ProvidePreimage(t.Context(), sspConfig, preimage[:])
	require.NoError(t, err)
	assert.Equal(t, spark.TransferStatus_TRANSFER_STATUS_SENDER_KEY_TWEAKED, receiverTransfer.GetStatus())
	require.Equal(t, transfer.GetId(), receiverTransfer.GetId())

	receiverToken, err := wallet.AuthenticateWithServer(t.Context(), sspConfig)
	require.NoError(t, err)
	receiverCtx := wallet.ContextWithToken(t.Context(), receiverToken)
	_, err = wallet.VerifyPendingTransfer(receiverCtx, sspConfig, receiverTransfer)
	require.NoError(t, err)
	finalLeafPrivKey := keys.GeneratePrivateKey()
	leavesToClaim := []wallet.LeafKeyTweak{{
		Leaf:              receiverTransfer.GetLeaves()[0].GetLeaf(),
		SigningPrivKey:    newLeafPrivKey,
		NewSigningPrivKey: finalLeafPrivKey,
	}}
	_, err = wallet.ClaimTransfer(receiverCtx, receiverTransfer, sspConfig, leavesToClaim)
	require.NoError(t, err, "receiver should be able to claim the consensus-initiated transfer")
}

// TestInitiatePreimageSwapV3_Consensus_WritesFlowExecutionRows asserts that every
// operator writes an INITIATE_PREIMAGE_SWAP FlowExecution row in COMMITTED state,
// sharing the coordinator's execution id, with role aligned to coordinator/participant.
func TestInitiatePreimageSwapV3_Consensus_WritesFlowExecutionRows(t *testing.T) {
	if !sparktesting.HasLocalSparkIngressHost() {
		t.Skip("skipping cross-operator integration test without minikube ingress (set SPARK_LOCAL_INGRESS_HOST)")
	}
	kc, err := sparktesting.NewKnobController(t)
	if err != nil {
		t.Skipf("knob controller unavailable, cannot route through consensus engine: %v", err)
	}
	enableConsensusInitiatePreimageSwapKnob(t, kc)

	userConfig := wallet.NewTestWalletConfig(t)
	sspConfig := wallet.NewTestWalletConfig(t)
	// The user is the principal that calls InitiatePreimageSwapV3, so the
	// coordinator is the user's coordinator.
	coordinatorIdx := int(userConfig.SigningOperators[userConfig.CoordinatorIdentifier].ID)
	operatorIndices := operatorIndicesFromConfig(userConfig)

	amountSats := uint64(100)
	_, paymentHash := testPreimageHash(t, amountSats)
	defer cleanUp(t, userConfig, paymentHash)

	userLeafPrivKey := keys.GeneratePrivateKey()
	feeSats := uint64(2)
	nodeToSend, err := wallet.CreateNewTree(userConfig, faucet, userLeafPrivKey, preimageSwapNodeValueSats)
	require.NoError(t, err)
	newLeafPrivKey := keys.GeneratePrivateKey()
	leaves := []wallet.LeafKeyTweak{{
		Leaf:              nodeToSend,
		SigningPrivKey:    userLeafPrivKey,
		NewSigningPrivKey: newLeafPrivKey,
	}}

	// Snapshot pre-swap flow_execution ids so the assertion isolates rows
	// produced by this swap.
	preExistingIDs := make(map[int]map[uuid.UUID]struct{}, len(operatorIndices))
	for _, i := range operatorIndices {
		preExistingIDs[i] = snapshotFlowExecutionIDs(t, operatorDatabasePath(t, i))
	}

	_, err = wallet.SwapNodesForPreimageWithHTLC(
		t.Context(),
		userConfig,
		leaves,
		sspConfig.IdentityPublicKey(),
		paymentHash[:],
		new(testInvoice),
		feeSats,
		false,
		amountSats,
		true, // useV3
	)
	require.NoError(t, err)

	newRowsByOperator := make(map[int]*ent.FlowExecution, len(operatorIndices))
	for _, i := range operatorIndices {
		all := newFlowExecutionsSince(t, operatorDatabasePath(t, i), preExistingIDs[i])
		var rows []*ent.FlowExecution
		for _, r := range all {
			if r.OpType == opTypeInitiatePreimageSwap {
				rows = append(rows, r)
			}
		}
		require.Lenf(t, rows, 1, "operator %d must write exactly one new INITIATE_PREIMAGE_SWAP FlowExecution row", i)
		newRowsByOperator[i] = rows[0]
	}
	sharedID := newRowsByOperator[coordinatorIdx].ID
	for _, i := range operatorIndices {
		row := newRowsByOperator[i]
		assert.Equal(t, sharedID, row.ID, "operator %d FlowExecution id must match coordinator's", i)
		assert.Equal(t, st.FlowExecutionStatusCommitted, row.Status,
			"operator %d FlowExecution must be COMMITTED after a successful consensus initiate preimage swap", i)
		assert.Equal(t, uint(coordinatorIdx), row.CoordinatorIndex,
			"operator %d coordinator_index must point at the coordinator", i)
		if i == coordinatorIdx {
			assert.Equal(t, st.FlowExecutionRoleCoordinator, row.Role, "coordinator row must carry the COORDINATOR role")
		} else {
			assert.Equal(t, st.FlowExecutionRoleParticipant, row.Role, "operator %d should be PARTICIPANT", i)
		}
	}
}

// TestInitiatePreimageSwapV3_Consensus_ReceiveHappyPath drives a non-HODL
// lightning-receive preimage swap through the 2PC engine — the path with the
// most novel coordinator logic: every SO returns its preimage share in Prepare
// and the coordinator recovers the secret from a threshold of them in
// BuildCommitPayload (recoverPreimage) before verifying it against the payment
// hash. Verifies:
//   - the swap returns the recovered preimage (proves cross-SO threshold recovery
//     ran through the consensus engine, not the legacy fanout)
//   - every operator's DB has the transfer row (Prepare ran everywhere)
//   - the receiver can complete delivery + claim
//
// A preimage share is pre-stored via CreateLightningInvoiceWithPreimage (which is
// what makes this the non-HODL path rather than HODL).
func TestInitiatePreimageSwapV3_Consensus_ReceiveHappyPath(t *testing.T) {
	if !sparktesting.HasLocalSparkIngressHost() {
		t.Skip("skipping cross-operator integration test without minikube ingress (set SPARK_LOCAL_INGRESS_HOST)")
	}
	kc, err := sparktesting.NewKnobController(t)
	if err != nil {
		t.Skipf("knob controller unavailable, cannot route through consensus engine: %v", err)
	}
	enableConsensusInitiatePreimageSwapKnob(t, kc)

	userConfig := wallet.NewTestWalletConfig(t)
	sspConfig := wallet.NewTestWalletConfig(t)

	amountSats := uint64(100)
	preimage, paymentHash := testPreimageHash(t, amountSats)
	fakeInvoiceCreator := NewFakeLightningInvoiceCreator()
	defer cleanUp(t, userConfig, paymentHash)

	// The user creates the invoice, distributing preimage shares across the SOs —
	// this is what makes the swap non-HODL (the SOs can recover the preimage).
	invoice, err := wallet.CreateLightningInvoiceWithPreimage(t.Context(), userConfig, fakeInvoiceCreator, amountSats, "test", preimage)
	require.NoError(t, err)
	require.NotNil(t, invoice)

	// The SSP funds a leaf to send to the user.
	sspLeafPrivKey := keys.GeneratePrivateKey()
	nodeToSend, err := wallet.CreateNewTree(sspConfig, faucet, sspLeafPrivKey, 12345)
	require.NoError(t, err)
	newLeafPrivKey := keys.GeneratePrivateKey()
	leaves := []wallet.LeafKeyTweak{{
		Leaf:              nodeToSend,
		SigningPrivKey:    sspLeafPrivKey,
		NewSigningPrivKey: newLeafPrivKey,
	}}

	response, err := wallet.SwapNodesForPreimage(
		t.Context(),
		sspConfig,
		leaves,
		userConfig.IdentityPublicKey(),
		paymentHash[:],
		nil,
		uint64(0), // feeSats: not allowed on receive
		true,      // isInboundPayment: lightning receive
		amountSats,
		true, // useV3: route through the consensus engine
	)
	require.NoError(t, err, "consensus receive swap should succeed")
	// The coordinator recovered the preimage from the threshold of shares the SOs
	// returned in Prepare — the load-bearing assertion for recoverPreimage.
	assert.Equal(t, preimage[:], response.GetPreimage())
	senderTransfer := response.GetTransfer()

	// Every SO must have the transfer row — Prepare created it on all of them.
	transferUUID, err := uuid.Parse(senderTransfer.GetId())
	require.NoError(t, err)
	for _, i := range operatorIndicesFromConfig(sspConfig) {
		entClient := db.NewPostgresEntClientForIntegrationTest(t, operatorDatabasePath(t, i))
		t.Cleanup(func() { _ = entClient.Close() })
		_, err := entClient.Transfer.Query().Where(transferent.IDEQ(transferUUID)).Only(t.Context())
		require.NoError(t, err, "operator %d missing transfer row after consensus receive swap", i)
	}

	// Delivery + claim complete the flow against the consensus-initiated transfer.
	transfer, err := wallet.DeliverTransferPackage(t.Context(), sspConfig, senderTransfer, leaves, nil)
	require.NoError(t, err)
	assert.Equal(t, spark.TransferStatus_TRANSFER_STATUS_SENDER_KEY_TWEAKED, transfer.GetStatus())

	receiverToken, err := wallet.AuthenticateWithServer(t.Context(), userConfig)
	require.NoError(t, err)
	receiverCtx := wallet.ContextWithToken(t.Context(), receiverToken)
	pendingTransfer, err := wallet.QueryPendingTransfers(receiverCtx, userConfig)
	require.NoError(t, err)
	require.Len(t, pendingTransfer.GetTransfers(), 1)
	receiverTransfer := pendingTransfer.GetTransfers()[0]
	require.Equal(t, senderTransfer.GetId(), receiverTransfer.GetId())

	leafPrivKeyMap, err := wallet.VerifyPendingTransfer(receiverCtx, userConfig, receiverTransfer)
	require.NoError(t, err)
	require.Equal(t, map[string]keys.Private{nodeToSend.GetId(): newLeafPrivKey}, leafPrivKeyMap)

	finalLeafPrivKey := keys.GeneratePrivateKey()
	leavesToClaim := []wallet.LeafKeyTweak{{
		Leaf:              receiverTransfer.GetLeaves()[0].GetLeaf(),
		SigningPrivKey:    newLeafPrivKey,
		NewSigningPrivKey: finalLeafPrivKey,
	}}
	_, err = wallet.ClaimTransfer(receiverCtx, receiverTransfer, userConfig, leavesToClaim)
	require.NoError(t, err, "receiver should be able to claim the consensus receive transfer")
}
