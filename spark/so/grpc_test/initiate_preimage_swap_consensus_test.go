package grpctest

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/common/keys"
	pbgossip "github.com/lightsparkdev/spark/proto/gossip"
	spark "github.com/lightsparkdev/spark/proto/spark"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/ent/preimagerequest"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	transferent "github.com/lightsparkdev/spark/so/ent/transfer"
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

// preimageSwapNodeValueSats is the leaf value the SEND tests create and assert
// against. Shared so the CreateNewTree call and the refund-sum assertion can't
// drift.
const preimageSwapNodeValueSats = int64(12347)

// TestInitiatePreimageSwapV3_Consensus_SendHappyPath drives a lightning-send
// preimage swap (REASON_SEND with a transfer package — the path that produces
// FROST refund-signature shares in Prepare and aggregates them in
// BuildCommitPayload) through the 2PC engine end-to-end, and verifies:
//   - InitiatePreimageSwapV3 returns a transfer in SENDER_KEY_TWEAK_PENDING
//     (key tweaks are deferred to ProvidePreimage, matching legacy)
//   - every operator's DB has the transfer row in SENDER_KEY_TWEAK_PENDING
//     (proves Prepare ran on every SO — it creates the row in this status)
//   - every operator's stored cpfp refund carries a valid aggregated signature
//     (the Commit-phase check: BuildCommitPayload→UpdateTransferLeavesSignatures
//     applied the FROST signature on every SO; a Commit no-op would leave an
//     unverifiable refund and fail this)
//   - the downstream ProvidePreimage settles the transfer to SENDER_KEY_TWEAKED
//     and the receiver can claim it
//
// This is the load-bearing end-state assertion for the 2PC path. FlowExecution
// row invariants are covered by TestInitiatePreimageSwapV3_Consensus_WritesFlowExecutionRows.
func TestInitiatePreimageSwapV3_Consensus_SendHappyPath(t *testing.T) {
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
		row, err := entClient.Transfer.Query().
			Where(transferent.IDEQ(transferUUID)).
			WithTransferLeaves(func(q *ent.TransferLeafQuery) { q.WithLeaf() }).
			Only(t.Context())
		require.NoError(t, err, "operator %d missing transfer row", i)
		assert.Equal(t, st.TransferStatusSenderKeyTweakPending, row.Status,
			"operator %d transfer status mismatch after consensus initiate preimage swap", i)

		// Commit-phase check: the status above only proves Prepare ran (it creates
		// the row already at SENDER_KEY_TWEAK_PENDING). The applied cpfp refund
		// signature is what proves Commit ran — mirror the handler's own
		// VerifySignatureSingleInput against the node output the refund spends.
		require.NotEmpty(t, row.Edges.TransferLeaves, "operator %d has no transfer leaves", i)
		for _, tl := range row.Edges.TransferLeaves {
			refundTx, err := common.TxFromRawTxBytes(tl.IntermediateRefundTx)
			require.NoError(t, err, "operator %d leaf %s: unparseable cpfp refund tx", i, tl.ID)
			nodeTx, err := common.TxFromRawTxBytes(tl.Edges.Leaf.RawTx)
			require.NoError(t, err, "operator %d leaf %s: unparseable node tx", i, tl.ID)
			require.NoError(t,
				common.VerifySignatureSingleInput(refundTx, 0, nodeTx.TxOut[0]),
				"operator %d leaf %s: cpfp refund signature not applied (Commit-phase no-op?)", i, tl.ID)
		}
	}

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
//     ran through the consensus engine)
//   - every operator's DB has the transfer row (Prepare ran everywhere)
//   - the receiver can complete delivery + claim
//
// A preimage share is pre-stored via CreateLightningInvoiceWithPreimage (which is
// what makes this the non-HODL path rather than HODL).
func TestInitiatePreimageSwapV3_Consensus_ReceiveHappyPath(t *testing.T) {
	if !sparktesting.HasLocalSparkIngressHost() {
		t.Skip("skipping cross-operator integration test without minikube ingress (set SPARK_LOCAL_INGRESS_HOST)")
	}
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

	response, err := wallet.SwapNodesForPreimageWithHTLC(
		t.Context(),
		sspConfig,
		leaves,
		userConfig.IdentityPublicKey(),
		paymentHash[:],
		nil,
		uint64(0), // feeSats: not allowed on receive
		true,      // isInboundPayment: lightning receive
		amountSats,
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

	assert.Equal(t, spark.TransferStatus_TRANSFER_STATUS_SENDER_KEY_TWEAKED, senderTransfer.GetStatus())

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

// TestInitiatePreimageSwapV3_Consensus_ReceiveHodlPath drives a HODL
// lightning-receive preimage swap through the 2PC engine: no invoice is
// registered with the SOs beforehand, so no preimage share exists and the
// swap must defer the preimage instead of recovering it. Verifies:
//   - InitiatePreimageSwapV3 succeeds with an empty preimage and a transfer
//     in SENDER_KEY_TWEAK_PENDING (key tweaks are deferred to ProvidePreimage)
//   - every operator holds the transfer row and a PreimageRequest in
//     WAITING_FOR_PREIMAGE (proves Prepare took the HODL branch on every SO)
//   - the receiver later supplies the preimage via ProvidePreimage, the
//     transfer settles to SENDER_KEY_TWEAKED, and the receiver can claim it
func TestInitiatePreimageSwapV3_Consensus_ReceiveHodlPath(t *testing.T) {
	if !sparktesting.HasLocalSparkIngressHost() {
		t.Skip("skipping cross-operator integration test without minikube ingress (set SPARK_LOCAL_INGRESS_HOST)")
	}
	userConfig := wallet.NewTestWalletConfig(t)
	sspConfig := wallet.NewTestWalletConfig(t)

	amountSats := uint64(100)
	// A preimage distinct from testPreimageHash's fixed values so this test
	// cannot collide with PreimageShare/PreimageRequest rows created by the
	// non-HODL tests that share a payment hash.
	preimage, err := hex.DecodeString("6f1e35a90dd48c8e2bbbf2c744cbef58e0b1c3f2a4d5e60798a1b2c3d4e5f601")
	require.NoError(t, err)
	paymentHash := sha256.Sum256(preimage)
	defer cleanUp(t, userConfig, paymentHash)

	// The SSP funds a leaf to send to the user. Deliberately NO
	// CreateLightningInvoiceWithPreimage: the missing preimage share is what
	// makes this the HODL branch.
	sspLeafPrivKey := keys.GeneratePrivateKey()
	nodeToSend, err := wallet.CreateNewTree(sspConfig, faucet, sspLeafPrivKey, 12345)
	require.NoError(t, err)
	newLeafPrivKey := keys.GeneratePrivateKey()
	leaves := []wallet.LeafKeyTweak{{
		Leaf:              nodeToSend,
		SigningPrivKey:    sspLeafPrivKey,
		NewSigningPrivKey: newLeafPrivKey,
	}}

	response, err := wallet.SwapNodesForPreimageWithHTLC(
		t.Context(),
		sspConfig,
		leaves,
		userConfig.IdentityPublicKey(),
		paymentHash[:],
		nil,
		uint64(0), // feeSats: not allowed on receive
		true,      // isInboundPayment: lightning receive
		amountSats,
	)
	require.NoError(t, err, "HODL receive swap should succeed with a deferred preimage")
	assert.Empty(t, response.GetPreimage(), "HODL initiate must not return a preimage — none exists yet")
	senderTransfer := response.GetTransfer()
	assert.Equal(t, spark.TransferStatus_TRANSFER_STATUS_SENDER_KEY_TWEAK_PENDING, senderTransfer.GetStatus(),
		"HODL receive must leave key tweaks pending until ProvidePreimage")

	// Every SO must hold the transfer row and a WAITING_FOR_PREIMAGE request —
	// the observable proof that Prepare took the HODL branch everywhere.
	transferUUID, err := uuid.Parse(senderTransfer.GetId())
	require.NoError(t, err)
	for _, i := range operatorIndicesFromConfig(sspConfig) {
		entClient := db.NewPostgresEntClientForIntegrationTest(t, operatorDatabasePath(t, i))
		t.Cleanup(func() { _ = entClient.Close() })
		_, err := entClient.Transfer.Query().Where(transferent.IDEQ(transferUUID)).Only(t.Context())
		require.NoError(t, err, "operator %d missing transfer row after HODL receive swap", i)
		reqRow, err := entClient.PreimageRequest.Query().
			Where(preimagerequest.PaymentHash(paymentHash[:])).
			Only(t.Context())
		require.NoError(t, err, "operator %d missing preimage request row after HODL receive swap", i)
		assert.Equal(t, st.PreimageRequestStatusWaitingForPreimage, reqRow.Status,
			"operator %d preimage request must wait for the user's preimage", i)
	}

	// The user reveals the preimage, settling the sender key tweaks.
	receiverTransfer, err := wallet.ProvidePreimage(t.Context(), userConfig, preimage)
	require.NoError(t, err, "ProvidePreimage should settle the HODL receive transfer")
	assert.Equal(t, spark.TransferStatus_TRANSFER_STATUS_SENDER_KEY_TWEAKED, receiverTransfer.GetStatus())
	require.Equal(t, senderTransfer.GetId(), receiverTransfer.GetId())

	receiverToken, err := wallet.AuthenticateWithServer(t.Context(), userConfig)
	require.NoError(t, err)
	receiverCtx := wallet.ContextWithToken(t.Context(), receiverToken)
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
	require.NoError(t, err, "receiver should be able to claim the HODL receive transfer")
}

// TestInitiatePreimageSwapV3_Consensus_PrepareRejectionRollsBack proves the
// public InitiatePreimageSwapV3 surface rejects invalid requests through the
// engine's Prepare phase and leaves no committed state behind. The request
// carries a fee on a RECEIVE swap — a rule enforced only in participant
// Prepare (prepareState), not in the coordinator's validate — so a rejection
// here is direct evidence that the engine invoked the Prepare handler and
// that a participant failure rolls the flow back on every SO.
func TestInitiatePreimageSwapV3_Consensus_PrepareRejectionRollsBack(t *testing.T) {
	if !sparktesting.HasLocalSparkIngressHost() {
		t.Skip("skipping cross-operator integration test without minikube ingress (set SPARK_LOCAL_INGRESS_HOST)")
	}
	userConfig := wallet.NewTestWalletConfig(t)
	sspConfig := wallet.NewTestWalletConfig(t)
	operatorIndices := operatorIndicesFromConfig(sspConfig)

	amountSats := uint64(100)
	// Distinct preimage for the same reason as the HODL test above.
	preimage, err := hex.DecodeString("9c2b7d41e6f30a58c1d2e3f405162738495a6b7c8d9e0f1a2b3c4d5e6f708192")
	require.NoError(t, err)
	paymentHash := sha256.Sum256(preimage)
	defer cleanUp(t, userConfig, paymentHash)

	sspLeafPrivKey := keys.GeneratePrivateKey()
	nodeToSend, err := wallet.CreateNewTree(sspConfig, faucet, sspLeafPrivKey, 12345)
	require.NoError(t, err)
	newLeafPrivKey := keys.GeneratePrivateKey()
	leaves := []wallet.LeafKeyTweak{{
		Leaf:              nodeToSend,
		SigningPrivKey:    sspLeafPrivKey,
		NewSigningPrivKey: newLeafPrivKey,
	}}

	preExistingIDs := make(map[int]map[uuid.UUID]struct{}, len(operatorIndices))
	for _, i := range operatorIndices {
		preExistingIDs[i] = snapshotFlowExecutionIDs(t, operatorDatabasePath(t, i))
	}

	_, err = wallet.SwapNodesForPreimageWithHTLC(
		t.Context(),
		sspConfig,
		leaves,
		userConfig.IdentityPublicKey(),
		paymentHash[:],
		nil,
		uint64(5), // fee on a RECEIVE swap: rejected in Prepare, not coordinator validate
		true,      // isInboundPayment: lightning receive
		amountSats,
	)
	require.ErrorContains(t, err, "fee is not allowed for receive preimage swap",
		"the participant Prepare rejection must surface through the public V3 surface")

	// No preimage request may exist on any SO — the rejected flow must leave no
	// partial state behind. (Rollback of fully-prepared domain state is proven
	// separately by TestReceiveLightningPaymentWithWrongPreimage, whose abort
	// fires after Prepare completed on every SO.)
	for _, i := range operatorIndices {
		entClient := db.NewPostgresEntClientForIntegrationTest(t, operatorDatabasePath(t, i))
		t.Cleanup(func() { _ = entClient.Close() })
		count, err := entClient.PreimageRequest.Query().
			Where(preimagerequest.PaymentHash(paymentHash[:])).
			Count(t.Context())
		require.NoError(t, err)
		assert.Zero(t, count, "operator %d must not keep a preimage request for the rejected swap", i)
	}

	// The coordinator's FlowExecution row is created in a detached engine
	// session before the prepare fan-out, so it survives the abort and MUST
	// converge to ROLLED_BACK — requiring it keeps this assertion from passing
	// vacuously. Participants write their row on the same tx as their prepare
	// work, so a rejected prepare legitimately leaves them rowless; any row
	// they did write must also converge to ROLLED_BACK.
	coordinatorIdx := int(sspConfig.SigningOperators[sspConfig.CoordinatorIdentifier].ID)
	require.Eventually(t, func() bool {
		coordinatorRolledBackRows := 0
		for _, i := range operatorIndices {
			for _, row := range newFlowExecutionsSince(t, operatorDatabasePath(t, i), preExistingIDs[i]) {
				if row.OpType != opTypeInitiatePreimageSwap {
					continue
				}
				if row.Status != st.FlowExecutionStatusRolledBack {
					return false
				}
				if i == coordinatorIdx {
					coordinatorRolledBackRows++
				}
			}
		}
		return coordinatorRolledBackRows >= 1
	}, 30*time.Second, time.Second,
		"the coordinator must durably record a ROLLED_BACK INITIATE_PREIMAGE_SWAP FlowExecution row, and every other row must converge to ROLLED_BACK")
}
