package grpctest

import (
	"testing"

	"github.com/lightsparkdev/spark/common/keys"

	"github.com/lightsparkdev/spark"
	"github.com/lightsparkdev/spark/common"
	pb "github.com/lightsparkdev/spark/proto/spark"
	sparktesting "github.com/lightsparkdev/spark/testing"
	"github.com/lightsparkdev/spark/testing/wallet"
	"github.com/stretchr/testify/require"
)

// Test we can unilateral exit a leaf node after depositing funds into
// a single leaf tree.
func TestUnilateralExitSingleLeaf(t *testing.T) {
	skipIfGithubActions(t)
	config := sparktesting.TestWalletConfig(t)
	leafPrivKey, err := keys.GeneratePrivateKey()
	require.NoError(t, err)
	rootNode, err := sparktesting.CreateNewTree(config, faucet, leafPrivKey, 100_000)
	require.NoError(t, err)

	getCurrentTimelock := func(rootNode *pb.TreeNode) int64 {
		refundTx, err := common.TxFromRawTxBytes(rootNode.GetRefundTx())
		require.NoError(t, err)
		return int64(refundTx.TxIn[0].Sequence & 0xFFFF)
	}

	// Re-sign the leaf with decrement timelock so we don't need to mine so many blocks
	for getCurrentTimelock(rootNode) > spark.TimeLockInterval*2 {
		rootNode, err = wallet.RefreshTimelockRefundTx(t.Context(), config, rootNode, leafPrivKey)
		require.NoError(t, err)
	}

	nodeTx, err := common.TxFromRawTxBytes(rootNode.GetNodeTx())
	require.NoError(t, err)
	err = faucet.FeeBumpAndConfirmTx(nodeTx)
	require.NoError(t, err)

	refundTx, err := common.TxFromRawTxBytes(rootNode.GetRefundTx())
	require.NoError(t, err)
	err = faucet.FeeBumpAndConfirmTx(refundTx)
	require.NoError(t, err)
}

// Test we can unilateral exit a leaf node of a tree with multiple leaves.
func TestUnilateralExitTreeLeaf(t *testing.T) {
	skipIfGithubActions(t)
	config := sparktesting.TestWalletConfig(t)
	leafPrivKey, err := keys.GeneratePrivateKey()
	require.NoError(t, err)
	tree, nodes, err := sparktesting.CreateNewTreeWithLevels(config, faucet, leafPrivKey, 100_000, 1)
	require.NoError(t, err)
	require.Len(t, nodes, 5)

	// These indices are hard-coded based on how we do tree construction
	rootNode := nodes[0]
	leafNode := nodes[len(nodes)-1]
	signingKey := tree.Children[1].SigningPrivateKey
	parentNode := nodes[len(nodes)-3]
	require.Equal(t, parentNode.Id, *leafNode.ParentNodeId)

	// Decrement our timelocks so we don't need to mine so many blocks
	getTimelock := func(txBytes []byte) int64 {
		tx, err := common.TxFromRawTxBytes(txBytes)
		require.NoError(t, err)
		return int64(tx.TxIn[0].Sequence & 0xFFFF)
	}

	for getTimelock(leafNode.NodeTx) > spark.TimeLockInterval*2 {
		nodes, err = wallet.RefreshTimelockNodes(t.Context(), config, []*pb.TreeNode{leafNode}, parentNode, signingKey)
		leafNode = nodes[0]
		require.NoError(t, err)
	}

	for getTimelock(leafNode.RefundTx) > spark.TimeLockInterval*2 {
		leafNode, err = wallet.RefreshTimelockRefundTx(t.Context(), config, leafNode, signingKey)
		require.NoError(t, err)
	}

	rootNodeTx, err := common.TxFromRawTxBytes(rootNode.GetNodeTx())
	require.NoError(t, err)
	err = faucet.FeeBumpAndConfirmTx(rootNodeTx)
	require.NoError(t, err)

	parentNodeTx, err := common.TxFromRawTxBytes(parentNode.GetNodeTx())
	require.NoError(t, err)
	err = faucet.FeeBumpAndConfirmTx(parentNodeTx)
	require.NoError(t, err)

	nodeTx, err := common.TxFromRawTxBytes(leafNode.GetNodeTx())
	require.NoError(t, err)
	err = faucet.FeeBumpAndConfirmTx(nodeTx)
	require.NoError(t, err)

	refundTx, err := common.TxFromRawTxBytes(leafNode.GetRefundTx())
	require.NoError(t, err)
	err = faucet.FeeBumpAndConfirmTx(refundTx)
	require.NoError(t, err)
}
