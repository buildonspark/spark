package grpctest

import (
	"testing"

	"github.com/lightsparkdev/spark"
	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/common/keys"
	pbmock "github.com/lightsparkdev/spark/proto/mock"
	pb "github.com/lightsparkdev/spark/proto/spark"
	"github.com/lightsparkdev/spark/testing/wallet"
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
