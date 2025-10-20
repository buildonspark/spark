package grpctest

import (
	"testing"

	"github.com/lightsparkdev/spark/common/keys"
	sparktesting "github.com/lightsparkdev/spark/testing"

	"github.com/lightsparkdev/spark"
	"github.com/lightsparkdev/spark/common"
	pb "github.com/lightsparkdev/spark/proto/spark"
	"github.com/lightsparkdev/spark/testing/wallet"
	"github.com/stretchr/testify/require"
)

// Test we can unilateral exit a leaf node after depositing funds into
// a single leaf tree.
func TestUnilateralExitSingleLeaf(t *testing.T) {
	sparktesting.SkipIfGithubActions(t)
	config := wallet.NewTestWalletConfig(t)
	leafPrivKey := keys.GeneratePrivateKey()
	rootNode, err := wallet.CreateNewTree(config, faucet, leafPrivKey, 100_000)
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
