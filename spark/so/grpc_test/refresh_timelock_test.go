package grpctest

import (
	"context"
	"fmt"
	"testing"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	pb "github.com/lightsparkdev/spark/proto/spark"
	testutil "github.com/lightsparkdev/spark/test_util"
	"github.com/lightsparkdev/spark/wallet"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRefreshTimelock(t *testing.T) {
	senderConfig, err := testutil.TestWalletConfig()
	assert.NoError(t, err)
	senderLeafPrivKey, err := secp256k1.GeneratePrivateKey()
	assert.NoError(t, err)
	tree, nodes, err := testutil.CreateNewTreeWithLevels(senderConfig, faucet, senderLeafPrivKey, 100_000, 1)
	assert.NoError(t, err)
	fmt.Println("node count:", len(nodes))
	require.NotEqual(t, 0, len(nodes), "no nodes created when creating tree")
	node := nodes[len(nodes)-1]

	signingKeyBytes := tree.Children[1].SigningPrivateKey
	signingKey := secp256k1.PrivKeyFromBytes(signingKeyBytes)

	// Decrement timelock on refundTx
	_, err = wallet.RefreshTimelockRefundTx(
		context.Background(),
		senderConfig,
		node,
		signingKey,
	)
	assert.NoError(t, err)

	parentNode := nodes[len(nodes)-3]
	assert.Equal(t, parentNode.Id, *node.ParentNodeId)

	// Reset timelock on refundTx, decrement timelock on leafNodeTx
	_, err = wallet.RefreshTimelockNodes(
		context.Background(),
		senderConfig,
		[]*pb.TreeNode{node},
		parentNode,
		signingKey,
	)
	assert.NoError(t, err)

	// TODO: test that we can refresh the timelock for >1 parents
	// (requires extension RPC)
}

func TestExtendLeaf(t *testing.T) {
	senderConfig, err := testutil.TestWalletConfig()
	assert.NoError(t, err)
	senderLeafPrivKey, err := secp256k1.GeneratePrivateKey()
	assert.NoError(t, err)
	tree, nodes, err := testutil.CreateNewTreeWithLevels(senderConfig, faucet, senderLeafPrivKey, 100_000, 1)
	assert.NoError(t, err)
	fmt.Println("node count:", len(nodes))
	require.NotEqual(t, 0, len(nodes), "no nodes created when creating tree")
	node := nodes[len(nodes)-1]

	signingKeyBytes := tree.Children[1].SigningPrivateKey
	signingKey := secp256k1.PrivKeyFromBytes(signingKeyBytes)

	err = wallet.ExtendTimelock(
		context.Background(),
		senderConfig,
		node,
		signingKey,
	)
	assert.NoError(t, err)

	// TODO: test that we can refresh where first node has no timelock
	// TODO: test that we cannot modify a node after it's reached
	// 0 timelock
}
