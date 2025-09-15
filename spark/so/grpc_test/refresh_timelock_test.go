package grpctest

import (
	"fmt"
	"testing"

	"github.com/lightsparkdev/spark"
	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/common/keys"
	pb "github.com/lightsparkdev/spark/proto/spark"
	sparktesting "github.com/lightsparkdev/spark/testing"
	"github.com/lightsparkdev/spark/testing/wallet"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestRefreshTimelock(t *testing.T) {
	senderConfig := sparktesting.TestWalletConfig(t)
	senderLeafPrivKey, err := keys.GeneratePrivateKey()
	require.NoError(t, err)
	tree, nodes, err := sparktesting.CreateNewTreeWithLevels(senderConfig, faucet, senderLeafPrivKey, 100_000, 1)
	require.NoError(t, err)
	fmt.Println("node count:", len(nodes))
	require.NotEmpty(t, nodes, "no nodes created when creating tree")
	node := nodes[len(nodes)-1]

	signingKey := tree.Children[1].SigningPrivateKey

	// Decrement timelock on refundTx
	_, err = wallet.RefreshTimelockRefundTx(t.Context(), senderConfig, node, signingKey)
	require.NoError(t, err)

	parentNode := nodes[len(nodes)-3]
	assert.Equal(t, parentNode.Id, *node.ParentNodeId)

	// Reset timelock on refundTx, decrement timelock on leafNodeTx
	_, err = wallet.RefreshTimelockNodes(t.Context(), senderConfig, []*pb.TreeNode{node}, parentNode, signingKey)
	require.NoError(t, err)

	// TODO: test that we can refresh the timelock for >1 parents
	// (requires extension RPC)
}

func TestExtendLeaf(t *testing.T) {
	senderConfig := sparktesting.TestWalletConfig(t)
	senderLeafPrivKey, err := keys.GeneratePrivateKey()
	require.NoError(t, err)
	tree, nodes, err := sparktesting.CreateNewTreeWithLevels(senderConfig, faucet, senderLeafPrivKey, 100_000, 1)
	require.NoError(t, err)
	require.NotEmpty(t, nodes, "no nodes created when creating tree")
	node := nodes[len(nodes)-1]

	signingKey := tree.Children[1].SigningPrivateKey
	err = wallet.ExtendTimelock(t.Context(), senderConfig, node, signingKey)
	require.NoError(t, err)

	// TODO: test that we can refresh where first node has no timelock
	// TODO: test that we cannot modify a node after it's reached
	// 0 timelock
}

// skipping these tests - timelock decrement is not practical in CI
// TODO: move to a unit test

func TestRenewLeafExtend(t *testing.T) {
	skipIfGithubActions(t)
	senderConfig := sparktesting.TestWalletConfig(t)
	senderLeafPrivKey, err := keys.GeneratePrivateKey()
	require.NoError(t, err)
	tree, nodes, err := sparktesting.CreateNewTreeWithLevels(senderConfig, faucet, senderLeafPrivKey, 100_000, 1)
	require.NoError(t, err)
	require.NotEmpty(t, nodes, "no nodes created when creating tree")
	node := nodes[len(nodes)-1]
	parentNode := nodes[len(nodes)-3]
	assert.Equal(t, parentNode.Id, *node.ParentNodeId)

	signingKey := tree.Children[1].SigningPrivateKey
	// Decrement timelock of node TX to 300
	for range 17 {
		// Reset timelock on refundTx, decrement timelock on leafNodeTx
		refreshedNodes, err := wallet.RefreshTimelockNodes(t.Context(), senderConfig, []*pb.TreeNode{node}, parentNode, signingKey)
		require.NoError(t, err)
		require.Len(t, refreshedNodes, 1)
		node = refreshedNodes[0]
	}
	// Decrement timelock of refund TX to 300
	for range 17 {
		// Decrement timelock on refundTx
		node, err = wallet.RefreshTimelockRefundTx(t.Context(), senderConfig, node, signingKey)
		require.NoError(t, err)
	}

	resp, err := wallet.ExtendTimelockUsingRenew(t.Context(), senderConfig, node, parentNode, signingKey)
	require.NoError(t, err)

	result := resp.GetRenewNodeTimelockResult()
	require.NotNil(t, result)
	splitLeaf := result.SplitNode
	require.NotNil(t, splitLeaf)
	extendedLeaf := result.Node
	require.NotNil(t, extendedLeaf)
	assert.Equal(t, "SPLIT_LOCKED", splitLeaf.Status)
	assert.Equal(t, "AVAILABLE", extendedLeaf.Status)
	assert.Empty(t, splitLeaf.RefundTx)

	// Extract and deserialize transactions from the response
	decrementedNodeTx, err := common.TxFromRawTxBytes(splitLeaf.NodeTx)
	require.NoError(t, err, "failed to deserialize decremented node tx")

	extendedNodeTx, err := common.TxFromRawTxBytes(extendedLeaf.NodeTx)
	require.NoError(t, err, "failed to deserialize extended node tx")

	refundTx, err := common.TxFromRawTxBytes(extendedLeaf.RefundTx)
	require.NoError(t, err, "failed to deserialize refund tx")

	// Check sequences
	assert.Equal(t, spark.ZeroSequence, decrementedNodeTx.TxIn[0].Sequence)
	assert.Equal(t, spark.InitialSequence(), extendedNodeTx.TxIn[0].Sequence)
	assert.Equal(t, spark.InitialSequence(), refundTx.TxIn[0].Sequence)

	// Check signatures
	assert.True(t, decrementedNodeTx.HasWitness())
	assert.True(t, refundTx.HasWitness())
	assert.True(t, extendedNodeTx.HasWitness())
}

func TestRenewLeafRefresh(t *testing.T) {
	skipIfGithubActions(t)
	senderConfig := sparktesting.TestWalletConfig(t)
	senderLeafPrivKey, err := keys.GeneratePrivateKey()
	require.NoError(t, err)
	tree, nodes, err := sparktesting.CreateNewTreeWithLevels(senderConfig, faucet, senderLeafPrivKey, 100_000, 1)
	require.NoError(t, err)
	require.NotEmpty(t, nodes, "no nodes created when creating tree")
	node := nodes[len(nodes)-1]
	parentNode := nodes[len(nodes)-3]
	assert.Equal(t, parentNode.Id, *node.ParentNodeId)

	signingKey := tree.Children[1].SigningPrivateKey
	// Decrement timelock of refund TX to 300
	for range 17 {
		// Decrement timelock on refundTx
		node, err = wallet.RefreshTimelockRefundTx(t.Context(), senderConfig, node, signingKey)
		require.NoError(t, err)
	}

	resp, err := wallet.RefreshTimelockUsingRenew(t.Context(), senderConfig, node, parentNode, signingKey)
	require.NoError(t, err)

	result := resp.GetRenewRefundTimelockResult()
	require.NotNil(t, result)
	refreshedNode := result.Node
	require.NotNil(t, refreshedNode)
	assert.Equal(t, "AVAILABLE", refreshedNode.Status)

	refundTx, err := common.TxFromRawTxBytes(refreshedNode.RefundTx)
	require.NoError(t, err, "failed to deserialize refund tx")
	// Refund tx should have refreshed sequence
	assert.Equal(t, spark.InitialSequence(), refundTx.TxIn[0].Sequence)

	nodeTx, err := common.TxFromRawTxBytes(refreshedNode.NodeTx)
	require.NoError(t, err, "failed to deserialize refreshed node tx")
	oldNodeTx, err := common.TxFromRawTxBytes(node.NodeTx)
	require.NoError(t, err, "failed to deserialize old node tx")

	// Node tx should have decremented sequence
	assert.Less(t, nodeTx.TxIn[0].Sequence, oldNodeTx.TxIn[0].Sequence)

	// Check signatures (witnesses)
	assert.True(t, nodeTx.HasWitness())
	assert.True(t, refundTx.HasWitness())
}

func TestRenewLeafExtend_InvalidRefundTimelock(t *testing.T) {
	skipIfGithubActions(t)
	senderConfig := sparktesting.TestWalletConfig(t)
	senderLeafPrivKey, err := keys.GeneratePrivateKey()
	require.NoError(t, err)
	tree, nodes, err := sparktesting.CreateNewTreeWithLevels(senderConfig, faucet, senderLeafPrivKey, 100_000, 1)
	require.NoError(t, err)
	require.NotEmpty(t, nodes, "no nodes created when creating tree")
	node := nodes[len(nodes)-1]
	parentNode := nodes[len(nodes)-3]
	assert.Equal(t, parentNode.Id, *node.ParentNodeId)

	signingKey := tree.Children[1].SigningPrivateKey
	// Decrement timelock of node TX to 300
	for range 17 {
		// Reset timelock on refundTx, decrement timelock on leafNodeTx
		refreshedNodes, err := wallet.RefreshTimelockNodes(t.Context(), senderConfig, []*pb.TreeNode{node}, parentNode, signingKey)
		require.NoError(t, err)
		require.Len(t, refreshedNodes, 1)
		node = refreshedNodes[0]
	}

	_, err = wallet.ExtendTimelockUsingRenew(t.Context(), senderConfig, node, parentNode, signingKey)
	require.Error(t, err)
	stat, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.FailedPrecondition, stat.Code())
	assert.Contains(t, err.Error(), "refund transaction sequence must be less than or equal to 300")
}

func TestRenewLeafExtend_InvalidNodeTimelock(t *testing.T) {
	senderConfig := sparktesting.TestWalletConfig(t)
	senderLeafPrivKey, err := keys.GeneratePrivateKey()
	require.NoError(t, err)
	tree, nodes, err := sparktesting.CreateNewTreeWithLevels(senderConfig, faucet, senderLeafPrivKey, 100_000, 1)
	require.NoError(t, err)
	require.NotEmpty(t, nodes, "no nodes created when creating tree")
	node := nodes[len(nodes)-1]
	parentNode := nodes[len(nodes)-3]
	assert.Equal(t, parentNode.Id, *node.ParentNodeId)

	signingKey := tree.Children[1].SigningPrivateKey
	_, err = wallet.ExtendTimelockUsingRenew(t.Context(), senderConfig, node, parentNode, signingKey)
	require.Error(t, err)
	stat, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.FailedPrecondition, stat.Code())
	assert.Contains(t, err.Error(), "node transaction sequence must be less than or equal to 300")
}

func TestRenewLeafRefresh_InvalidRefundTimelock(t *testing.T) {
	senderConfig := sparktesting.TestWalletConfig(t)
	senderLeafPrivKey, err := keys.GeneratePrivateKey()
	require.NoError(t, err)
	tree, nodes, err := sparktesting.CreateNewTreeWithLevels(senderConfig, faucet, senderLeafPrivKey, 100_000, 1)
	require.NoError(t, err)
	require.NotEmpty(t, nodes, "no nodes created when creating tree")
	node := nodes[len(nodes)-1]
	parentNode := nodes[len(nodes)-3]
	assert.Equal(t, parentNode.Id, *node.ParentNodeId)

	signingKey := tree.Children[1].SigningPrivateKey

	_, err = wallet.RefreshTimelockUsingRenew(t.Context(), senderConfig, node, parentNode, signingKey)
	require.Error(t, err)
	stat, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.FailedPrecondition, stat.Code())
	assert.Contains(t, err.Error(), "refund transaction sequence must be less than or equal to 300")
}
