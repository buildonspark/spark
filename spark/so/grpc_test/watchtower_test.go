package grpctest

import (
	"testing"
	"time"

	"github.com/lightsparkdev/spark/common/keys"
	"github.com/stretchr/testify/assert"

	"github.com/lightsparkdev/spark"
	"github.com/lightsparkdev/spark/common"
	pb "github.com/lightsparkdev/spark/proto/spark"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/ent/treenode"
	"github.com/lightsparkdev/spark/so/watchtower"
	"github.com/lightsparkdev/spark/testing"
	"github.com/lightsparkdev/spark/testing/wallet"
	"github.com/stretchr/testify/require"
)

func TestTimelockExpirationHappyPath(t *testing.T) {
	skipIfGithubActions(t)
	walletConfig := wallet.NewTestWalletConfig(t)
	config := sparktesting.TestConfig(t)
	client := sparktesting.GetBitcoinClient()
	faucet := sparktesting.GetFaucetInstance(client)
	require.NoError(t, faucet.Refill())

	leafPrivKey := keys.GeneratePrivateKey()
	rootNode, err := wallet.CreateNewTree(walletConfig, faucet, leafPrivKey, 100_000)
	require.NoError(t, err)

	// Reduce timelock
	getCurrentTimelock := func(rootNode *pb.TreeNode) int64 {
		refundTx, err := common.TxFromRawTxBytes(rootNode.GetRefundTx())
		require.NoError(t, err)
		return int64(refundTx.TxIn[0].Sequence & 0xFFFF)
	}

	for getCurrentTimelock(rootNode) > spark.TimeLockInterval*2 {
		rootNode, err = wallet.RefreshTimelockRefundTx(t.Context(), walletConfig, rootNode, leafPrivKey)
		require.NoError(t, err)
	}
	require.LessOrEqual(t, getCurrentTimelock(rootNode), int64(spark.TimeLockInterval*2))

	ctx, dbCtx := db.NewTestContext(t, config.DatabaseDriver(), config.DatabasePath)

	// Broadcast the node transaction
	nodeTx, err := common.TxFromRawTxBytes(rootNode.GetNodeTx())
	require.NoError(t, err)
	nodeTxBytes, err := common.SerializeTx(nodeTx)
	require.NoError(t, err)

	// Generate a block to start
	randomAddress, err := common.P2TRRawAddressFromPublicKey(leafPrivKey.Public(), common.Regtest)
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
	node, err := dbCtx.Client.TreeNode.Query().
		Where(treenode.RawTx(nodeTxBytes)).
		Only(ctx)
	require.NoError(t, err)

	_, err = client.GenerateToAddress(1, randomAddress, nil)
	require.NoError(t, err)

	// Wait for node confirmation with retry logic
	var broadcastedNode *ent.TreeNode
	for range 10 {
		time.Sleep(500 * time.Millisecond)
		broadcastedNode, err = dbCtx.Client.TreeNode.Get(ctx, node.ID)
		require.NoError(t, err)
		if broadcastedNode.NodeConfirmationHeight > 0 {
			break
		}
	}
	require.Positive(t, broadcastedNode.NodeConfirmationHeight, "Node confirmation height should be set to a positive block height")
	require.Zero(t, broadcastedNode.RefundConfirmationHeight, "Refund confirmation height should not be set yet")
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

	err = watchtower.BroadcastTransaction(ctx, client, node.ID.String(), node.RawRefundTx)
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
		finalNode, err = dbCtx.Client.TreeNode.Get(ctx, node.ID)
		require.NoError(t, err)
		if finalNode.RefundConfirmationHeight > 0 {
			break
		}
	}

	assert.Positive(t, finalNode.NodeConfirmationHeight, "Node confirmation height should be set to a positive block height")
	assert.Positive(t, finalNode.RefundConfirmationHeight, "Refund confirmation height should be set to a positive block height")
}

func TestTimelockExpirationTransferredNode(t *testing.T) {
	skipIfGithubActions(t)
	walletConfig := wallet.NewTestWalletConfig(t)
	config := sparktesting.TestConfig(t)
	client := sparktesting.GetBitcoinClient()
	faucet := sparktesting.GetFaucetInstance(client)
	require.NoError(t, faucet.Refill())

	// Create sender wallet and tree
	senderLeafPrivKey := keys.GeneratePrivateKey()
	senderRootNode, err := wallet.CreateNewTree(walletConfig, faucet, senderLeafPrivKey, 100_000)
	require.NoError(t, err)

	// Create receiver wallet
	receiverPrivKey := keys.GeneratePrivateKey()
	receiverConfig := wallet.NewTestWalletConfigWithIdentityKey(t, receiverPrivKey)

	// Prepare transfer - sender creates new signing key for the transfer
	newLeafPrivKey := keys.GeneratePrivateKey()

	transferNode := wallet.LeafKeyTweak{
		Leaf:              senderRootNode,
		SigningPrivKey:    senderLeafPrivKey,
		NewSigningPrivKey: newLeafPrivKey,
	}
	leavesToTransfer := []wallet.LeafKeyTweak{transferNode}

	authToken, err := wallet.AuthenticateWithServer(t.Context(), walletConfig)
	require.NoError(t, err, "failed to authenticate sender")
	senderCtx := wallet.ContextWithToken(t.Context(), authToken)

	// Sender initiates transfer
	senderTransfer, err := wallet.SendTransferWithKeyTweaks(
		senderCtx,
		walletConfig,
		leavesToTransfer,
		receiverPrivKey.Public(),
		time.Now().Add(10*time.Minute),
	)
	require.NoError(t, err, "failed to transfer tree node")

	// Receiver queries and claims the pending transfer
	receiverToken, err := wallet.AuthenticateWithServer(t.Context(), receiverConfig)
	require.NoError(t, err, "failed to authenticate receiver")
	receiverCtx := wallet.ContextWithToken(t.Context(), receiverToken)
	pendingTransfer, err := wallet.QueryPendingTransfers(receiverCtx, receiverConfig)
	require.NoError(t, err, "failed to query pending transfers")
	require.Len(t, pendingTransfer.Transfers, 1)
	receiverTransfer := pendingTransfer.Transfers[0]
	require.Equal(t, senderTransfer.Id, receiverTransfer.Id)

	// Verify the pending transfer
	leafPrivKeyMap, err := wallet.VerifyPendingTransfer(t.Context(), receiverConfig, receiverTransfer)
	require.NoError(t, err, "failed to verify pending transfer")
	require.Len(t, leafPrivKeyMap, 1)
	require.Equal(t, newLeafPrivKey.Serialize(), leafPrivKeyMap[senderRootNode.Id])

	// Receiver claims the transfer with a final signing key
	finalLeafPrivKey := keys.GeneratePrivateKey()
	claimingNode := wallet.LeafKeyTweak{
		Leaf:              receiverTransfer.Leaves[0].Leaf,
		SigningPrivKey:    newLeafPrivKey,
		NewSigningPrivKey: finalLeafPrivKey,
	}
	leavesToClaim := []wallet.LeafKeyTweak{claimingNode}
	claimedNodes, err := wallet.ClaimTransfer(receiverCtx, receiverTransfer, receiverConfig, leavesToClaim)
	require.NoError(t, err, "failed to claim transfer")
	require.Len(t, claimedNodes, 1)
	transferredNode := claimedNodes[0]

	// Reduce timelock on the transferred node's node transaction (not refund yet)
	getCurrentTimelock := func(txBytes []byte) int64 {
		tx, err := common.TxFromRawTxBytes(txBytes)
		require.NoError(t, err)
		return int64(tx.TxIn[0].Sequence & 0xFFFF)
	}

	ctx, dbCtx := db.NewTestContext(t, config.DatabaseDriver(), config.DatabasePath)

	// Serialize the node transaction for database queries
	nodeTx, err := common.TxFromRawTxBytes(transferredNode.GetNodeTx())
	require.NoError(t, err)
	nodeTxBytes, err := common.SerializeTx(nodeTx)
	require.NoError(t, err)

	// Generate a block to start
	randomAddress, err := common.P2TRRawAddressFromPublicKey(finalLeafPrivKey.Public(), common.Regtest)
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
	node, err := dbCtx.Client.TreeNode.Query().
		Where(treenode.RawTx(nodeTxBytes)).
		Only(ctx)
	require.NoError(t, err)

	_, err = client.GenerateToAddress(1, randomAddress, nil)
	require.NoError(t, err)

	// Wait for node confirmation with retry logic
	var broadcastedNode *ent.TreeNode
	for range 10 {
		time.Sleep(500 * time.Millisecond)
		broadcastedNode, err = dbCtx.Client.TreeNode.Get(ctx, node.ID)
		require.NoError(t, err)
		if broadcastedNode.NodeConfirmationHeight > 0 {
			break
		}
	}
	require.Positive(t, broadcastedNode.NodeConfirmationHeight, "Node confirmation height should be set to a positive block height")
	require.Zero(t, broadcastedNode.RefundConfirmationHeight, "Refund confirmation height should not be set yet")
	require.NotEmpty(t, broadcastedNode.RawRefundTx, "RawRefundTx should exist in the database")

	// Now reduce the timelock on the refund transaction
	for getCurrentTimelock(transferredNode.RefundTx) > spark.TimeLockInterval*2 {
		transferredNode, err = wallet.RefreshTimelockRefundTx(t.Context(), receiverConfig, transferredNode, finalLeafPrivKey)
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
	broadcastedNode, err = dbCtx.Client.TreeNode.Get(ctx, node.ID)
	expectedMinHeight := int64(broadcastedNode.NodeConfirmationHeight) + getCurrentTimelock(broadcastedNode.RawRefundTx)
	require.Greater(t, currentHeight, expectedMinHeight, "Current block height should be greater than node confirmation height + timelock")
	require.NoError(t, err)

	refundTx, err := common.TxFromRawTxBytes(broadcastedNode.RawRefundTx)
	require.NoError(t, err)

	// Call watchtower to check expired timelocks - this should broadcast the refund transaction
	err = watchtower.BroadcastTransaction(ctx, client, broadcastedNode.ID.String(), broadcastedNode.RawRefundTx)
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
		finalNode, err = dbCtx.Client.TreeNode.Get(ctx, broadcastedNode.ID)
		require.NoError(t, err)
		if finalNode.RefundConfirmationHeight > 0 {
			break
		}
	}

	require.Positive(t, finalNode.NodeConfirmationHeight, "Node confirmation height should be set to a positive block height")
	require.Positive(t, finalNode.RefundConfirmationHeight, "Refund confirmation height should be set to a positive block height")
}

func TestTimelockExpirationAfterLightningTransfer(t *testing.T) {
	skipIfGithubActions(t)
	// Create user and ssp configs
	userConfig := wallet.NewTestWalletConfig(t)
	sspConfig := wallet.NewTestWalletConfig(t)
	config := sparktesting.TestConfig(t)
	client := sparktesting.GetBitcoinClient()

	faucet := sparktesting.GetFaucetInstance(client)
	require.NoError(t, faucet.Refill())

	// User creates an invoice
	invoiceSats := uint64(100)
	preimage, paymentHash := testPreimageHash(t, invoiceSats)
	defer cleanUp(t, userConfig, paymentHash)

	fakeInvoiceCreator := &FakeLightningInvoiceCreator{
		invoice: testInvoice,
	}

	invoice, _, err := wallet.CreateLightningInvoiceWithPreimage(t.Context(), userConfig, fakeInvoiceCreator, 100, "test", preimage)
	require.NoError(t, err)
	require.NotNil(t, invoice)

	// SSP creates a node of 12345 sats
	sspLeafPrivKey := keys.GeneratePrivateKey()
	feeSats := uint64(0)
	nodeToSend, err := wallet.CreateNewTree(sspConfig, faucet, sspLeafPrivKey, 12345)
	require.NoError(t, err)

	newLeafPrivKey := keys.GeneratePrivateKey()

	leaves := []wallet.LeafKeyTweak{{
		Leaf:              nodeToSend,
		SigningPrivKey:    sspLeafPrivKey,
		NewSigningPrivKey: newLeafPrivKey,
	}}

	// SSP swaps nodes for preimage (lightning receive)
	response, err := wallet.SwapNodesForPreimage(
		t.Context(),
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

	transfer, err := wallet.DeliverTransferPackage(t.Context(), sspConfig, response.Transfer, leaves, nil)
	require.NoError(t, err)
	require.Equal(t, pb.TransferStatus_TRANSFER_STATUS_SENDER_KEY_TWEAKED, transfer.Status)

	// User queries and claims the pending transfer
	receiverToken, err := wallet.AuthenticateWithServer(t.Context(), userConfig)
	require.NoError(t, err, "failed to authenticate receiver")
	receiverCtx := wallet.ContextWithToken(t.Context(), receiverToken)
	pendingTransfer, err := wallet.QueryPendingTransfers(receiverCtx, userConfig)
	require.NoError(t, err, "failed to query pending transfers")
	require.Len(t, pendingTransfer.Transfers, 1)
	receiverTransfer := pendingTransfer.Transfers[0]
	require.Equal(t, receiverTransfer.Id, senderTransfer.Id)
	require.Equal(t, pb.TransferType_PREIMAGE_SWAP, receiverTransfer.Type)

	// User verifies the pending transfer
	leafPrivKeyMap, err := wallet.VerifyPendingTransfer(t.Context(), userConfig, receiverTransfer)
	require.NoError(t, err, "unable to verify pending transfer")
	require.Len(t, leafPrivKeyMap, 1)
	require.Equal(t, leafPrivKeyMap[nodeToSend.Id], newLeafPrivKey.Serialize(), "wrong leaf signing private key")

	// User claims the transfer with a final signing key
	finalLeafPrivKey := keys.GeneratePrivateKey()
	claimingNode := wallet.LeafKeyTweak{
		Leaf:              receiverTransfer.Leaves[0].Leaf,
		SigningPrivKey:    newLeafPrivKey,
		NewSigningPrivKey: finalLeafPrivKey,
	}
	leavesToClaim := []wallet.LeafKeyTweak{claimingNode}
	claimedNodes, err := wallet.ClaimTransfer(
		receiverCtx,
		receiverTransfer,
		userConfig,
		leavesToClaim,
	)
	require.NoError(t, err, "failed to ClaimTransfer")
	require.Len(t, claimedNodes, 1)
	transferredNode := claimedNodes[0]

	// Now test the watchtower functionality with the transferred node
	getCurrentTimelock := func(txBytes []byte) int64 {
		tx, err := common.TxFromRawTxBytes(txBytes)
		require.NoError(t, err)
		return int64(tx.TxIn[0].Sequence & 0xFFFF)
	}

	// Reduce timelock on the transferred node's refund transaction
	for getCurrentTimelock(transferredNode.RefundTx) > spark.TimeLockInterval*2 {
		transferredNode, err = wallet.RefreshTimelockRefundTx(t.Context(), userConfig, transferredNode, finalLeafPrivKey)
		require.NoError(t, err)
	}
	require.LessOrEqual(t, getCurrentTimelock(transferredNode.RefundTx), int64(spark.TimeLockInterval*2))

	ctx, dbCtx := db.NewTestContext(t, config.DatabaseDriver(), config.DatabasePath)

	// Serialize the node transaction for database queries
	nodeTx, err := common.TxFromRawTxBytes(transferredNode.GetNodeTx())
	require.NoError(t, err)
	nodeTxBytes, err := common.SerializeTx(nodeTx)
	require.NoError(t, err)

	// Generate a block to start
	randomAddress, err := common.P2TRRawAddressFromPublicKey(finalLeafPrivKey.Public(), common.Regtest)
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
	node, err := dbCtx.Client.TreeNode.Query().
		Where(treenode.RawTx(nodeTxBytes)).
		Only(ctx)
	require.NoError(t, err)

	_, err = client.GenerateToAddress(1, randomAddress, nil)
	require.NoError(t, err)

	// Wait for node confirmation with retry logic
	var broadcastedNode *ent.TreeNode
	for range 10 {
		time.Sleep(500 * time.Millisecond)
		broadcastedNode, err = dbCtx.Client.TreeNode.Get(ctx, node.ID)
		require.NoError(t, err)
		if broadcastedNode.NodeConfirmationHeight > 0 {
			break
		}
	}
	require.Positive(t, broadcastedNode.NodeConfirmationHeight, "Node confirmation height should be set to a positive block height")
	require.Zero(t, broadcastedNode.RefundConfirmationHeight, "Refund confirmation height should not be set yet")
	require.NotEmpty(t, broadcastedNode.RawRefundTx, "RawRefundTx should exist in the database")

	// Generate blocks until refund transaction timelock expires
	refundTimelock := getCurrentTimelock(transferredNode.RefundTx) + spark.WatchtowerTimeLockBuffer
	_, err = client.GenerateToAddress(refundTimelock, randomAddress, nil)
	require.NoError(t, err)

	// Get current block height
	currentHeight, err := client.GetBlockCount()
	require.NoError(t, err)

	// Calculate expected minimum height (node confirmation + timelock)
	broadcastedNode, err = dbCtx.Client.TreeNode.Get(ctx, node.ID)
	require.NoError(t, err)
	expectedMinHeight := int64(broadcastedNode.NodeConfirmationHeight) + getCurrentTimelock(broadcastedNode.RawRefundTx)
	require.Greater(t, currentHeight, expectedMinHeight, "Current block height should be greater than node confirmation height + timelock")

	refundTx, err := common.TxFromRawTxBytes(broadcastedNode.RawRefundTx)
	require.NoError(t, err)

	// Call watchtower to check expired timelocks - this should broadcast the refund transaction
	err = watchtower.BroadcastTransaction(ctx, client, broadcastedNode.ID.String(), broadcastedNode.RawRefundTx)
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
		finalNode, err = dbCtx.Client.TreeNode.Get(ctx, broadcastedNode.ID)
		require.NoError(t, err)
		if finalNode.RefundConfirmationHeight > 0 {
			break
		}
	}

	assert.Positive(t, finalNode.NodeConfirmationHeight, "Node confirmation height should be set to a positive block height")
	assert.Positive(t, finalNode.RefundConfirmationHeight, "Refund confirmation height should be set to a positive block height")
}
