package grpctest

import (
	"fmt"
	"testing"

	"github.com/lightsparkdev/spark/common/keys"
	pb "github.com/lightsparkdev/spark/proto/spark"
	sparktesting "github.com/lightsparkdev/spark/testing"
	"github.com/lightsparkdev/spark/testing/wallet"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
