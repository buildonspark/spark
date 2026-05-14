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
	require.Contains(t, resp.Nodes, leafID)
	return resp.Nodes[leafID]
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
	require.NoError(t, err, "failed to send transfer for leaf %s", leaf.Id)

	receiverConfig := wallet.NewTestWalletConfigWithIdentityKey(t, receiverIdentityPrivKey)
	receiverToken, err := wallet.AuthenticateWithServer(t.Context(), receiverConfig)
	require.NoError(t, err)
	receiverCtx := wallet.ContextWithToken(t.Context(), receiverToken)

	pendingTransfers, err := wallet.QueryPendingTransfers(receiverCtx, receiverConfig)
	require.NoError(t, err)
	var receiverTransfer *pb.Transfer
	for _, transfer := range pendingTransfers.Transfers {
		if transfer.Id == senderTransfer.Id {
			receiverTransfer = transfer
			break
		}
	}
	require.NotNil(t, receiverTransfer, "receiver should see pending transfer %s", senderTransfer.Id)
	require.Len(t, receiverTransfer.Leaves, 1)

	transferSecrets, err := wallet.VerifyPendingTransfer(t.Context(), receiverConfig, receiverTransfer)
	require.NoError(t, err)
	require.Equal(t, transferSigningPrivKey, transferSecrets[leaf.Id])

	claimedSigningPrivKey := keys.GeneratePrivateKey()
	claimedNodes, err := wallet.ClaimTransfer(
		receiverCtx,
		receiverTransfer,
		receiverConfig,
		[]wallet.LeafKeyTweak{{
			Leaf:              receiverTransfer.Leaves[0].Leaf,
			SigningPrivKey:    transferSigningPrivKey,
			NewSigningPrivKey: claimedSigningPrivKey,
		}},
	)
	require.NoError(t, err, "failed to claim transfer %s", senderTransfer.Id)
	require.Len(t, claimedNodes, 1)
	require.Equal(t, leaf.Id, claimedNodes[0].Id)
	require.Equal(t, "AVAILABLE", claimedNodes[0].Status)

	return receiverConfig, claimedNodes[0], claimedSigningPrivKey
}

func TestRenewNodeZeroTimelock(t *testing.T) {
	config := wallet.NewTestWalletConfig(t)
	leafPrivKey := keys.GeneratePrivateKey()
	rootNode, err := wallet.CreateNewTree(config, faucet, leafPrivKey, 100000)
	require.NoError(t, err)
	require.Equal(t, "AVAILABLE", rootNode.Status)

	nodeTimelock := getTimelockFromTxBytes(t, rootNode.NodeTx)
	refundTimelock := getTimelockFromTxBytes(t, rootNode.RefundTx)
	require.Equal(t, uint32(0), nodeTimelock, "fresh deposit should have node_tx timelock 0")
	require.Equal(t, uint32(2000), refundTimelock, "fresh deposit should have refund_tx timelock 2000")

	// Mock: reduce refund_tx timelock to 200 across all SOs
	modifyNodeTimelockAllOperators(t, config, rootNode.Id, 0, timelockBelowRenewThreshold)

	authToken, err := wallet.AuthenticateWithServer(t.Context(), config)
	require.NoError(t, err)
	ctx := wallet.ContextWithToken(t.Context(), authToken)

	// Re-query to get updated state
	leaf := queryLeafByID(t, config, authToken, rootNode.Id)

	renewedLeaf, err := wallet.RenewNodeZeroTimelock(ctx, config, leaf, leafPrivKey)
	require.NoError(t, err)
	require.NotNil(t, renewedLeaf)

	renewedNodeTimelock := getTimelockFromTxBytes(t, renewedLeaf.NodeTx)
	renewedRefundTimelock := getTimelockFromTxBytes(t, renewedLeaf.RefundTx)
	require.Equal(t, uint32(0), renewedNodeTimelock, "renewed node_tx should have timelock 0")
	require.Equal(t, uint32(2000), renewedRefundTimelock, "renewed refund_tx should have timelock 2000")
	require.Equal(t, "AVAILABLE", renewedLeaf.Status)

	queriedLeaf := queryLeafByID(t, config, authToken, renewedLeaf.Id)
	require.Equal(t, "AVAILABLE", queriedLeaf.Status)
}

func TestRenewNodeZeroTimelockAfterRepeatedTransfers(t *testing.T) {
	config := wallet.NewTestWalletConfig(t)
	signingPrivKey := keys.GeneratePrivateKey()
	leaf, err := wallet.CreateNewTree(config, faucet, signingPrivKey, 100000)
	require.NoError(t, err)
	require.Equal(t, "AVAILABLE", leaf.Status)

	previousRefundTimelock := getTimelockFromTxBytes(t, leaf.RefundTx)
	require.Equal(t, spark.InitialTimeLock, previousRefundTimelock)
	require.Equal(t, uint32(spark.ZeroTimelock), getTimelockFromTxBytes(t, leaf.NodeTx))

	const maxTransfersToRenewThreshold = 25
	transfers := 0
	for previousRefundTimelock > spark.RenewTimelockThreshold {
		require.Less(t, transfers, maxTransfersToRenewThreshold, "refund timelock did not reach renew threshold")

		config, leaf, signingPrivKey = transferAndClaimSingleLeaf(t, config, leaf, signingPrivKey)
		refundTimelock := getTimelockFromTxBytes(t, leaf.RefundTx)
		nodeTimelock := getTimelockFromTxBytes(t, leaf.NodeTx)

		require.Equal(t, uint32(spark.ZeroTimelock), nodeTimelock, "transfer chain should still use the zero-node renew path")
		require.Less(t, refundTimelock, previousRefundTimelock, "each completed transfer must reduce the refund timelock")

		previousRefundTimelock = refundTimelock
		transfers++
	}
	require.GreaterOrEqual(t, previousRefundTimelock, uint32(spark.TimeLockInterval))

	authToken, err := wallet.AuthenticateWithServer(t.Context(), config)
	require.NoError(t, err)
	ctx := wallet.ContextWithToken(t.Context(), authToken)
	leaf = queryLeafByID(t, config, authToken, leaf.Id)

	renewedLeaf, err := wallet.RenewNodeZeroTimelock(ctx, config, leaf, signingPrivKey)
	require.NoError(t, err)
	require.Equal(t, "AVAILABLE", renewedLeaf.Status)
	require.Equal(t, uint32(spark.ZeroTimelock), getTimelockFromTxBytes(t, renewedLeaf.NodeTx))
	require.Equal(t, spark.InitialTimeLock, getTimelockFromTxBytes(t, renewedLeaf.RefundTx))

	_, postRenewLeaf, _ := transferAndClaimSingleLeaf(t, config, renewedLeaf, signingPrivKey)
	require.Equal(t, "AVAILABLE", postRenewLeaf.Status)
	require.Less(t, getTimelockFromTxBytes(t, postRenewLeaf.RefundTx), spark.InitialTimeLock)
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
	modifyNodeTimelockAllOperators(t, config, rootNode.Id, 0, timelockBelowRenewThreshold)
	leaf := queryLeafByID(t, config, authToken, rootNode.Id)
	leafAfterZeroRenew, err := wallet.RenewNodeZeroTimelock(ctx, config, leaf, leafPrivKey)
	require.NoError(t, err)

	// Now the leaf has a parent (the split node). Mock node_tx (currently 0) and refund_tx (currently 2000) both below the renewal threshold to trigger RenewNodeTimelock.
	modifyNodeTimelockAllOperators(t, config, leafAfterZeroRenew.Id, timelockBelowRenewThreshold, timelockBelowRenewThreshold)

	queriedLeaf := queryLeafByID(t, config, authToken, leafAfterZeroRenew.Id)
	require.NotNil(t, queriedLeaf.ParentNodeId, "leaf should have a parent node after renewNodeZeroTimelock")
	parentLeaf := queryLeafByID(t, config, authToken, *queriedLeaf.ParentNodeId)
	require.NotNil(t, parentLeaf)

	renewedLeaf, err := wallet.RenewNodeTimelock(ctx, config, queriedLeaf, parentLeaf, leafPrivKey)
	require.NoError(t, err)
	require.NotNil(t, renewedLeaf)

	renewedNodeTimelock := getTimelockFromTxBytes(t, renewedLeaf.NodeTx)
	renewedRefundTimelock := getTimelockFromTxBytes(t, renewedLeaf.RefundTx)
	require.Equal(t, uint32(2000), renewedNodeTimelock, "renewed node_tx should have timelock 2000")
	require.Equal(t, uint32(2000), renewedRefundTimelock, "renewed refund_tx should have timelock 2000")
	require.Equal(t, "AVAILABLE", renewedLeaf.Status)

	queriedRenewedLeaf := queryLeafByID(t, config, authToken, renewedLeaf.Id)
	require.Equal(t, "AVAILABLE", queriedRenewedLeaf.Status)
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
	modifyNodeTimelockAllOperators(t, config, rootNode.Id, 0, timelockBelowRenewThreshold)
	leaf := queryLeafByID(t, config, authToken, rootNode.Id)
	renewedLeaf, err := wallet.RenewNodeZeroTimelock(ctx, config, leaf, leafPrivKey)
	require.NoError(t, err)

	// After renewNodeZeroTimelock: node_tx = 0, refund = 2000
	// Mock both to <= 300 for renewNodeTimelock
	modifyNodeTimelockAllOperators(t, config, renewedLeaf.Id, timelockBelowRenewThreshold, timelockBelowRenewThreshold)

	queriedLeaf := queryLeafByID(t, config, authToken, renewedLeaf.Id)
	require.NotNil(t, queriedLeaf.ParentNodeId)
	parentLeaf := queryLeafByID(t, config, authToken, *queriedLeaf.ParentNodeId)

	renewedLeaf2, err := wallet.RenewNodeTimelock(ctx, config, queriedLeaf, parentLeaf, leafPrivKey)
	require.NoError(t, err)

	// Now node_tx = 2000, refund_tx = 2000
	// For renewRefundTimelock: need refund <= 300, node > 300
	modifyNodeTimelockAllOperators(t, config, renewedLeaf2.Id, 2000, timelockBelowRenewThreshold)

	queriedLeaf2 := queryLeafByID(t, config, authToken, renewedLeaf2.Id)
	require.NotNil(t, queriedLeaf2.ParentNodeId)
	parentLeaf2 := queryLeafByID(t, config, authToken, *queriedLeaf2.ParentNodeId)

	renewedLeaf3, err := wallet.RenewRefundTimelock(ctx, config, queriedLeaf2, parentLeaf2, leafPrivKey)
	require.NoError(t, err)
	require.NotNil(t, renewedLeaf3)

	renewedNodeTimelock := getTimelockFromTxBytes(t, renewedLeaf3.NodeTx)
	renewedRefundTimelock := getTimelockFromTxBytes(t, renewedLeaf3.RefundTx)
	require.Equal(t, uint32(1900), renewedNodeTimelock, "renewed node_tx should have timelock 1900 (decremented from 2000)")
	require.Equal(t, uint32(2000), renewedRefundTimelock, "renewed refund_tx should have timelock 2000 (reset)")
	require.Equal(t, "AVAILABLE", renewedLeaf3.Status)

	queriedRenewedLeaf := queryLeafByID(t, config, authToken, renewedLeaf3.Id)
	require.Equal(t, "AVAILABLE", queriedRenewedLeaf.Status)
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
			if node, ok := resp.Nodes[leafID]; ok {
				statuses[identifier] = node.Status
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
	require.Equal(t, "AVAILABLE", rootNode.Status)

	authToken, err := wallet.AuthenticateWithServer(t.Context(), config)
	require.NoError(t, err)

	// Verify all SOs show Available
	statuses := queryLeafStatusOnAllOperators(t, config, authToken, rootNode.Id)
	for id, status := range statuses {
		assert.Equal(t, "AVAILABLE", status, "SO %s should be Available before renew", id)
	}

	// Mock: reduce refund_tx timelock to trigger renewal
	modifyNodeTimelockAllOperators(t, config, rootNode.Id, 0, timelockBelowRenewThreshold)
	leaf := queryLeafByID(t, config, authToken, rootNode.Id)

	// Attempt a renew with a corrupt signing job to force a failure after prepare.
	// Send an invalid RenewNodeZeroTimelockSigningJob with empty transactions.
	ctx := wallet.ContextWithToken(t.Context(), authToken)
	conn, err := config.NewCoordinatorGRPCConnection()
	require.NoError(t, err)
	defer conn.Close()
	sparkClient := pb.NewSparkServiceClient(conn)

	_, renewErr := sparkClient.RenewLeaf(ctx, &pb.RenewLeafRequest{
		LeafId: leaf.Id,
		SigningJobs: &pb.RenewLeafRequest_RenewNodeZeroTimelockSigningJob{
			RenewNodeZeroTimelockSigningJob: &pb.RenewNodeZeroTimelockSigningJob{
				// Empty/nil signing jobs — should fail during validation
			},
		},
	})
	require.Error(t, renewErr, "renew with invalid signing data should fail")

	// Verify all SOs have the leaf back to Available (rollback worked)
	statuses = queryLeafStatusOnAllOperators(t, config, authToken, rootNode.Id)
	for id, status := range statuses {
		assert.Equal(t, "AVAILABLE", status, "SO %s should be Available after failed renew (rollback)", id)
	}
}
