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
	timelock := getCurrentTimelock(rootNode) + spark.WatchtowerTimeLockBuffer
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

	err = watchtower.CheckExpiredTimeLocks(ctx, client, broadcastedNode, currentHeight, common.Regtest)
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

func TestTimelockExpirationTransferredNode(t *testing.T) {
	skipIfGithubActions(t)

	walletConfig, err := testutil.TestWalletConfig()
	require.NoError(t, err)

	config, err := testutil.TestConfig()
	require.NoError(t, err)

	client, err := testutil.NewRegtestClient()
	require.NoError(t, err)

	faucet := testutil.GetFaucetInstance(client)
	err = faucet.Refill()
	require.NoError(t, err)

	// Create sender wallet and tree
	senderLeafPrivKey, err := secp256k1.GeneratePrivateKey()
	require.NoError(t, err)
	senderRootNode, err := testutil.CreateNewTree(walletConfig, faucet, senderLeafPrivKey, 100_000)
	require.NoError(t, err)

	// Create receiver wallet
	receiverPrivKey, err := secp256k1.GeneratePrivateKey()
	require.NoError(t, err)
	receiverConfig, err := testutil.TestWalletConfigWithIdentityKey(*receiverPrivKey)
	require.NoError(t, err)

	// Prepare transfer - sender creates new signing key for the transfer
	newLeafPrivKey, err := secp256k1.GeneratePrivateKey()
	require.NoError(t, err)

	transferNode := wallet.LeafKeyTweak{
		Leaf:              senderRootNode,
		SigningPrivKey:    senderLeafPrivKey.Serialize(),
		NewSigningPrivKey: newLeafPrivKey.Serialize(),
	}
	leavesToTransfer := []wallet.LeafKeyTweak{transferNode}

	// Sender initiates transfer
	senderTransfer, err := wallet.SendTransfer(
		context.Background(),
		walletConfig,
		leavesToTransfer,
		receiverPrivKey.PubKey().SerializeCompressed(),
		time.Now().Add(10*time.Minute),
	)
	require.NoError(t, err, "failed to transfer tree node")

	// Receiver queries and claims the pending transfer
	receiverToken, err := wallet.AuthenticateWithServer(context.Background(), receiverConfig)
	require.NoError(t, err, "failed to authenticate receiver")
	receiverCtx := wallet.ContextWithToken(context.Background(), receiverToken)
	pendingTransfer, err := wallet.QueryPendingTransfers(receiverCtx, receiverConfig)
	require.NoError(t, err, "failed to query pending transfers")
	require.Equal(t, 1, len(pendingTransfer.Transfers))
	receiverTransfer := pendingTransfer.Transfers[0]
	require.Equal(t, senderTransfer.Id, receiverTransfer.Id)

	// Verify the pending transfer
	leafPrivKeyMap, err := wallet.VerifyPendingTransfer(context.Background(), receiverConfig, receiverTransfer)
	require.NoError(t, err, "failed to verify pending transfer")
	require.Equal(t, 1, len(*leafPrivKeyMap))
	require.Equal(t, newLeafPrivKey.Serialize(), (*leafPrivKeyMap)[senderRootNode.Id])

	// Receiver claims the transfer with a final signing key
	finalLeafPrivKey, err := secp256k1.GeneratePrivateKey()
	require.NoError(t, err, "failed to create final node signing private key")
	claimingNode := wallet.LeafKeyTweak{
		Leaf:              receiverTransfer.Leaves[0].Leaf,
		SigningPrivKey:    newLeafPrivKey.Serialize(),
		NewSigningPrivKey: finalLeafPrivKey.Serialize(),
	}
	leavesToClaim := []wallet.LeafKeyTweak{claimingNode}
	claimedNodes, err := wallet.ClaimTransfer(
		receiverCtx,
		receiverTransfer,
		receiverConfig,
		leavesToClaim,
	)
	require.NoError(t, err, "failed to claim transfer")
	require.Equal(t, 1, len(claimedNodes))
	transferredNode := claimedNodes[0]

	// Reduce timelock on the transferred node's node transaction (not refund yet)
	getCurrentTimelock := func(txBytes []byte) int64 {
		tx, err := common.TxFromRawTxBytes(txBytes)
		require.NoError(t, err)
		return int64(tx.TxIn[0].Sequence & 0xFFFF)
	}

	ctx, dbClient, err := testutil.TestContext(config)
	require.NoError(t, err)

	// Serialize the node transaction for database queries
	nodeTx, err := common.TxFromRawTxBytes(transferredNode.GetNodeTx())
	require.NoError(t, err)
	nodeTxBytes, err := serializeTx(nodeTx)
	require.NoError(t, err)

	// Generate a block to start
	randomAddress, err := common.P2TRRawAddressFromPublicKey(finalLeafPrivKey.PubKey().SerializeCompressed(), common.Regtest)
	require.NoError(t, err)
	_, err = client.GenerateToAddress(1, randomAddress, nil)
	require.NoError(t, err)

	// Broadcast transferred node tx
	_, err = client.SendRawTransaction(nodeTx, false)
	require.NoError(t, err)

	// Generate a block to confirm the node transaction
	blockHashes, err := client.GenerateToAddress(1, randomAddress, nil)
	require.NoError(t, err)

	// Verify node tx is confirmed
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

	// Now reduce the timelock on the refund transaction
	for getCurrentTimelock(transferredNode.RefundTx) > spark.TimeLockInterval*2 {
		transferredNode, err = wallet.RefreshTimelockRefundTx(context.Background(), receiverConfig, transferredNode, finalLeafPrivKey)
		require.NoError(t, err)
	}
	require.LessOrEqual(t, getCurrentTimelock(transferredNode.RefundTx), int64(spark.TimeLockInterval*2))

	// Generate blocks until refund transaction timelock expires
	refundTimelock := getCurrentTimelock(transferredNode.RefundTx) + spark.WatchtowerTimeLockBuffer
	_, err = client.GenerateToAddress(refundTimelock, randomAddress, nil)
	require.NoError(t, err)

	// Get current block height
	currentHeight, err := client.GetBlockCount()
	require.NoError(t, err)

	// Calculate expected minimum height (node confirmation + timelock)
	broadcastedNode, err = dbClient.TreeNode.Get(ctx, node.ID)
	expectedMinHeight := int64(broadcastedNode.NodeConfirmationHeight) + getCurrentTimelock(broadcastedNode.RawRefundTx)
	require.Greater(t, currentHeight, expectedMinHeight, "Current block height should be greater than node confirmation height + timelock")
	require.NoError(t, err)

	refundTx, err := common.TxFromRawTxBytes(broadcastedNode.RawRefundTx)
	require.NoError(t, err)

	// Call watchtower to check expired timelocks - this should broadcast the refund transaction
	err = watchtower.CheckExpiredTimeLocks(ctx, client, broadcastedNode, currentHeight, common.Regtest)
	require.NoError(t, err)

	// Verify refund tx is confirmed
	blockHashes, err = client.GenerateToAddress(1, randomAddress, nil)
	require.NoError(t, err)
	block, err = client.GetBlockVerbose(blockHashes[0])
	require.NoError(t, err)
	require.Contains(t, block.Tx, refundTx.TxID(), "Refund transaction should be in the block")
	_, err = client.GenerateToAddress(1, randomAddress, nil)
	require.NoError(t, err)

	// Wait for refund confirmation with retry logic while continuously generating new blocks
	var finalNode *ent.TreeNode
	for range 15 {
		time.Sleep(500 * time.Millisecond)
		finalNode, err = dbClient.TreeNode.Get(ctx, broadcastedNode.ID)
		require.NoError(t, err)
		if finalNode.RefundConfirmationHeight > 0 {
			break
		}
	}

	require.Greater(t, finalNode.NodeConfirmationHeight, uint64(0), "Node confirmation height should be set to a positive block height")
	require.Greater(t, finalNode.RefundConfirmationHeight, uint64(0), "Refund confirmation height should be set to a positive block height")
}

func TestTimelockExpirationMultiLevelTree(t *testing.T) {
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

	// Create a multi-level tree with 1 level (root + 2 children + 2 leaves = 5 nodes total)
	tree, nodes, err := testutil.CreateNewTreeWithLevels(walletConfig, faucet, leafPrivKey, 100_000, 1)
	require.NoError(t, err)
	require.Equal(t, 5, len(nodes))

	// Get the root node and a leaf node
	rootNode := nodes[0]
	leafNode := nodes[len(nodes)-1]   // Last node should be a leaf
	parentNode := nodes[len(nodes)-3] // Parent of the leaf node
	require.Equal(t, parentNode.Id, *leafNode.ParentNodeId)

	// Get the signing key for the leaf node
	signingKeyBytes := tree.Children[1].SigningPrivateKey
	signingKey := secp256k1.PrivKeyFromBytes(signingKeyBytes)

	// Reduce timelock for both root and leaf nodes
	getCurrentTimelock := func(txBytes []byte) int64 {
		tx, err := common.TxFromRawTxBytes(txBytes)
		require.NoError(t, err)
		return int64(tx.TxIn[0].Sequence & 0xFFFF)
	}

	// Reduce timelock on leaf node transaction
	for getCurrentTimelock(leafNode.NodeTx) > spark.TimeLockInterval*2 {
		updatedNodes, err := wallet.RefreshTimelockNodes(context.Background(), walletConfig, []*pb.TreeNode{leafNode}, parentNode, signingKey)
		require.NoError(t, err)
		leafNode = updatedNodes[0]
	}
	require.LessOrEqual(t, getCurrentTimelock(leafNode.NodeTx), int64(spark.TimeLockInterval*2))

	ctx, dbClient, err := testutil.TestContext(config)
	require.NoError(t, err)

	// Serialize transactions for database queries
	rootNodeTx, err := common.TxFromRawTxBytes(rootNode.GetNodeTx())
	require.NoError(t, err)
	rootNodeTxBytes, err := serializeTx(rootNodeTx)
	require.NoError(t, err)

	parentNodeTx, err := common.TxFromRawTxBytes(parentNode.GetNodeTx())
	require.NoError(t, err)
	parentNodeTxBytes, err := serializeTx(parentNodeTx)
	require.NoError(t, err)

	leafNodeTx, err := common.TxFromRawTxBytes(leafNode.GetNodeTx())
	require.NoError(t, err)
	leafNodeTxBytes, err := serializeTx(leafNodeTx)
	require.NoError(t, err)

	// Generate a block to start
	randomAddress, err := common.P2TRRawAddressFromPublicKey(leafPrivKey.PubKey().SerializeCompressed(), common.Regtest)
	require.NoError(t, err)
	_, err = client.GenerateToAddress(1, randomAddress, nil)
	require.NoError(t, err)

	// Broadcast and confirm root node transaction
	_, err = client.SendRawTransaction(rootNodeTx, false)
	require.NoError(t, err)
	blockHashes, err := client.GenerateToAddress(1, randomAddress, nil)
	require.NoError(t, err)
	block, err := client.GetBlockVerbose(blockHashes[0])
	require.NoError(t, err)
	require.Contains(t, block.Tx, rootNodeTx.TxID())

	// Get the nodes from the database and verify initial state
	rootDbNode, err := dbClient.TreeNode.Query().
		Where(treenode.RawTx(rootNodeTxBytes)).
		Only(ctx)
	require.NoError(t, err)

	parentDbNode, err := dbClient.TreeNode.Query().
		Where(treenode.RawTx(parentNodeTxBytes)).
		Only(ctx)
	require.NoError(t, err)

	leafDbNode, err := dbClient.TreeNode.Query().
		Where(treenode.RawTx(leafNodeTxBytes)).
		Only(ctx)
	require.NoError(t, err)

	// Generate additional blocks to ensure confirmations are processed
	_, err = client.GenerateToAddress(2, randomAddress, nil)
	require.NoError(t, err)

	// Wait for root node confirmation with retry logic
	var confirmedRootNode *ent.TreeNode
	for range 10 {
		time.Sleep(500 * time.Millisecond)
		confirmedRootNode, err = dbClient.TreeNode.Get(ctx, rootDbNode.ID)
		require.NoError(t, err)
		if confirmedRootNode.NodeConfirmationHeight > 0 {
			break
		}
	}
	require.Greater(t, confirmedRootNode.NodeConfirmationHeight, uint64(0), "Root node confirmation height should be set")

	// Generate blocks until parent node timelock expires
	for getCurrentTimelock(leafNode.NodeTx) > spark.TimeLockInterval*2 {
		updatedNodes, err := wallet.RefreshTimelockNodes(context.Background(), walletConfig, []*pb.TreeNode{leafNode}, parentNode, signingKey)
		require.NoError(t, err)
		leafNode = updatedNodes[0]
	}
	// Mine additional block to trigger watchtower check
	_, err = client.GenerateToAddress(1, randomAddress, nil)
	require.NoError(t, err)

	// Get updated parent node from database
	confirmedParentNode, err := dbClient.TreeNode.Get(ctx, parentDbNode.ID)
	require.NoError(t, err)

	// Call watchtower to check expired timelocks for parent node - this should broadcast the parent node transaction
	_, err = client.SendRawTransaction(parentNodeTx, false)
	require.NoError(t, err)

	// Verify parent node tx is confirmed in the next block
	blockHashes, err = client.GenerateToAddress(1, randomAddress, nil)
	require.NoError(t, err)
	block, err = client.GetBlockVerbose(blockHashes[0])
	require.NoError(t, err)
	require.Contains(t, block.Tx, parentNodeTx.TxID(), "Parent node transaction should be in the block")

	// Wait for parent node confirmation with retry logic
	for range 10 {
		time.Sleep(500 * time.Millisecond)
		confirmedParentNode, err = dbClient.TreeNode.Get(ctx, parentDbNode.ID)
		require.NoError(t, err)
		if confirmedParentNode.NodeConfirmationHeight > 0 {
			break
		}
	}
	require.Greater(t, confirmedParentNode.NodeConfirmationHeight, uint64(0), "Parent node confirmation height should be set")

	// Get updated leaf node from database
	confirmedLeafNode, err := dbClient.TreeNode.Get(ctx, leafDbNode.ID)
	require.NoError(t, err)
	require.Equal(t, uint64(0), confirmedLeafNode.NodeConfirmationHeight, "Leaf node should not be confirmed yet")

	// Mine additional block to trigger watchtower check
	_, err = client.GenerateToAddress(1, randomAddress, nil)
	require.NoError(t, err)

	// Generate blocks until refund transaction timelock expires
	timelock := getCurrentTimelock(leafNode.NodeTx) + spark.WatchtowerTimeLockBuffer
	_, err = client.GenerateToAddress(timelock, randomAddress, nil)
	require.NoError(t, err)

	// Get current block height
	currentHeight, err := client.GetBlockCount()
	require.NoError(t, err)

	// Calculate expected minimum height (parent confirmation + timelock)
	expectedMinHeight := int64(confirmedParentNode.NodeConfirmationHeight) + getCurrentTimelock(leafNode.NodeTx)
	require.Greater(t, currentHeight, expectedMinHeight, "Current block height should be greater than parent confirmation height + timelock")

	// Get updated leaf node from database
	confirmedLeafNode, err = dbClient.TreeNode.Get(ctx, leafDbNode.ID)
	require.NoError(t, err)

	// Call watchtower to check expired timelocks for leaf node - this should broadcast the leaf node transaction
	err = watchtower.CheckExpiredTimeLocks(ctx, client, confirmedLeafNode, currentHeight, common.Regtest)
	require.NoError(t, err)

	// Verify leaf node tx is confirmed in the next block
	blockHashes, err = client.GenerateToAddress(1, randomAddress, nil)
	require.NoError(t, err)
	block, err = client.GetBlockVerbose(blockHashes[0])
	require.NoError(t, err)
	require.Contains(t, block.Tx, leafNodeTx.TxID(), "Leaf node transaction should be in the block")

	// Wait for leaf node confirmation with retry logic
	var finalLeafNode *ent.TreeNode
	for range 10 {
		time.Sleep(500 * time.Millisecond)
		finalLeafNode, err = dbClient.TreeNode.Get(ctx, leafDbNode.ID)
		require.NoError(t, err)
		if finalLeafNode.NodeConfirmationHeight > 0 {
			break
		}
	}

	require.Greater(t, finalLeafNode.NodeConfirmationHeight, uint64(0), "Leaf node confirmation height should be set to a positive block height")
}

func TestTimelockExpirationAfterLightningTransfer(t *testing.T) {
	// Create user and ssp configs
	userConfig, err := testutil.TestWalletConfig()
	require.NoError(t, err)

	sspConfig, err := testutil.TestWalletConfig()
	require.NoError(t, err)

	config, err := testutil.TestConfig()
	require.NoError(t, err)

	client, err := testutil.NewRegtestClient()
	require.NoError(t, err)

	faucet := testutil.GetFaucetInstance(client)
	err = faucet.Refill()
	require.NoError(t, err)

	// User creates an invoice
	invoiceSats := uint64(100)
	preimage, paymentHash := testPreimageHash(t, invoiceSats)
	defer cleanUp(t, userConfig, paymentHash)

	fakeInvoiceCreator := &FakeLightningInvoiceCreator{
		invoice: testInvoice,
	}

	invoice, _, err := wallet.CreateLightningInvoiceWithPreimage(context.Background(), userConfig, fakeInvoiceCreator, 100, "test", [32]byte(preimage))
	require.NoError(t, err)
	require.NotNil(t, invoice)

	// SSP creates a node of 12345 sats
	sspLeafPrivKey, err := secp256k1.GeneratePrivateKey()
	require.NoError(t, err)
	feeSats := uint64(0)
	nodeToSend, err := testutil.CreateNewTree(sspConfig, faucet, sspLeafPrivKey, 12345)
	require.NoError(t, err)

	newLeafPrivKey, err := secp256k1.GeneratePrivateKey()
	require.NoError(t, err)

	leaves := []wallet.LeafKeyTweak{}
	leaves = append(leaves, wallet.LeafKeyTweak{
		Leaf:              nodeToSend,
		SigningPrivKey:    sspLeafPrivKey.Serialize(),
		NewSigningPrivKey: newLeafPrivKey.Serialize(),
	})

	// SSP swaps nodes for preimage (lightning receive)
	response, err := wallet.SwapNodesForPreimage(
		context.Background(),
		sspConfig,
		leaves,
		userConfig.IdentityPublicKey(),
		paymentHash[:],
		nil,
		feeSats,
		true,
		invoiceSats,
	)
	require.NoError(t, err)
	require.Equal(t, response.Preimage, preimage[:])
	senderTransfer := response.Transfer

	// SSP completes the transfer by tweaking the key
	transfer, err := wallet.SendTransferTweakKey(context.Background(), sspConfig, response.Transfer, leaves, nil)
	require.NoError(t, err)
	require.Equal(t, transfer.Status, pb.TransferStatus_TRANSFER_STATUS_SENDER_KEY_TWEAKED)

	// User queries and claims the pending transfer
	receiverToken, err := wallet.AuthenticateWithServer(context.Background(), userConfig)
	require.NoError(t, err, "failed to authenticate receiver")
	receiverCtx := wallet.ContextWithToken(context.Background(), receiverToken)
	pendingTransfer, err := wallet.QueryPendingTransfers(receiverCtx, userConfig)
	require.NoError(t, err, "failed to query pending transfers")
	require.Equal(t, 1, len(pendingTransfer.Transfers))
	receiverTransfer := pendingTransfer.Transfers[0]
	require.Equal(t, receiverTransfer.Id, senderTransfer.Id)
	require.Equal(t, receiverTransfer.Type, pb.TransferType_PREIMAGE_SWAP)

	// User verifies the pending transfer
	leafPrivKeyMap, err := wallet.VerifyPendingTransfer(context.Background(), userConfig, receiverTransfer)
	require.NoError(t, err, "unable to verify pending transfer")
	require.Equal(t, 1, len(*leafPrivKeyMap))
	require.Equal(t, (*leafPrivKeyMap)[nodeToSend.Id], newLeafPrivKey.Serialize(), "wrong leaf signing private key")

	// User claims the transfer with a final signing key
	finalLeafPrivKey, err := secp256k1.GeneratePrivateKey()
	require.NoError(t, err, "failed to create final node signing private key")
	claimingNode := wallet.LeafKeyTweak{
		Leaf:              receiverTransfer.Leaves[0].Leaf,
		SigningPrivKey:    newLeafPrivKey.Serialize(),
		NewSigningPrivKey: finalLeafPrivKey.Serialize(),
	}
	leavesToClaim := [1]wallet.LeafKeyTweak{claimingNode}
	claimedNodes, err := wallet.ClaimTransfer(
		receiverCtx,
		receiverTransfer,
		userConfig,
		leavesToClaim[:],
	)
	require.NoError(t, err, "failed to ClaimTransfer")
	require.Equal(t, 1, len(claimedNodes))
	transferredNode := claimedNodes[0]

	// Now test the watchtower functionality with the transferred node
	getCurrentTimelock := func(txBytes []byte) int64 {
		tx, err := common.TxFromRawTxBytes(txBytes)
		require.NoError(t, err)
		return int64(tx.TxIn[0].Sequence & 0xFFFF)
	}

	// Reduce timelock on the transferred node's refund transaction
	for getCurrentTimelock(transferredNode.RefundTx) > spark.TimeLockInterval*2 {
		transferredNode, err = wallet.RefreshTimelockRefundTx(context.Background(), userConfig, transferredNode, finalLeafPrivKey)
		require.NoError(t, err)
	}
	require.LessOrEqual(t, getCurrentTimelock(transferredNode.RefundTx), int64(spark.TimeLockInterval*2))

	ctx, dbClient, err := testutil.TestContext(config)
	require.NoError(t, err)

	// Serialize the node transaction for database queries
	nodeTx, err := common.TxFromRawTxBytes(transferredNode.GetNodeTx())
	require.NoError(t, err)
	nodeTxBytes, err := serializeTx(nodeTx)
	require.NoError(t, err)

	// Generate a block to start
	randomAddress, err := common.P2TRRawAddressFromPublicKey(finalLeafPrivKey.PubKey().SerializeCompressed(), common.Regtest)
	require.NoError(t, err)
	_, err = client.GenerateToAddress(1, randomAddress, nil)
	require.NoError(t, err)

	// Broadcast transferred node tx
	_, err = client.SendRawTransaction(nodeTx, false)
	require.NoError(t, err)

	// Generate a block to confirm the node transaction
	blockHashes, err := client.GenerateToAddress(1, randomAddress, nil)
	require.NoError(t, err)

	// Verify node tx is confirmed
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

	// Generate blocks until refund transaction timelock expires
	refundTimelock := getCurrentTimelock(transferredNode.RefundTx) + spark.WatchtowerTimeLockBuffer
	_, err = client.GenerateToAddress(refundTimelock, randomAddress, nil)
	require.NoError(t, err)

	// Get current block height
	currentHeight, err := client.GetBlockCount()
	require.NoError(t, err)

	// Calculate expected minimum height (node confirmation + timelock)
	broadcastedNode, err = dbClient.TreeNode.Get(ctx, node.ID)
	require.NoError(t, err)
	expectedMinHeight := int64(broadcastedNode.NodeConfirmationHeight) + getCurrentTimelock(broadcastedNode.RawRefundTx)
	require.Greater(t, currentHeight, expectedMinHeight, "Current block height should be greater than node confirmation height + timelock")

	refundTx, err := common.TxFromRawTxBytes(broadcastedNode.RawRefundTx)
	require.NoError(t, err)

	// Call watchtower to check expired timelocks - this should broadcast the refund transaction
	err = watchtower.CheckExpiredTimeLocks(ctx, client, broadcastedNode, currentHeight, common.Regtest)
	require.NoError(t, err)

	// Verify refund tx is confirmed
	blockHashes, err = client.GenerateToAddress(1, randomAddress, nil)
	require.NoError(t, err)
	block, err = client.GetBlockVerbose(blockHashes[0])
	require.NoError(t, err)
	require.Contains(t, block.Tx, refundTx.TxID(), "Refund transaction should be in the block")

	// Generate one more block to ensure confirmation is processed
	_, err = client.GenerateToAddress(1, randomAddress, nil)
	require.NoError(t, err)

	// Wait for refund confirmation with retry logic
	var finalNode *ent.TreeNode
	for range 15 {
		time.Sleep(500 * time.Millisecond)
		finalNode, err = dbClient.TreeNode.Get(ctx, broadcastedNode.ID)
		require.NoError(t, err)
		if finalNode.RefundConfirmationHeight > 0 {
			break
		}
	}

	require.Greater(t, finalNode.NodeConfirmationHeight, uint64(0), "Node confirmation height should be set to a positive block height")
	require.Greater(t, finalNode.RefundConfirmationHeight, uint64(0), "Refund confirmation height should be set to a positive block height")
}
