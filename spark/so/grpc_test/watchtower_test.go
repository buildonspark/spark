package grpctest

import (
	"context"
	"encoding/hex"
	"testing"
	"time"

	"github.com/btcsuite/btcd/wire"
	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/lightsparkdev/spark"
	"github.com/lightsparkdev/spark/common"
	pb "github.com/lightsparkdev/spark/proto/spark"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/ent/treenode"
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

	// Get funding for the node transaction
	coin, err := faucet.Fund()
	require.NoError(t, err)

	// Create and sign fee bump transaction for node tx
	nodeTxHash := nodeTx.TxHash()
	anchorOutPoint := wire.NewOutPoint(&nodeTxHash, 1)
	outputScript, err := common.P2TRScriptFromPubKey(leafPrivKey.PubKey())
	require.NoError(t, err)
	nodeFeeBumpTx, err := testutil.SignFaucetCoinFeeBump(anchorOutPoint, coin, outputScript)
	require.NoError(t, err)

	// Serialize transactions
	nodeTxBytes, err := serializeTx(nodeTx)
	require.NoError(t, err)
	nodeFeeBumpTxBytes, err := serializeTx(nodeFeeBumpTx)
	require.NoError(t, err)

	// Generate a block to start
	randomAddress, err := common.P2TRRawAddressFromPublicKey(leafPrivKey.PubKey().SerializeCompressed(), common.Regtest)
	require.NoError(t, err)
	_, err = client.GenerateToAddress(1, randomAddress, nil)
	require.NoError(t, err)

	// Submit node tx package
	err = submitPackage(client, []string{hex.EncodeToString(nodeTxBytes), hex.EncodeToString(nodeFeeBumpTxBytes)})
	require.NoError(t, err)

	// Generate a block to confirm the node transaction
	blockHashes, err := client.GenerateToAddress(1, randomAddress, nil)
	require.NoError(t, err)

	// Verify node tx and fee bump are confirmed
	block, err := client.GetBlockVerbose(blockHashes[0])
	require.NoError(t, err)
	require.Contains(t, block.Tx, nodeTx.TxID())
	require.Contains(t, block.Tx, nodeFeeBumpTx.TxID())

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

	// // Get the refund transaction and create a fee bump for it
	refundTx, err := common.TxFromRawTxBytes(rootNode.GetRefundTx())
	require.NoError(t, err)

	// // Get funding for the refund transaction
	coin, err = faucet.Fund()
	require.NoError(t, err)

	// // Create and sign fee bump transaction for refund tx
	refundTxHash := refundTx.TxHash()
	refundAnchorOutPoint := wire.NewOutPoint(&refundTxHash, 1)
	refundFeeBumpTx, err := testutil.SignFaucetCoinFeeBump(refundAnchorOutPoint, coin, outputScript)
	require.NoError(t, err)

	// // Serialize transactions
	refundTxBytes, err := serializeTx(refundTx)
	require.NoError(t, err)
	refundFeeBumpTxBytes, err := serializeTx(refundFeeBumpTx)
	require.NoError(t, err)

	// // Submit refund tx package
	err = submitPackage(client, []string{hex.EncodeToString(refundTxBytes), hex.EncodeToString(refundFeeBumpTxBytes)})
	require.NoError(t, err)

	// Mine to confirm transaction broadcasts correctly.
	blockHashes, err = client.GenerateToAddress(1, randomAddress, nil)
	require.NoError(t, err)

	// Verify refund tx is confirmed
	block, err = client.GetBlockVerbose(blockHashes[0])
	require.NoError(t, err)
	require.NoError(t, err)
	require.Contains(t, block.Tx, refundTx.TxID(), "Refund transaction should be in the block (TxHash)")
	require.Contains(t, block.Tx, refundFeeBumpTx.TxID(), "Refund fee bump should be in the block")

	// Get current block height
	currentHeight, err := client.GetBlockCount()
	require.NoError(t, err)

	// Calculate expected minimum height (node confirmation + timelock)
	expectedMinHeight := int64(broadcastedNode.NodeConfirmationHeight) + getCurrentTimelock(rootNode)
	require.Greater(t, currentHeight, expectedMinHeight, "Current block height should be greater than node confirmation height + timelock")

	// Wait for refund confirmation with retry logic
	var finalNode *ent.TreeNode
	for range 50 {
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
