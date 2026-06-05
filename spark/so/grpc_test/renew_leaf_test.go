package grpctest

import (
	"testing"
	"time"

	"github.com/lightsparkdev/spark"
	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/common/keys"
	pbmock "github.com/lightsparkdev/spark/proto/mock"
	pb "github.com/lightsparkdev/spark/proto/spark"
	sparktesting "github.com/lightsparkdev/spark/testing"
	"github.com/lightsparkdev/spark/testing/wallet"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// timelockBelowRenewThreshold is a timelock value that triggers renewal eligibility.
const timelockBelowRenewThreshold = spark.RenewTimelockThreshold - spark.TimeLockInterval

func modifyNodeTimelockAllOperators(t *testing.T, config *wallet.TestWalletConfig, nodeID string, nodeTimelock, refundTimelock uint32) {
	for _, operator := range config.SigningOperators {
		func() {
			conn, err := operator.NewOperatorGRPCConnection()
			require.NoError(t, err)
			defer conn.Close()
			mockClient := pbmock.NewMockServiceClient(conn)
			_, err = mockClient.ModifyNodeTimelock(t.Context(), &pbmock.ModifyNodeTimelockRequest{
				NodeId:         nodeID,
				NodeTimelock:   nodeTimelock,
				RefundTimelock: refundTimelock,
			})
			require.NoError(t, err)
		}()
	}
}

func getTimelockFromTxBytes(t *testing.T, rawTx []byte) uint32 {
	tx, err := common.TxFromRawTxBytes(rawTx)
	require.NoError(t, err)
	require.NotEmpty(t, tx.TxIn)
	return tx.TxIn[0].Sequence & 0xffff
}

func queryLeafByID(t *testing.T, config *wallet.TestWalletConfig, authToken string, leafID string) *pb.TreeNode {
	conn, err := config.NewCoordinatorGRPCConnection()
	require.NoError(t, err)
	defer conn.Close()
	sparkClient := pb.NewSparkServiceClient(conn)
	ctx := wallet.ContextWithToken(t.Context(), authToken)
	resp, err := sparkClient.QueryNodes(ctx, &pb.QueryNodesRequest{
		Source: &pb.QueryNodesRequest_NodeIds{NodeIds: &pb.TreeNodeIds{NodeIds: []string{leafID}}},
	})
	require.NoError(t, err)
	require.Contains(t, resp.GetNodes(), leafID)
	return resp.GetNodes()[leafID]
}

func transferAndClaimSingleLeaf(
	t *testing.T,
	senderConfig *wallet.TestWalletConfig,
	leaf *pb.TreeNode,
	signingPrivKey keys.Private,
) (*wallet.TestWalletConfig, *pb.TreeNode, keys.Private) {
	t.Helper()

	transferSigningPrivKey := keys.GeneratePrivateKey()
	receiverIdentityPrivKey := keys.GeneratePrivateKey()
	senderTransfer, err := wallet.SendTransferWithKeyTweaks(
		t.Context(),
		senderConfig,
		[]wallet.LeafKeyTweak{{
			Leaf:              leaf,
			SigningPrivKey:    signingPrivKey,
			NewSigningPrivKey: transferSigningPrivKey,
		}},
		receiverIdentityPrivKey.Public(),
		time.Now().Add(10*time.Minute),
	)
	require.NoError(t, err, "failed to send transfer for leaf %s", leaf.GetId())

	receiverConfig := wallet.NewTestWalletConfigWithIdentityKey(t, receiverIdentityPrivKey)
	receiverToken, err := wallet.AuthenticateWithServer(t.Context(), receiverConfig)
	require.NoError(t, err)
	receiverCtx := wallet.ContextWithToken(t.Context(), receiverToken)

	pendingTransfers, err := wallet.QueryPendingTransfers(receiverCtx, receiverConfig)
	require.NoError(t, err)
	var receiverTransfer *pb.Transfer
	for _, transfer := range pendingTransfers.GetTransfers() {
		if transfer.GetId() == senderTransfer.GetId() {
			receiverTransfer = transfer
			break
		}
	}
	require.NotNil(t, receiverTransfer, "receiver should see pending transfer %s", senderTransfer.GetId())
	require.Len(t, receiverTransfer.GetLeaves(), 1)

	transferSecrets, err := wallet.VerifyPendingTransfer(t.Context(), receiverConfig, receiverTransfer)
	require.NoError(t, err)
	require.Equal(t, transferSigningPrivKey, transferSecrets[leaf.GetId()])

	claimedSigningPrivKey := keys.GeneratePrivateKey()
	claimedNodes, err := wallet.ClaimTransfer(
		receiverCtx,
		receiverTransfer,
		receiverConfig,
		[]wallet.LeafKeyTweak{{
			Leaf:              receiverTransfer.GetLeaves()[0].GetLeaf(),
			SigningPrivKey:    transferSigningPrivKey,
			NewSigningPrivKey: claimedSigningPrivKey,
		}},
	)
	require.NoError(t, err, "failed to claim transfer %s", senderTransfer.GetId())
	require.Len(t, claimedNodes, 1)
	require.Equal(t, leaf.GetId(), claimedNodes[0].GetId())
	require.Equal(t, "AVAILABLE", claimedNodes[0].GetStatus())

	return receiverConfig, claimedNodes[0], claimedSigningPrivKey
}

func TestRenewNodeZeroTimelock(t *testing.T) {
	config := wallet.NewTestWalletConfig(t)
	leafPrivKey := keys.GeneratePrivateKey()
	rootNode, err := wallet.CreateNewTree(config, faucet, leafPrivKey, 100000)
	require.NoError(t, err)
	require.Equal(t, "AVAILABLE", rootNode.GetStatus())

	nodeTimelock := getTimelockFromTxBytes(t, rootNode.GetNodeTx())
	refundTimelock := getTimelockFromTxBytes(t, rootNode.GetRefundTx())
	require.Equal(t, uint32(0), nodeTimelock, "fresh deposit should have node_tx timelock 0")
	require.Equal(t, uint32(2000), refundTimelock, "fresh deposit should have refund_tx timelock 2000")

	// Mock: reduce refund_tx timelock to 200 across all SOs
	modifyNodeTimelockAllOperators(t, config, rootNode.GetId(), 0, timelockBelowRenewThreshold)

	authToken, err := wallet.AuthenticateWithServer(t.Context(), config)
	require.NoError(t, err)
	ctx := wallet.ContextWithToken(t.Context(), authToken)

	// Re-query to get updated state
	leaf := queryLeafByID(t, config, authToken, rootNode.GetId())

	renewedLeaf, err := wallet.RenewNodeZeroTimelock(ctx, config, leaf, leafPrivKey)
	require.NoError(t, err)
	require.NotNil(t, renewedLeaf)

	renewedNodeTimelock := getTimelockFromTxBytes(t, renewedLeaf.GetNodeTx())
	renewedRefundTimelock := getTimelockFromTxBytes(t, renewedLeaf.GetRefundTx())
	require.Equal(t, uint32(0), renewedNodeTimelock, "renewed node_tx should have timelock 0")
	require.Equal(t, uint32(2000), renewedRefundTimelock, "renewed refund_tx should have timelock 2000")
	require.Equal(t, "AVAILABLE", renewedLeaf.GetStatus())

	queriedLeaf := queryLeafByID(t, config, authToken, renewedLeaf.GetId())
	require.Equal(t, "AVAILABLE", queriedLeaf.GetStatus())
}

func TestRenewNodeZeroTimelockAfterRepeatedTransfers(t *testing.T) {
	config := wallet.NewTestWalletConfig(t)
	signingPrivKey := keys.GeneratePrivateKey()
	leaf, err := wallet.CreateNewTree(config, faucet, signingPrivKey, 100000)
	require.NoError(t, err)
	require.Equal(t, "AVAILABLE", leaf.GetStatus())

	previousRefundTimelock := getTimelockFromTxBytes(t, leaf.GetRefundTx())
	require.Equal(t, spark.InitialTimeLock, previousRefundTimelock)
	require.Equal(t, uint32(spark.ZeroTimelock), getTimelockFromTxBytes(t, leaf.GetNodeTx()))

	const maxTransfersToRenewThreshold = 25
	transfers := 0
	for previousRefundTimelock > spark.RenewTimelockThreshold {
		require.Less(t, transfers, maxTransfersToRenewThreshold, "refund timelock did not reach renew threshold")

		config, leaf, signingPrivKey = transferAndClaimSingleLeaf(t, config, leaf, signingPrivKey)
		refundTimelock := getTimelockFromTxBytes(t, leaf.GetRefundTx())
		nodeTimelock := getTimelockFromTxBytes(t, leaf.GetNodeTx())

		require.Equal(t, uint32(spark.ZeroTimelock), nodeTimelock, "transfer chain should still use the zero-node renew path")
		require.Less(t, refundTimelock, previousRefundTimelock, "each completed transfer must reduce the refund timelock")

		previousRefundTimelock = refundTimelock
		transfers++
	}
	require.GreaterOrEqual(t, previousRefundTimelock, uint32(spark.TimeLockInterval))

	authToken, err := wallet.AuthenticateWithServer(t.Context(), config)
	require.NoError(t, err)
	ctx := wallet.ContextWithToken(t.Context(), authToken)
	leaf = queryLeafByID(t, config, authToken, leaf.GetId())

	renewedLeaf, err := wallet.RenewNodeZeroTimelock(ctx, config, leaf, signingPrivKey)
	require.NoError(t, err)
	require.Equal(t, "AVAILABLE", renewedLeaf.GetStatus())
	require.Equal(t, uint32(spark.ZeroTimelock), getTimelockFromTxBytes(t, renewedLeaf.GetNodeTx()))
	require.Equal(t, spark.InitialTimeLock, getTimelockFromTxBytes(t, renewedLeaf.GetRefundTx()))

	_, postRenewLeaf, _ := transferAndClaimSingleLeaf(t, config, renewedLeaf, signingPrivKey)
	require.Equal(t, "AVAILABLE", postRenewLeaf.GetStatus())
	require.Less(t, getTimelockFromTxBytes(t, postRenewLeaf.GetRefundTx()), spark.InitialTimeLock)
}

func TestRenewNodeTimelock(t *testing.T) {
	config := wallet.NewTestWalletConfig(t)
	leafPrivKey := keys.GeneratePrivateKey()
	rootNode, err := wallet.CreateNewTree(config, faucet, leafPrivKey, 100000)
	require.NoError(t, err)

	authToken, err := wallet.AuthenticateWithServer(t.Context(), config)
	require.NoError(t, err)
	ctx := wallet.ContextWithToken(t.Context(), authToken)

	// Fresh deposit has no parent. First do renewNodeZeroTimelock to create one.
	modifyNodeTimelockAllOperators(t, config, rootNode.GetId(), 0, timelockBelowRenewThreshold)
	leaf := queryLeafByID(t, config, authToken, rootNode.GetId())
	leafAfterZeroRenew, err := wallet.RenewNodeZeroTimelock(ctx, config, leaf, leafPrivKey)
	require.NoError(t, err)

	// Now the leaf has a parent (the split node). Mock node_tx (currently 0) and refund_tx (currently 2000) both below the renewal threshold to trigger RenewNodeTimelock.
	modifyNodeTimelockAllOperators(t, config, leafAfterZeroRenew.GetId(), timelockBelowRenewThreshold, timelockBelowRenewThreshold)

	queriedLeaf := queryLeafByID(t, config, authToken, leafAfterZeroRenew.GetId())
	require.NotNil(t, queriedLeaf.ParentNodeId, "leaf should have a parent node after renewNodeZeroTimelock")
	parentLeaf := queryLeafByID(t, config, authToken, queriedLeaf.GetParentNodeId())
	require.NotNil(t, parentLeaf)

	renewedLeaf, err := wallet.RenewNodeTimelock(ctx, config, queriedLeaf, parentLeaf, leafPrivKey)
	require.NoError(t, err)
	require.NotNil(t, renewedLeaf)

	renewedNodeTimelock := getTimelockFromTxBytes(t, renewedLeaf.GetNodeTx())
	renewedRefundTimelock := getTimelockFromTxBytes(t, renewedLeaf.GetRefundTx())
	require.Equal(t, uint32(2000), renewedNodeTimelock, "renewed node_tx should have timelock 2000")
	require.Equal(t, uint32(2000), renewedRefundTimelock, "renewed refund_tx should have timelock 2000")
	require.Equal(t, "AVAILABLE", renewedLeaf.GetStatus())

	queriedRenewedLeaf := queryLeafByID(t, config, authToken, renewedLeaf.GetId())
	require.Equal(t, "AVAILABLE", queriedRenewedLeaf.GetStatus())
}

func TestRenewRefundTimelock(t *testing.T) {
	config := wallet.NewTestWalletConfig(t)
	leafPrivKey := keys.GeneratePrivateKey()
	rootNode, err := wallet.CreateNewTree(config, faucet, leafPrivKey, 100000)
	require.NoError(t, err)

	authToken, err := wallet.AuthenticateWithServer(t.Context(), config)
	require.NoError(t, err)
	ctx := wallet.ContextWithToken(t.Context(), authToken)

	// Step 1: Get to a state where node_tx > 300.
	// Fresh deposit has node_tx = 0, refund_tx = 2000.
	// Use renewNodeZeroTimelock first to establish the pattern, then renewNodeTimelock to get node_tx = 2000.

	// Mock refund to low, then renewNodeZeroTimelock
	modifyNodeTimelockAllOperators(t, config, rootNode.GetId(), 0, timelockBelowRenewThreshold)
	leaf := queryLeafByID(t, config, authToken, rootNode.GetId())
	renewedLeaf, err := wallet.RenewNodeZeroTimelock(ctx, config, leaf, leafPrivKey)
	require.NoError(t, err)

	// After renewNodeZeroTimelock: node_tx = 0, refund = 2000
	// Mock both to <= 300 for renewNodeTimelock
	modifyNodeTimelockAllOperators(t, config, renewedLeaf.GetId(), timelockBelowRenewThreshold, timelockBelowRenewThreshold)

	queriedLeaf := queryLeafByID(t, config, authToken, renewedLeaf.GetId())
	require.NotNil(t, queriedLeaf.ParentNodeId)
	parentLeaf := queryLeafByID(t, config, authToken, queriedLeaf.GetParentNodeId())

	renewedLeaf2, err := wallet.RenewNodeTimelock(ctx, config, queriedLeaf, parentLeaf, leafPrivKey)
	require.NoError(t, err)

	// Now node_tx = 2000, refund_tx = 2000
	// For renewRefundTimelock: need refund <= 300, node > 300
	modifyNodeTimelockAllOperators(t, config, renewedLeaf2.GetId(), 2000, timelockBelowRenewThreshold)

	queriedLeaf2 := queryLeafByID(t, config, authToken, renewedLeaf2.GetId())
	require.NotNil(t, queriedLeaf2.ParentNodeId)
	parentLeaf2 := queryLeafByID(t, config, authToken, queriedLeaf2.GetParentNodeId())

	renewedLeaf3, err := wallet.RenewRefundTimelock(ctx, config, queriedLeaf2, parentLeaf2, leafPrivKey)
	require.NoError(t, err)
	require.NotNil(t, renewedLeaf3)

	renewedNodeTimelock := getTimelockFromTxBytes(t, renewedLeaf3.GetNodeTx())
	renewedRefundTimelock := getTimelockFromTxBytes(t, renewedLeaf3.GetRefundTx())
	require.Equal(t, uint32(1900), renewedNodeTimelock, "renewed node_tx should have timelock 1900 (decremented from 2000)")
	require.Equal(t, uint32(2000), renewedRefundTimelock, "renewed refund_tx should have timelock 2000 (reset)")
	require.Equal(t, "AVAILABLE", renewedLeaf3.GetStatus())

	queriedRenewedLeaf := queryLeafByID(t, config, authToken, renewedLeaf3.GetId())
	require.Equal(t, "AVAILABLE", queriedRenewedLeaf.GetStatus())
}

// queryLeafStatusOnAllOperators queries the node status on each SO individually.
func queryLeafStatusOnAllOperators(t *testing.T, config *wallet.TestWalletConfig, authToken string, leafID string) map[string]string {
	t.Helper()
	statuses := make(map[string]string)
	ctx := wallet.ContextWithToken(t.Context(), authToken)
	for identifier, operator := range config.SigningOperators {
		func() {
			conn, err := operator.NewOperatorGRPCConnection()
			require.NoError(t, err)
			defer conn.Close()
			sparkClient := pb.NewSparkServiceClient(conn)
			resp, err := sparkClient.QueryNodes(ctx, &pb.QueryNodesRequest{
				Source: &pb.QueryNodesRequest_NodeIds{NodeIds: &pb.TreeNodeIds{NodeIds: []string{leafID}}},
			})
			require.NoError(t, err)
			if node, ok := resp.GetNodes()[leafID]; ok {
				statuses[identifier] = node.GetStatus()
			}
		}()
	}
	return statuses
}

// TestRenewConsensusRollbackUnlocksAllOperators verifies that when a renew
// operation fails after prepare, all SOs have their leaf status reset from
// RenewLocked back to Available.
func TestRenewConsensusRollbackUnlocksAllOperators(t *testing.T) {
	if !sparktesting.HasLocalSparkIngressHost() {
		t.Skip("skipping cross-operator integration test without minikube ingress (set SPARK_LOCAL_INGRESS_HOST)")
	}

	config := wallet.NewTestWalletConfig(t)
	leafPrivKey := keys.GeneratePrivateKey()
	rootNode, err := wallet.CreateNewTree(config, faucet, leafPrivKey, 100000)
	require.NoError(t, err)
	require.Equal(t, "AVAILABLE", rootNode.GetStatus())

	authToken, err := wallet.AuthenticateWithServer(t.Context(), config)
	require.NoError(t, err)

	// Verify all SOs show Available
	statuses := queryLeafStatusOnAllOperators(t, config, authToken, rootNode.GetId())
	for id, status := range statuses {
		assert.Equal(t, "AVAILABLE", status, "SO %s should be Available before renew", id)
	}

	// Mock: reduce refund_tx timelock to trigger renewal
	modifyNodeTimelockAllOperators(t, config, rootNode.GetId(), 0, timelockBelowRenewThreshold)
	leaf := queryLeafByID(t, config, authToken, rootNode.GetId())

	// Attempt a renew with a corrupt signing job to force a failure after prepare.
	// Send an invalid RenewNodeZeroTimelockSigningJob with empty transactions.
	ctx := wallet.ContextWithToken(t.Context(), authToken)
	conn, err := config.NewCoordinatorGRPCConnection()
	require.NoError(t, err)
	defer conn.Close()
	sparkClient := pb.NewSparkServiceClient(conn)

	_, renewErr := sparkClient.RenewLeaf(ctx, &pb.RenewLeafRequest{
		LeafId: leaf.GetId(),
		SigningJobs: &pb.RenewLeafRequest_RenewNodeZeroTimelockSigningJob{
			RenewNodeZeroTimelockSigningJob: &pb.RenewNodeZeroTimelockSigningJob{
				// Empty/nil signing jobs — should fail during validation
			},
		},
	})
	require.Error(t, renewErr, "renew with invalid signing data should fail")

	// Verify all SOs have the leaf back to Available (rollback worked)
	statuses = queryLeafStatusOnAllOperators(t, config, authToken, rootNode.GetId())
	for id, status := range statuses {
		assert.Equal(t, "AVAILABLE", status, "SO %s should be Available after failed renew (rollback)", id)
	}
}
