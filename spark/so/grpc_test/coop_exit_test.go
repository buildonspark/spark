package grpctest

import (
	"context"
	"encoding/hex"
	"testing"
	"time"

	"github.com/lightsparkdev/spark/common/keys"

	"github.com/btcsuite/btcd/wire"
	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/proto/spark"
	pb "github.com/lightsparkdev/spark/proto/spark"
	"github.com/lightsparkdev/spark/so/handler"
	sparktesting "github.com/lightsparkdev/spark/testing"
	"github.com/lightsparkdev/spark/testing/wallet"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func setupUsers(t *testing.T, amountSats int64) (*wallet.TestWalletConfig, *wallet.TestWalletConfig, wallet.LeafKeyTweak) {
	config, err := sparktesting.TestWalletConfig()
	require.NoError(t, err)
	sspConfig, err := sparktesting.TestWalletConfig()
	require.NoError(t, err)

	leafPrivKey, err := keys.GeneratePrivateKey()
	require.NoError(t, err)

	rootNode, err := sparktesting.CreateNewTree(config, faucet, leafPrivKey, amountSats)
	require.NoError(t, err)

	transferNode := wallet.LeafKeyTweak{
		Leaf:              rootNode,
		SigningPrivKey:    leafPrivKey,
		NewSigningPrivKey: sspConfig.IdentityPrivateKey,
	}

	return config, sspConfig, transferNode
}

func createTestCoopExitAndConnectorOutputs(
	t *testing.T,
	config *wallet.TestWalletConfig,
	leafCount int,
	outPoint *wire.OutPoint,
	userPubKey keys.Public, userAmountSats int64,
) (*wire.MsgTx, []*wire.OutPoint) {
	// Get arbitrary SSP address, using identity for convenience
	identityPubKey, err := keys.ParsePublicKey(config.IdentityPublicKey().Serialize())
	require.NoError(t, err)
	sspIntermediateAddress, err := common.P2TRAddressFromPublicKey(identityPubKey, config.Network)
	require.NoError(t, err)

	withdrawAddress, err := common.P2TRAddressFromPublicKey(userPubKey, config.Network)
	require.NoError(t, err)

	dustAmountSats := 354
	intermediateAmountSats := int64((leafCount + 1) * dustAmountSats)

	exitTx, err := sparktesting.CreateTestCoopExitTransaction(outPoint, withdrawAddress, userAmountSats, sspIntermediateAddress, intermediateAmountSats)
	require.NoError(t, err)

	exitTxHash := exitTx.TxHash()
	intermediateOutPoint := wire.NewOutPoint(&exitTxHash, 1)
	var connectorP2trAddrs []string
	for range leafCount + 1 {
		connectorPrivKey, err := keys.GeneratePrivateKey()
		require.NoError(t, err)
		connectorAddress, err := common.P2TRAddressFromPublicKey(connectorPrivKey.Public(), config.Network)
		require.NoError(t, err)
		connectorP2trAddrs = append(connectorP2trAddrs, connectorAddress)
	}
	feeBumpAddr := connectorP2trAddrs[len(connectorP2trAddrs)-1]
	connectorP2trAddrs = connectorP2trAddrs[:len(connectorP2trAddrs)-1]
	connectorTx, err := sparktesting.CreateTestConnectorTransaction(intermediateOutPoint, intermediateAmountSats, connectorP2trAddrs, feeBumpAddr)
	require.NoError(t, err)

	var connectorOutputs []*wire.OutPoint
	for i := range connectorTx.TxOut[:len(connectorTx.TxOut)-1] {
		txHash := connectorTx.TxHash()
		connectorOutputs = append(connectorOutputs, wire.NewOutPoint(&txHash, uint32(i)))
	}
	return exitTx, connectorOutputs
}

func waitForPendingTransferToConfirm(
	ctx context.Context,
	t *testing.T,
	config *wallet.TestWalletConfig,
) *spark.Transfer {
	pendingTransfer, err := wallet.QueryPendingTransfers(ctx, config)
	require.NoError(t, err)
	startTime := time.Now()
	for len(pendingTransfer.Transfers) == 0 {
		if time.Since(startTime) > 10*time.Second {
			t.Fatalf("timed out waiting for key to be tweaked from tx confirmation")
		}
		time.Sleep(100 * time.Millisecond)
		pendingTransfer, err = wallet.QueryPendingTransfers(ctx, config)
		require.NoError(t, err)
	}
	return pendingTransfer.Transfers[0]
}

func TestCoopExitBasic(t *testing.T) {
	client := sparktesting.GetBitcoinClient()

	coin, err := faucet.Fund()
	require.NoError(t, err)

	amountSats := int64(100_000)
	config, sspConfig, transferNode := setupUsers(t, amountSats)

	// SSP creates transactions
	withdrawPrivKey, err := keys.GeneratePrivateKey()
	require.NoError(t, err)
	exitTx, connectorOutputs := createTestCoopExitAndConnectorOutputs(
		t, sspConfig, 1, coin.OutPoint, withdrawPrivKey.Public(), amountSats,
	)

	// User creates transfer to SSP on the condition that the tx is confirmed
	exitTxID, err := hex.DecodeString(exitTx.TxID())
	require.NoError(t, err)
	senderTransfer, _, err := wallet.GetConnectorRefundSignaturesV2(
		t.Context(),
		config,
		[]wallet.LeafKeyTweak{transferNode},
		exitTxID,
		connectorOutputs,
		sspConfig.IdentityPublicKey(),
		time.Now().Add(24*time.Hour),
	)
	require.NoError(t, err)
	assert.Equal(t, spark.TransferStatus_TRANSFER_STATUS_SENDER_KEY_TWEAK_PENDING, senderTransfer.Status)

	// SSP signs exit tx and broadcasts
	signedExitTx, err := sparktesting.SignFaucetCoin(exitTx, coin.TxOut, coin.Key)
	require.NoError(t, err)

	_, err = client.SendRawTransaction(signedExitTx, true)
	require.NoError(t, err)

	// Make sure the exit tx gets enough confirmations
	randomKey, err := keys.GeneratePrivateKey()
	require.NoError(t, err)
	randomAddress, err := common.P2TRRawAddressFromPublicKey(randomKey.Public(), common.Regtest)
	require.NoError(t, err)
	// Confirm extra buffer to scan more blocks than needed
	// So that we don't race the chain watcher in this test
	_, err = client.GenerateToAddress(handler.CoopExitConfirmationThreshold+6, randomAddress, nil)
	require.NoError(t, err)

	// Wait until tx is confirmed and picked up by SO
	sspToken, err := wallet.AuthenticateWithServer(t.Context(), sspConfig)
	require.NoError(t, err)
	sspCtx := wallet.ContextWithToken(t.Context(), sspToken)

	receiverTransfer := waitForPendingTransferToConfirm(sspCtx, t, sspConfig)
	assert.Equal(t, senderTransfer.Id, receiverTransfer.Id)
	assert.Equal(t, spark.TransferStatus_TRANSFER_STATUS_SENDER_KEY_TWEAKED, receiverTransfer.Status)
	assert.Equal(t, spark.TransferType_COOPERATIVE_EXIT, receiverTransfer.Type)

	leafPrivKeyMap, err := wallet.VerifyPendingTransfer(t.Context(), sspConfig, receiverTransfer)
	require.NoError(t, err)
	assert.Len(t, leafPrivKeyMap, 1)
	assert.Equal(t, leafPrivKeyMap[transferNode.Leaf.Id], sspConfig.IdentityPrivateKey.Serialize())

	// Claim leaf. This requires a loop because sometimes there are
	// delays in processing blocks, and after the tx initially confirms,
	// the SO will still reject a claim until the tx has enough confirmations.
	finalLeafPrivKey, err := keys.GeneratePrivateKey()
	require.NoError(t, err)
	claimingNode := wallet.LeafKeyTweak{
		Leaf:              senderTransfer.Leaves[0].Leaf,
		SigningPrivKey:    sspConfig.IdentityPrivateKey,
		NewSigningPrivKey: finalLeafPrivKey,
	}
	leavesToClaim := []wallet.LeafKeyTweak{claimingNode}
	startTime := time.Now()
	for {
		// Refresh transfer status from server to make sure the ClaimTransfer function has the correct transfer status
		currentTransfer := receiverTransfer
		transfers, _, err := wallet.QueryAllTransfersWithTypes(
			sspCtx, sspConfig, 100, 0, []pb.TransferType{pb.TransferType_COOPERATIVE_EXIT},
		)
		require.NoError(t, err)
		for _, tr := range transfers {
			if tr.Id == receiverTransfer.Id {
				currentTransfer = tr
				break
			}
		}

		_, err = wallet.ClaimTransfer(sspCtx, currentTransfer, sspConfig, leavesToClaim)
		if err == nil {
			break
		}
		time.Sleep(200 * time.Millisecond)
		if time.Since(startTime) > 15*time.Second {
			t.Fatalf("timed out waiting for tx to confirm")
		}
	}
}

func TestCoopExitCannotClaimBeforeEnoughConfirmations(t *testing.T) {
	client := sparktesting.GetBitcoinClient()

	coin, err := faucet.Fund()
	require.NoError(t, err)

	amountSats := int64(100_000)
	config, sspConfig, transferNode := setupUsers(t, amountSats)

	// SSP creates transactions
	withdrawPrivKey, err := keys.GeneratePrivateKey()
	require.NoError(t, err)
	exitTx, connectorOutputs := createTestCoopExitAndConnectorOutputs(
		t, sspConfig, 1, coin.OutPoint, withdrawPrivKey.Public(), amountSats,
	)

	// User creates transfer to SSP on the condition that the tx is confirmed
	exitTxID, err := hex.DecodeString(exitTx.TxID())
	require.NoError(t, err)
	_, _, err = wallet.GetConnectorRefundSignaturesV2(
		t.Context(),
		config,
		[]wallet.LeafKeyTweak{transferNode},
		exitTxID,
		connectorOutputs,
		sspConfig.IdentityPublicKey(),
		time.Now().Add(24*time.Hour),
	)
	require.NoError(t, err)

	// SSP signs exit tx and broadcasts
	signedExitTx, err := sparktesting.SignFaucetCoin(exitTx, coin.TxOut, coin.Key)
	require.NoError(t, err)

	_, err = client.SendRawTransaction(signedExitTx, true)
	require.NoError(t, err)

	randomKey, err := keys.GeneratePrivateKey()
	require.NoError(t, err)
	randomAddress, err := common.P2TRRawAddressFromPublicKey(randomKey.Public(), common.Regtest)
	require.NoError(t, err)
	// Confirm half the threshold
	_, err = client.GenerateToAddress(handler.CoopExitConfirmationThreshold/2, randomAddress, nil)
	require.NoError(t, err)

	// Wait until tx is confirmed and picked up by SO
	sspToken, err := wallet.AuthenticateWithServer(t.Context(), sspConfig)
	require.NoError(t, err)
	sspCtx := wallet.ContextWithToken(t.Context(), sspToken)

	receiverTransfer := waitForPendingTransferToConfirm(sspCtx, t, sspConfig)

	// Try to claim leaf before exit tx confirms -> should fail
	finalLeafPrivKey, err := keys.GeneratePrivateKey()
	require.NoError(t, err)
	claimingNode := wallet.LeafKeyTweak{
		Leaf:              receiverTransfer.Leaves[0].Leaf,
		SigningPrivKey:    sspConfig.IdentityPrivateKey,
		NewSigningPrivKey: finalLeafPrivKey,
	}
	leavesToClaim := [1]wallet.LeafKeyTweak{claimingNode}
	_, err = wallet.ClaimTransfer(
		sspCtx,
		receiverTransfer,
		sspConfig,
		leavesToClaim[:],
	)
	require.Error(t, err, "expected error claiming transfer before exit tx confirms")
	stat, ok := status.FromError(err)
	assert.True(t, ok)
	assert.Equal(t, codes.FailedPrecondition, stat.Code())
}

func TestCoopExitCannotClaimBeforeConfirm(t *testing.T) {
	coin, err := faucet.Fund()
	require.NoError(t, err)

	amountSats := int64(100_000)
	config, sspConfig, transferNode := setupUsers(t, amountSats)

	// SSP creates transactions
	withdrawPrivKey, err := keys.GeneratePrivateKey()
	require.NoError(t, err)
	exitTx, connectorOutputs := createTestCoopExitAndConnectorOutputs(
		t, sspConfig, 1, coin.OutPoint, withdrawPrivKey.Public(), amountSats,
	)

	// User creates transfer to SSP on the condition that the tx is confirmed
	exitTxID, err := hex.DecodeString(exitTx.TxID())
	require.NoError(t, err)
	senderTransfer, _, err := wallet.GetConnectorRefundSignaturesV2(
		t.Context(),
		config,
		[]wallet.LeafKeyTweak{transferNode},
		exitTxID,
		connectorOutputs,
		sspConfig.IdentityPublicKey(),
		time.Now().Add(24*time.Hour),
	)
	require.NoError(t, err)
	assert.Equal(t, spark.TransferStatus_TRANSFER_STATUS_SENDER_KEY_TWEAK_PENDING, senderTransfer.Status)

	// Prepare for claim
	finalLeafPrivKey, err := keys.GeneratePrivateKey()
	require.NoError(t, err)
	claimingNode := wallet.LeafKeyTweak{
		Leaf:              senderTransfer.Leaves[0].Leaf,
		SigningPrivKey:    sspConfig.IdentityPrivateKey,
		NewSigningPrivKey: finalLeafPrivKey,
	}

	// Try to claim leaf before exit tx confirms -> should fail
	sspToken, err := wallet.AuthenticateWithServer(t.Context(), sspConfig)
	require.NoError(t, err)
	sspCtx := wallet.ContextWithToken(t.Context(), sspToken)
	leavesToClaim := [1]wallet.LeafKeyTweak{claimingNode}
	_, err = wallet.ClaimTransferTweakKeys(
		sspCtx,
		senderTransfer,
		sspConfig,
		leavesToClaim[:],
	)
	require.Error(t, err, "expected error claiming transfer before exit tx confirms")
	stat, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.FailedPrecondition, stat.Code())
}

// Start coop exit, SSP doesn't broadcast, should be able to cancel after expiry
func TestCoopExitCancelNoBroadcast(t *testing.T) {
	coin, err := faucet.Fund()
	require.NoError(t, err)

	amountSats := int64(100_000)
	config, sspConfig, transferNode := setupUsers(t, amountSats)

	withdrawPrivKey, err := keys.GeneratePrivateKey()
	require.NoError(t, err)
	exitTx, connectorOutputs := createTestCoopExitAndConnectorOutputs(
		t, sspConfig, 1, coin.OutPoint, withdrawPrivKey.Public(), amountSats,
	)

	exitTxID, err := hex.DecodeString(exitTx.TxID())
	require.NoError(t, err)
	expiryDelta := 1 * time.Second
	senderTransfer, _, err := wallet.GetConnectorRefundSignatures(
		t.Context(),
		config,
		[]wallet.LeafKeyTweak{transferNode},
		exitTxID,
		connectorOutputs,
		sspConfig.IdentityPublicKey(),
		time.Now().Add(expiryDelta),
	)
	require.NoError(t, err)

	time.Sleep(expiryDelta)

	_, err = wallet.CancelTransfer(t.Context(), config, senderTransfer)
	require.NoError(t, err)
}

// Start coop exit, SSP broadcasts, should not be able to cancel after expiry
func TestCoopExitCannotCancelAfterBroadcast(t *testing.T) {
	client := sparktesting.GetBitcoinClient()

	coin, err := faucet.Fund()
	require.NoError(t, err)

	amountSats := int64(100_000)
	config, sspConfig, transferNode := setupUsers(t, amountSats)

	withdrawPrivKey, err := keys.GeneratePrivateKey()
	require.NoError(t, err)
	exitTx, connectorOutputs := createTestCoopExitAndConnectorOutputs(
		t, sspConfig, 1, coin.OutPoint, withdrawPrivKey.Public(), amountSats,
	)

	exitTxID, err := hex.DecodeString(exitTx.TxID())
	require.NoError(t, err)
	expiryDelta := 1 * time.Second
	senderTransfer, _, err := wallet.GetConnectorRefundSignaturesV2(
		t.Context(),
		config,
		[]wallet.LeafKeyTweak{transferNode},
		exitTxID,
		connectorOutputs,
		sspConfig.IdentityPublicKey(),
		time.Now().Add(expiryDelta),
	)
	require.NoError(t, err)

	time.Sleep(expiryDelta)

	// Broadcast and make sure 1. we can't cancel, and 2. we can claim
	signedExitTx, err := sparktesting.SignFaucetCoin(exitTx, coin.TxOut, coin.Key)
	require.NoError(t, err)

	_, err = client.SendRawTransaction(signedExitTx, true)
	require.NoError(t, err)

	randomKey, err := keys.GeneratePrivateKey()
	require.NoError(t, err)
	randomPubKey := randomKey.Public()
	randomAddress, err := common.P2TRRawAddressFromPublicKey(randomPubKey, common.Regtest)
	require.NoError(t, err)

	_, err = client.GenerateToAddress(handler.CoopExitConfirmationThreshold+6, randomAddress, nil)
	require.NoError(t, err)

	sspToken, err := wallet.AuthenticateWithServer(t.Context(), sspConfig)
	require.NoError(t, err)
	sspCtx := wallet.ContextWithToken(t.Context(), sspToken)

	pendingTransfer, err := wallet.QueryPendingTransfers(sspCtx, sspConfig)
	require.NoError(t, err)
	startTime := time.Now()
	for len(pendingTransfer.Transfers) == 0 {
		if time.Since(startTime) > 10*time.Second {
			t.Fatalf("timed out waiting for key to be tweaked from tx confirmation")
		}
		time.Sleep(100 * time.Millisecond)
		pendingTransfer, err = wallet.QueryPendingTransfers(sspCtx, sspConfig)
		require.NoError(t, err)
	}
	receiverTransfer := pendingTransfer.Transfers[0]
	assert.Equal(t, receiverTransfer.Id, senderTransfer.Id)
	assert.Equal(t, spark.TransferStatus_TRANSFER_STATUS_SENDER_KEY_TWEAKED, receiverTransfer.Status)
	assert.Equal(t, spark.TransferType_COOPERATIVE_EXIT, receiverTransfer.Type)

	leafPrivKeyMap, err := wallet.VerifyPendingTransfer(t.Context(), sspConfig, receiverTransfer)
	require.NoError(t, err)
	assert.Len(t, leafPrivKeyMap, 1)
	assert.Equal(t, leafPrivKeyMap[transferNode.Leaf.Id], sspConfig.IdentityPrivateKey.Serialize())

	// Fail to cancel
	_, err = wallet.CancelTransfer(t.Context(), config, senderTransfer)
	require.Error(t, err, "expected error cancelling transfer after exit tx confirmed")

	// Succeed in claiming
	finalLeafPrivKey, err := keys.GeneratePrivateKey()
	require.NoError(t, err)
	claimingNode := wallet.LeafKeyTweak{
		Leaf:              senderTransfer.Leaves[0].Leaf,
		SigningPrivKey:    sspConfig.IdentityPrivateKey,
		NewSigningPrivKey: finalLeafPrivKey,
	}
	leavesToClaim := [1]wallet.LeafKeyTweak{claimingNode}
	startTime = time.Now()
	for {
		_, err = wallet.ClaimTransfer(
			sspCtx,
			receiverTransfer,
			sspConfig,
			leavesToClaim[:],
		)
		if err == nil {
			break
		}
		time.Sleep(200 * time.Millisecond)
		if time.Since(startTime) > 15*time.Second {
			t.Fatalf("timed out waiting for tx to confirm")
		}
	}
}

// This test starts a coop exit, fails for one operator on the sync, and verifies that no transfer was created across all operators
func TestCoopExitFailureToSync(t *testing.T) {
	_, err := sparktesting.NewRegtestClient()
	require.NoError(t, err)

	coin, err := faucet.Fund()
	require.NoError(t, err)

	amountSats := int64(100_000)
	config, sspConfig, transferNode := setupUsers(t, amountSats)

	// Create gRPC client for V2 function
	conn, err := sparktesting.DangerousNewGRPCConnectionWithoutVerifyTLS(config.CoordinatorAddress(), nil)
	require.NoError(t, err, "failed to create grpc connection")
	defer conn.Close()

	authToken, err := wallet.AuthenticateWithServer(t.Context(), config)
	require.NoError(t, err, "failed to authenticate sender")
	tmpCtx := wallet.ContextWithToken(t.Context(), authToken)

	// Collect existing transfer IDs across all operators before the test
	existingTransferIDs := make(map[string]map[string]bool) // operator_id -> transfer_id -> exists
	for id, op := range config.SigningOperators {
		conn, err := op.NewOperatorGRPCConnection()
		require.NoError(t, err, "connect to %s", id)
		defer conn.Close()

		token, err := wallet.AuthenticateWithServer(t.Context(), config)
		require.NoError(t, err, "auth token for %s", id)

		ctxWithToken := wallet.ContextWithToken(t.Context(), token)
		client := pb.NewSparkServiceClient(conn)

		resp, err := client.QueryAllTransfers(ctxWithToken, &pb.TransferFilter{
			Network: pb.Network_REGTEST,
			Types:   []pb.TransferType{pb.TransferType_COOPERATIVE_EXIT},
		})
		require.NoError(t, err, "query transfers on %s", id)

		transferIDs := make(map[string]bool)
		for _, tr := range resp.Transfers {
			transferIDs[tr.Id] = true
		}
		existingTransferIDs[id] = transferIDs
	}

	// SSP creates transactions
	withdrawPrivKey, err := keys.GeneratePrivateKey()
	require.NoError(t, err)
	exitTx, connectorOutputs := createTestCoopExitAndConnectorOutputs(
		t, sspConfig, 1, coin.OutPoint, withdrawPrivKey.Public(), amountSats,
	)

	soController := sparktesting.NewSparkOperatorController(t)
	err = soController.DisableOperator(t, 2)
	if err != nil {
		t.Fatalf("failed to disable operator 2: %v", err)
	}

	// User creates transfer to SSP on the condition that the tx is confirmed
	exitTxID, err := hex.DecodeString(exitTx.TxID())
	require.NoError(t, err)
	_, _, err = wallet.GetConnectorRefundSignaturesV2(
		tmpCtx,
		config,
		[]wallet.LeafKeyTweak{transferNode},
		exitTxID,
		connectorOutputs,
		sspConfig.IdentityPublicKey(),
		time.Now().Add(24*time.Hour),
	)
	require.Error(t, err)

	err = soController.EnableOperator(t, 2)
	if err != nil {
		t.Fatalf("failed to enable operator 2: %v", err)
	}

	// Verify that any new transfers created during this test have the correct status
	for id, op := range config.SigningOperators {
		conn, err := op.NewOperatorGRPCConnection()
		require.NoError(t, err, "connect to %s", id)
		defer conn.Close()

		token, err := wallet.AuthenticateWithServer(t.Context(), config)
		require.NoError(t, err, "auth token for %s", id)

		ctxWithToken := wallet.ContextWithToken(t.Context(), token)
		client := pb.NewSparkServiceClient(conn)

		resp, err := client.QueryAllTransfers(ctxWithToken, &pb.TransferFilter{
			Network: pb.Network_REGTEST,
			Types:   []pb.TransferType{pb.TransferType_COOPERATIVE_EXIT},
		})
		require.NoError(t, err, "query transfers on %s", id)

		// Check only new transfers that weren't present before this test for their status
		for _, tr := range resp.Transfers {
			if tr.Type == pb.TransferType_COOPERATIVE_EXIT {
				if existingTransferIDs[id][tr.Id] {
					continue // Skip transfers that existed before this test
				}

				// This is a new transfer created during this test - it should have correct status
				if tr.Status != pb.TransferStatus_TRANSFER_STATUS_RETURNED {
					t.Fatalf("operator %s has new transfer %s with wrong status (want RETURNED/EXPIRED/COMPLETED) got %s", id, tr.Id, tr.Status)
				}
			}
		}
	}
}
