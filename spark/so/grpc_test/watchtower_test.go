package grpctest

import (
	"context"
	"testing"
	"time"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/lightsparkdev/spark"
	"github.com/lightsparkdev/spark/common"
	pb "github.com/lightsparkdev/spark/proto/spark"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/ent/treenode"
	"github.com/lightsparkdev/spark/so/watchtower"
	testutil "github.com/lightsparkdev/spark/test_util"
	"github.com/lightsparkdev/spark/wallet"
	"github.com/stretchr/testify/require"
)

func TestTimelockExpirationHappyPath(t *testing.T) {
	walletConfig, err := testutil.TestWalletConfig()
	require.NoError(t, err)

	config, err := testutil.TestConfig()
	require.NoError(t, err)

	client, err := testutil.NewRegtestClient()
	require.NoError(t, err)

	faucet := testutil.GetFaucetInstance(client)
	err = faucet.Refill()
	require.NoError(t, err)

	leafPrivKey, err := secp256k1.GeneratePrivateKey()
	require.NoError(t, err)
	rootNode, err := testutil.CreateNewTree(walletConfig, faucet, leafPrivKey, 100_000)
	require.NoError(t, err)

	// Reduce timelock
	getCurrentTimelock := func(rootNode *pb.TreeNode) int64 {
		refundTx, err := common.TxFromRawTxBytes(rootNode.GetRefundTx())
		require.NoError(t, err)
		return int64(refundTx.TxIn[0].Sequence & 0xFFFF)
	}

	for getCurrentTimelock(rootNode) > spark.TimeLockInterval*2 {
		rootNode, err = wallet.RefreshTimelockRefundTx(context.Background(), walletConfig, rootNode, leafPrivKey)
		require.NoError(t, err)
	}
	require.LessOrEqual(t, getCurrentTimelock(rootNode), int64(spark.TimeLockInterval*2))

	ctx, dbClient, err := testutil.TestContext(config)
	require.NoError(t, err)

	// Broadcast the node transaction
	nodeTx, err := common.TxFromRawTxBytes(rootNode.GetNodeTx())
	require.NoError(t, err)
	nodeTxBytes, err := serializeTx(nodeTx)
	require.NoError(t, err)

	// Generate a block to start
	randomAddress, err := common.P2TRRawAddressFromPublicKey(leafPrivKey.PubKey().SerializeCompressed(), common.Regtest)
	require.NoError(t, err)
	_, err = client.GenerateToAddress(1, randomAddress, nil)
	require.NoError(t, err)

	// Broadcast node tx
	_, err = client.SendRawTransaction(nodeTx, false)
	require.NoError(t, err)

	// Generate a block to confirm the node transaction
	blockHashes, err := client.GenerateToAddress(1, randomAddress, nil)
	require.NoError(t, err)

	// Verify node tx and fee bump are confirmed
	block, err := client.GetBlockVerbose(blockHashes[0])
	require.NoError(t, err)
	require.Contains(t, block.Tx, nodeTx.TxID())

	// Get the node from the database and verify initial state
	node, err := dbClient.TreeNode.Query().
		Where(treenode.RawTx(nodeTxBytes)).
		Only(ctx)
	require.NoError(t, err)

	_, err = client.GenerateToAddress(1, randomAddress, nil)
	require.NoError(t, err)

	// Wait for node confirmation with retry logic
	var broadcastedNode *ent.TreeNode
	for range 10 {
		time.Sleep(500 * time.Millisecond)
		broadcastedNode, err = dbClient.TreeNode.Get(ctx, node.ID)
		require.NoError(t, err)
		if broadcastedNode.NodeConfirmationHeight > 0 {
			break
		}
	}
	require.Greater(t, broadcastedNode.NodeConfirmationHeight, uint64(0), "Node confirmation height should be set to a positive block height")
	require.Equal(t, uint64(0), broadcastedNode.RefundConfirmationHeight, "Refund confirmation height should not be set yet")
	require.NotEmpty(t, broadcastedNode.RawRefundTx, "RawRefundTx should exist in the database")

	// Generate blocks until timelock expires
	timelock := getCurrentTimelock(rootNode)
	_, err = client.GenerateToAddress(timelock, randomAddress, nil)
	require.NoError(t, err)

	// Mine to confirm transaction broadcasts correctly.
	_, err = client.GenerateToAddress(1, randomAddress, nil)
	require.NoError(t, err)

	// Get curr block height
	currentHeight, err := client.GetBlockCount()
	require.NoError(t, err)

	// Calculate expected minimum height (node confirmation + timelock)
	expectedMinHeight := int64(broadcastedNode.NodeConfirmationHeight) + getCurrentTimelock(rootNode)
	require.Greater(t, currentHeight, expectedMinHeight, "Current block height should be greater than node confirmation height + timelock")

	tx, err := common.TxFromRawTxBytes(node.RawRefundTx)
	require.NoError(t, err)

	err = watchtower.CheckExpiredTimeLocks(ctx, client, broadcastedNode, currentHeight)
	require.NoError(t, err)

	// Verify refund tx is confirmed
	blockHashes, err = client.GenerateToAddress(1, randomAddress, nil)
	require.NoError(t, err)
	block, err = client.GetBlockVerbose(blockHashes[0])
	require.NoError(t, err)
	require.Contains(t, block.Tx, tx.TxID(), "Refund transaction should be in the block (TxHash)")

	// Wait for refund confirmation with retry logic while continuously generating new blocks
	var finalNode *ent.TreeNode
	for range 10 {
		_, err = client.GenerateToAddress(1, randomAddress, nil)
		require.NoError(t, err)
		time.Sleep(500 * time.Millisecond)
		finalNode, err = dbClient.TreeNode.Get(ctx, node.ID)
		require.NoError(t, err)
		if finalNode.RefundConfirmationHeight > 0 {
			break
		}
	}

	require.Greater(t, finalNode.NodeConfirmationHeight, uint64(0), "Node confirmation height should be set to a positive block height")
	require.Greater(t, finalNode.RefundConfirmationHeight, uint64(0), "Refund confirmation height should be set to a positive block height")
}
