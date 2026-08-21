package grpctest

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/common/secret_sharing/curve"
	pbgossip "github.com/lightsparkdev/spark/proto/gossip"
	sparkpb "github.com/lightsparkdev/spark/proto/spark"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/ent/treenode"
	"github.com/lightsparkdev/spark/so/knobs"
	sparktesting "github.com/lightsparkdev/spark/testing"
	"github.com/lightsparkdev/spark/testing/wallet"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const opTypeMpcSendTransfer = int32(pbgossip.ConsensusOperationType_CONSENSUS_OPERATION_TYPE_MPC_SEND_TRANSFER)

// leafKeysharePublicKeys reads the leaf's signing-keyshare public key from
// every operator's database, keyed by operator index.
func leafKeysharePublicKeys(t *testing.T, operatorIndices []int, leafID uuid.UUID) map[int]keys.Public {
	t.Helper()
	result := make(map[int]keys.Public, len(operatorIndices))
	for _, i := range operatorIndices {
		client := db.NewPostgresEntClientForIntegrationTest(t, operatorDatabasePath(t, i))
		row, err := client.TreeNode.Query().
			Where(treenode.IDEQ(leafID)).
			WithSigningKeyshare().
			Only(t.Context())
		require.NoError(t, err, "operator %d: failed to read leaf %s", i, leafID)
		require.NotNil(t, row.Edges.SigningKeyshare, "operator %d: leaf %s has no signing keyshare", i, leafID)
		result[i] = row.Edges.SigningKeyshare.PublicKey
		client.Close()
	}
	return result
}

// directBearingLeaf ages a fresh deposit into a renewed leaf: only the renew
// flows attach the direct transactions, and a multiparty submission requires
// all three refund variants.
func directBearingLeaf(t *testing.T, config *wallet.TestWalletConfig, leafPrivKey keys.Private) *sparkpb.TreeNode {
	t.Helper()
	rootNode, err := wallet.CreateNewTree(config, faucet, leafPrivKey, amountSatsToSend)
	require.NoError(t, err, "failed to create new tree")

	authToken, err := wallet.AuthenticateWithServer(t.Context(), config)
	require.NoError(t, err)
	ctx := wallet.ContextWithToken(t.Context(), authToken)

	modifyNodeTimelockAllOperators(t, config, rootNode.GetId(), 0, timelockBelowRenewThreshold)
	leaf := queryLeafByID(t, config, authToken, rootNode.GetId())
	leafAfterZeroRenew, err := wallet.RenewNodeZeroTimelock(ctx, config, leaf, leafPrivKey)
	require.NoError(t, err, "failed to renew zero timelock")

	modifyNodeTimelockAllOperators(t, config, leafAfterZeroRenew.GetId(), timelockBelowRenewThreshold, timelockBelowRenewThreshold)
	queriedLeaf := queryLeafByID(t, config, authToken, leafAfterZeroRenew.GetId())
	require.NotNil(t, queriedLeaf.ParentNodeId)
	parentLeaf := queryLeafByID(t, config, authToken, queriedLeaf.GetParentNodeId())
	renewedLeaf, err := wallet.RenewNodeTimelock(ctx, config, queriedLeaf, parentLeaf, leafPrivKey)
	require.NoError(t, err, "failed to renew node timelock")

	finalLeaf := queryLeafByID(t, config, authToken, renewedLeaf.GetId())
	require.NotEmpty(t, finalLeaf.GetDirectTx(), "renewed leaf should carry a direct tx")
	require.NotEmpty(t, finalLeaf.GetDirectRefundTx(), "renewed leaf should carry a direct refund tx")
	require.Equal(t, "AVAILABLE", finalLeaf.GetStatus())
	return finalLeaf
}

// TestMpcSendTransfer_EndToEnd drives one full multiparty (user-side MPC)
// send through the live cluster with the feature knob forced on — sub-user
// Shamir shares, live FROST refund contributions through the local signer,
// sealed tweak sub-shares, group-signed authorization — then has a
// single-party receiver claim the transfer, proving interop. Operator state
// is inspected directly: one COMMITTED MPC_SEND_TRANSFER FlowExecution row
// per operator sharing one id, and every operator's keyshare public key
// shifted by exactly the tweak point (owner key minus mask), leaving the leaf
// verifying key unchanged.
//
// Cannot be t.Parallel()'d: the flow-execution snapshot delta assumes no
// concurrent MPC_SEND_TRANSFER rows.
func TestMpcSendTransfer_EndToEnd(t *testing.T) {
	if !sparktesting.HasLocalSparkIngressHost() {
		t.Skip("skipping cross-operator integration test without minikube ingress (set SPARK_LOCAL_INGRESS_HOST)")
	}

	kc, err := sparktesting.NewKnobController(t)
	require.NoError(t, err)
	require.NoError(t, kc.SetKnob(t, knobs.KnobMpcTransferEnabled, 1))

	senderConfig := wallet.NewTestWalletConfig(t)
	coordinatorIdx := int(senderConfig.SigningOperators[senderConfig.CoordinatorIdentifier].ID)
	operatorIndices := operatorIndicesFromConfig(senderConfig)

	leafPrivKey := keys.GeneratePrivateKey()
	renewedLeaf := directBearingLeaf(t, senderConfig, leafPrivKey)
	leafUUID, err := uuid.Parse(renewedLeaf.GetId())
	require.NoError(t, err)

	preExistingIDs := make(map[int]map[uuid.UUID]struct{}, len(operatorIndices))
	for _, i := range operatorIndices {
		preExistingIDs[i] = snapshotFlowExecutionIDs(t, operatorDatabasePath(t, i))
	}
	keysharesBefore := leafKeysharePublicKeys(t, operatorIndices, leafUUID)

	mask := keys.GeneratePrivateKey()
	receiverPrivKey := keys.GeneratePrivateKey()
	senderTransfer, err := wallet.SendTransferMpc(
		t.Context(),
		senderConfig,
		[]wallet.LeafKeyTweak{{Leaf: renewedLeaf, SigningPrivKey: leafPrivKey, NewSigningPrivKey: mask}},
		receiverPrivKey.Public(),
		time.Now().Add(10*time.Minute),
		[]uint32{1, 2},
	)
	require.NoError(t, err, "failed to send MPC transfer")
	require.Equal(t, sparkpb.TransferStatus_TRANSFER_STATUS_SENDER_KEY_TWEAKED, senderTransfer.GetStatus())

	// Inspection point 1: exactly one new COMMITTED MPC_SEND_TRANSFER
	// FlowExecution row per operator, all sharing the engine's execution id.
	newRowsByOperator := make(map[int][]*ent.FlowExecution, len(operatorIndices))
	for _, i := range operatorIndices {
		for _, r := range newFlowExecutionsSince(t, operatorDatabasePath(t, i), preExistingIDs[i]) {
			if r.OpType == opTypeMpcSendTransfer {
				newRowsByOperator[i] = append(newRowsByOperator[i], r)
			}
		}
		require.Len(t, newRowsByOperator[i], 1, "operator %d must write exactly one new MPC_SEND_TRANSFER FlowExecution row", i)
	}
	sharedID := newRowsByOperator[coordinatorIdx][0].ID
	for _, i := range operatorIndices {
		row := newRowsByOperator[i][0]
		assert.Equal(t, sharedID, row.ID, "operator %d FlowExecution id must match coordinator's", i)
		assert.Equal(t, st.FlowExecutionStatusCommitted, row.Status,
			"operator %d FlowExecution must be COMMITTED after a successful MPC transfer", i)
		if i == coordinatorIdx {
			assert.Equal(t, st.FlowExecutionRoleCoordinator, row.Role)
		} else {
			assert.Equal(t, st.FlowExecutionRoleParticipant, row.Role, "operator %d should be PARTICIPANT", i)
		}
	}

	// Inspection point 2: the commit rotated every operator's keyshare by
	// exactly the tweak point (owner signing key minus mask), so the leaf's
	// verifying key is preserved while the old owner key is invalidated.
	ownerScalar, err := curve.ParseScalar(leafPrivKey.Serialize())
	require.NoError(t, err)
	maskScalar, err := curve.ParseScalar(mask.Serialize())
	require.NoError(t, err)
	tweakPriv, err := keys.ParsePrivateKey(ownerScalar.Sub(maskScalar).Serialize())
	require.NoError(t, err)
	keysharesAfter := leafKeysharePublicKeys(t, operatorIndices, leafUUID)
	for _, i := range operatorIndices {
		expected := keysharesBefore[i].Add(tweakPriv.Public())
		assert.Equal(t, expected, keysharesAfter[i],
			"operator %d keyshare public key must shift by the tweak point", i)
	}

	// A deployed single-party receiver completes the flow unchanged: the mask
	// delivered in the leaf's secret cipher is its new signing key.
	receiverConfig := wallet.NewTestWalletConfigWithIdentityKey(t, receiverPrivKey)
	receiverToken, err := wallet.AuthenticateWithServer(t.Context(), receiverConfig)
	require.NoError(t, err)
	receiverCtx := wallet.ContextWithToken(t.Context(), receiverToken)
	pendingTransfer, err := wallet.QueryPendingTransfers(receiverCtx, receiverConfig)
	require.NoError(t, err)
	require.Len(t, pendingTransfer.GetTransfers(), 1)
	receiverTransfer := pendingTransfer.GetTransfers()[0]
	require.Equal(t, senderTransfer.GetId(), receiverTransfer.GetId())

	leafPrivKeyMap, err := wallet.VerifyPendingTransfer(t.Context(), receiverConfig, receiverTransfer)
	require.NoError(t, err)
	require.Equal(t, map[string]keys.Private{renewedLeaf.GetId(): mask}, leafPrivKeyMap)

	finalLeafPrivKey := keys.GeneratePrivateKey()
	claimingNode := wallet.LeafKeyTweak{
		Leaf:              receiverTransfer.GetLeaves()[0].GetLeaf(),
		SigningPrivKey:    mask,
		NewSigningPrivKey: finalLeafPrivKey,
	}
	res, err := wallet.ClaimTransfer(receiverCtx, receiverTransfer, receiverConfig, []wallet.LeafKeyTweak{claimingNode})
	require.NoError(t, err, "failed to claim MPC transfer")
	require.Equal(t, renewedLeaf.GetId(), res[0].GetId())
}

// TestMpcSendTransfer_KnobOffRejected pins the rollout gate live: with the
// knob at its default (off), the endpoint aborts with UNIMPLEMENTED before any
// state is written. An otherwise-valid submission is used, so only the gate
// can explain the rejection.
//
// Cannot be t.Parallel()'d: the flow-execution snapshot delta assumes no
// concurrent MPC_SEND_TRANSFER rows.
func TestMpcSendTransfer_KnobOffRejected(t *testing.T) {
	if !sparktesting.HasLocalSparkIngressHost() {
		t.Skip("skipping cross-operator integration test without minikube ingress (set SPARK_LOCAL_INGRESS_HOST)")
	}

	kc, err := sparktesting.NewKnobController(t)
	require.NoError(t, err)
	require.NoError(t, kc.SetKnob(t, knobs.KnobMpcTransferEnabled, 0))

	senderConfig := wallet.NewTestWalletConfig(t)
	operatorIndices := operatorIndicesFromConfig(senderConfig)
	leafPrivKey := keys.GeneratePrivateKey()
	renewedLeaf := directBearingLeaf(t, senderConfig, leafPrivKey)

	preExistingIDs := make(map[int]map[uuid.UUID]struct{}, len(operatorIndices))
	for _, i := range operatorIndices {
		preExistingIDs[i] = snapshotFlowExecutionIDs(t, operatorDatabasePath(t, i))
	}

	mask := keys.GeneratePrivateKey()
	receiverPrivKey := keys.GeneratePrivateKey()
	_, err = wallet.SendTransferMpc(
		t.Context(),
		senderConfig,
		[]wallet.LeafKeyTweak{{Leaf: renewedLeaf, SigningPrivKey: leafPrivKey, NewSigningPrivKey: mask}},
		receiverPrivKey.Public(),
		time.Now().Add(10*time.Minute),
		[]uint32{1, 2},
	)
	require.Error(t, err, "MPC transfer must be rejected with the knob off")
	require.Equal(t, codes.Unimplemented, status.Code(err),
		"knob-off rejection must come from the feature gate, got: %v", err)

	// The gate fires before the consensus engine runs, so no operator may have
	// written an MPC_SEND_TRANSFER flow execution.
	for _, i := range operatorIndices {
		for _, r := range newFlowExecutionsSince(t, operatorDatabasePath(t, i), preExistingIDs[i]) {
			assert.NotEqual(t, opTypeMpcSendTransfer, r.OpType,
				"operator %d wrote an MPC_SEND_TRANSFER FlowExecution row (%s) with the knob off", i, r.ID)
		}
	}
}
