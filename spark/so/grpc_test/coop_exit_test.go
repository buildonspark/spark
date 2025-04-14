package grpctest

import (
	"bytes"
	"context"
	"encoding/hex"
	"testing"
	"time"

	"github.com/btcsuite/btcd/wire"
	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/lightsparkdev/spark-go/common"
	"github.com/lightsparkdev/spark-go/proto/spark"
	"github.com/lightsparkdev/spark-go/so/handler"
	testutil "github.com/lightsparkdev/spark-go/test_util"
	"github.com/lightsparkdev/spark-go/wallet"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupUsers(t *testing.T, amountSats int64) (*wallet.Config, *wallet.Config, wallet.LeafKeyTweak) {
	config, err := testutil.TestWalletConfig()
	assert.NoError(t, err)
	sspConfig, err := testutil.TestWalletConfig()
	assert.NoError(t, err)

	leafPrivKey, err := secp256k1.GeneratePrivateKey()
	assert.NoError(t, err)
	rootNode, err := testutil.CreateNewTree(config, faucet, leafPrivKey, amountSats)
	assert.NoError(t, err)

	transferNode := wallet.LeafKeyTweak{
		Leaf:              rootNode,
		SigningPrivKey:    leafPrivKey.Serialize(),
		NewSigningPrivKey: sspConfig.IdentityPrivateKey.Serialize(),
	}

	return config, sspConfig, transferNode
}

func createTestCoopExitAndConnectorOutputs(
	t *testing.T,
	config *wallet.Config,
	leafCount int,
	outPoint *wire.OutPoint,
	userPubKey *secp256k1.PublicKey, userAmountSats int64,
) (*wire.MsgTx, []*wire.OutPoint) {
	// Get arbitrary SSP address, using identity for convenience
	sspIntermediateAddress, err := common.P2TRAddressFromPublicKey(config.IdentityPublicKey(), config.Network)
	assert.NoError(t, err)

	withdrawAddress, err := common.P2TRAddressFromPublicKey(userPubKey.SerializeCompressed(), config.Network)
	assert.NoError(t, err)

	dustAmountSats := 354
	intermediateAmountSats := int64((leafCount + 1) * dustAmountSats)

	exitTx, err := testutil.CreateTestCoopExitTransaction(outPoint, *withdrawAddress, userAmountSats, *sspIntermediateAddress, intermediateAmountSats)
	assert.NoError(t, err)

	exitTxHash := exitTx.TxHash()
	intermediateOutPoint := wire.NewOutPoint(&exitTxHash, 1)
	connectorP2trAddrs := make([]string, 0)
	for range leafCount + 1 {
		connectorPrivKey, err := secp256k1.GeneratePrivateKey()
		assert.NoError(t, err)
		connectorPubKey := connectorPrivKey.PubKey()
		connectorAddress, err := common.P2TRAddressFromPublicKey(connectorPubKey.SerializeCompressed(), config.Network)
		assert.NoError(t, err)
		connectorP2trAddrs = append(connectorP2trAddrs, *connectorAddress)
	}
	feeBumpAddr := connectorP2trAddrs[len(connectorP2trAddrs)-1]
	connectorP2trAddrs = connectorP2trAddrs[:len(connectorP2trAddrs)-1]
	connectorTx, err := testutil.CreateTestConnectorTransaction(intermediateOutPoint, intermediateAmountSats, connectorP2trAddrs, feeBumpAddr)
	assert.NoError(t, err)

	connectorOutputs := make([]*wire.OutPoint, 0)
	for i := range connectorTx.TxOut[:len(connectorTx.TxOut)-1] {
		txHash := connectorTx.TxHash()
		connectorOutputs = append(connectorOutputs, wire.NewOutPoint(&txHash, uint32(i)))
	}
	return exitTx, connectorOutputs
}

func TestCoopExitBasic(t *testing.T) {
	client, err := testutil.NewRegtestClient()
	assert.NoError(t, err)

	coin, err := faucet.Fund()
	assert.NoError(t, err)

	amountSats := int64(100_000)
	config, sspConfig, transferNode := setupUsers(t, amountSats)

	// SSP creates transactions
	withdrawPrivKey, err := secp256k1.GeneratePrivateKey()
	assert.NoError(t, err)
	exitTx, connectorOutputs := createTestCoopExitAndConnectorOutputs(
		t, sspConfig, 1, coin.OutPoint, withdrawPrivKey.PubKey(), amountSats,
	)

	// User creates transfer to SSP on the condition that the tx is confirmed
	exitTxID, err := hex.DecodeString(exitTx.TxID())
	assert.NoError(t, err)
	senderTransfer, _, err := wallet.GetConnectorRefundSignatures(
		context.Background(),
		config,
		[]wallet.LeafKeyTweak{transferNode},
		exitTxID,
		connectorOutputs,
		sspConfig.IdentityPrivateKey.PubKey(),
		time.Now().Add(24*time.Hour),
	)
	assert.NoError(t, err)
	assert.Equal(t, senderTransfer.Status, spark.TransferStatus_TRANSFER_STATUS_SENDER_KEY_TWEAK_PENDING)

	// SSP signs exit tx and broadcasts
	signedExitTx, err := testutil.SignFaucetCoin(exitTx, coin.TxOut, coin.Key)
	assert.NoError(t, err)

	_, err = client.SendRawTransaction(signedExitTx, true)
	assert.NoError(t, err)

	// Make sure the exit tx gets enough confirmations
	randomKey, err := secp256k1.GeneratePrivateKey()
	assert.NoError(t, err)
	randomPubKey := randomKey.PubKey()
	randomAddress, err := common.P2TRRawAddressFromPublicKey(randomPubKey.SerializeCompressed(), common.Regtest)
	assert.NoError(t, err)
	// Confirm extra buffer to scan more blocks than needed
	// So that we don't race the chain watcher in this test
	_, err = client.GenerateToAddress(handler.CoopExitConfirmationThreshold+6, randomAddress, nil)
	assert.NoError(t, err)

	// Wait until tx is confirmed and picked up by SO
	sspToken, err := wallet.AuthenticateWithServer(context.Background(), sspConfig)
	assert.NoError(t, err)
	sspCtx := wallet.ContextWithToken(context.Background(), sspToken)

	pendingTransfer, err := wallet.QueryPendingTransfers(sspCtx, sspConfig)
	assert.NoError(t, err)
	startTime := time.Now()
	for len(pendingTransfer.Transfers) == 0 {
		if time.Since(startTime) > 10*time.Second {
			t.Fatalf("timed out waiting for key to be tweaked from tx confirmation")
		}
		time.Sleep(100 * time.Millisecond)
		pendingTransfer, err = wallet.QueryPendingTransfers(sspCtx, sspConfig)
		assert.NoError(t, err)
	}
	receiverTransfer := pendingTransfer.Transfers[0]
	assert.Equal(t, receiverTransfer.Id, senderTransfer.Id)
	assert.Equal(t, receiverTransfer.Status, spark.TransferStatus_TRANSFER_STATUS_SENDER_KEY_TWEAKED)
	require.Equal(t, receiverTransfer.Type, spark.TransferType_COOPERATIVE_EXIT)

	leafPrivKeyMap, err := wallet.VerifyPendingTransfer(context.Background(), sspConfig, receiverTransfer)
	assert.NoError(t, err)
	assert.Equal(t, len(*leafPrivKeyMap), 1)
	assert.True(t, bytes.Equal((*leafPrivKeyMap)[transferNode.Leaf.Id], sspConfig.IdentityPrivateKey.Serialize()))

	// Claim leaf. This requires a loop because sometimes there are
	// delays in processing blocks, and after the tx initially confirms,
	// the SO will still reject a claim until the tx has enough confirmations.
	finalLeafPrivKey, err := secp256k1.GeneratePrivateKey()
	assert.NoError(t, err)
	claimingNode := wallet.LeafKeyTweak{
		Leaf:              senderTransfer.Leaves[0].Leaf,
		SigningPrivKey:    sspConfig.IdentityPrivateKey.Serialize(),
		NewSigningPrivKey: finalLeafPrivKey.Serialize(),
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

func TestCoopExitCannotClaimBeforeConfirm(t *testing.T) {
	coin, err := faucet.Fund()
	assert.NoError(t, err)

	amountSats := int64(100_000)
	config, sspConfig, transferNode := setupUsers(t, amountSats)

	// SSP creates transactions
	withdrawPrivKey, err := secp256k1.GeneratePrivateKey()
	assert.NoError(t, err)
	exitTx, connectorOutputs := createTestCoopExitAndConnectorOutputs(
		t, sspConfig, 1, coin.OutPoint, withdrawPrivKey.PubKey(), amountSats,
	)

	// User creates transfer to SSP on the condition that the tx is confirmed
	exitTxID, err := hex.DecodeString(exitTx.TxID())
	assert.NoError(t, err)
	senderTransfer, _, err := wallet.GetConnectorRefundSignatures(
		context.Background(),
		config,
		[]wallet.LeafKeyTweak{transferNode},
		exitTxID,
		connectorOutputs,
		sspConfig.IdentityPrivateKey.PubKey(),
		time.Now().Add(24*time.Hour),
	)
	assert.NoError(t, err)
	assert.Equal(t, senderTransfer.Status, spark.TransferStatus_TRANSFER_STATUS_SENDER_KEY_TWEAK_PENDING)

	// Prepare for claim
	finalLeafPrivKey, err := secp256k1.GeneratePrivateKey()
	assert.NoError(t, err)
	claimingNode := wallet.LeafKeyTweak{
		Leaf:              senderTransfer.Leaves[0].Leaf,
		SigningPrivKey:    sspConfig.IdentityPrivateKey.Serialize(),
		NewSigningPrivKey: finalLeafPrivKey.Serialize(),
	}

	// Try to claim leaf before exit tx confirms -> should fail
	sspToken, err := wallet.AuthenticateWithServer(context.Background(), sspConfig)
	assert.NoError(t, err)
	sspCtx := wallet.ContextWithToken(context.Background(), sspToken)
	leavesToClaim := [1]wallet.LeafKeyTweak{claimingNode}
	_, err = wallet.ClaimTransfer(
		sspCtx,
		senderTransfer,
		sspConfig,
		leavesToClaim[:],
	)
	if err == nil {
		t.Fatalf("expected error claiming transfer before exit tx confirms")
	}
}

// Start coop exit, SSP doesn't broadcast, should be able to cancel after expiry
func TestCoopExitCancelNoBroadcast(t *testing.T) {
	coin, err := faucet.Fund()
	assert.NoError(t, err)

	amountSats := int64(100_000)
	config, sspConfig, transferNode := setupUsers(t, amountSats)

	withdrawPrivKey, err := secp256k1.GeneratePrivateKey()
	assert.NoError(t, err)
	exitTx, connectorOutputs := createTestCoopExitAndConnectorOutputs(
		t, sspConfig, 1, coin.OutPoint, withdrawPrivKey.PubKey(), amountSats,
	)

	exitTxID, err := hex.DecodeString(exitTx.TxID())
	assert.NoError(t, err)
	expiryDelta := 1 * time.Second
	senderTransfer, _, err := wallet.GetConnectorRefundSignatures(
		context.Background(),
		config,
		[]wallet.LeafKeyTweak{transferNode},
		exitTxID,
		connectorOutputs,
		sspConfig.IdentityPrivateKey.PubKey(),
		time.Now().Add(expiryDelta),
	)
	assert.NoError(t, err)

	time.Sleep(expiryDelta)

	_, err = wallet.CancelSendTransfer(context.Background(), config, senderTransfer)
	assert.NoError(t, err)
}

// Start coop exit, SSP broadcasts, should not be able to cancel after expiry
func TestCoopExitCannotCancelAfterBroadcast(t *testing.T) {
	client, err := testutil.NewRegtestClient()
	assert.NoError(t, err)

	coin, err := faucet.Fund()
	assert.NoError(t, err)

	amountSats := int64(100_000)
	config, sspConfig, transferNode := setupUsers(t, amountSats)

	withdrawPrivKey, err := secp256k1.GeneratePrivateKey()
	assert.NoError(t, err)
	exitTx, connectorOutputs := createTestCoopExitAndConnectorOutputs(
		t, sspConfig, 1, coin.OutPoint, withdrawPrivKey.PubKey(), amountSats,
	)

	exitTxID, err := hex.DecodeString(exitTx.TxID())
	assert.NoError(t, err)
	expiryDelta := 1 * time.Second
	senderTransfer, _, err := wallet.GetConnectorRefundSignatures(
		context.Background(),
		config,
		[]wallet.LeafKeyTweak{transferNode},
		exitTxID,
		connectorOutputs,
		sspConfig.IdentityPrivateKey.PubKey(),
		time.Now().Add(expiryDelta),
	)
	assert.NoError(t, err)

	time.Sleep(expiryDelta)

	// Broadcast and make sure 1. we can't cancel, and 2. we can claim
	signedExitTx, err := testutil.SignFaucetCoin(exitTx, coin.TxOut, coin.Key)
	assert.NoError(t, err)

	_, err = client.SendRawTransaction(signedExitTx, true)
	assert.NoError(t, err)

	randomKey, err := secp256k1.GeneratePrivateKey()
	assert.NoError(t, err)
	randomPubKey := randomKey.PubKey()
	randomAddress, err := common.P2TRRawAddressFromPublicKey(randomPubKey.SerializeCompressed(), common.Regtest)
	assert.NoError(t, err)

	_, err = client.GenerateToAddress(handler.CoopExitConfirmationThreshold+6, randomAddress, nil)
	assert.NoError(t, err)

	sspToken, err := wallet.AuthenticateWithServer(context.Background(), sspConfig)
	assert.NoError(t, err)
	sspCtx := wallet.ContextWithToken(context.Background(), sspToken)

	pendingTransfer, err := wallet.QueryPendingTransfers(sspCtx, sspConfig)
	assert.NoError(t, err)
	startTime := time.Now()
	for len(pendingTransfer.Transfers) == 0 {
		if time.Since(startTime) > 10*time.Second {
			t.Fatalf("timed out waiting for key to be tweaked from tx confirmation")
		}
		time.Sleep(100 * time.Millisecond)
		pendingTransfer, err = wallet.QueryPendingTransfers(sspCtx, sspConfig)
		assert.NoError(t, err)
	}
	receiverTransfer := pendingTransfer.Transfers[0]
	assert.Equal(t, receiverTransfer.Id, senderTransfer.Id)
	assert.Equal(t, receiverTransfer.Status, spark.TransferStatus_TRANSFER_STATUS_SENDER_KEY_TWEAKED)
	require.Equal(t, receiverTransfer.Type, spark.TransferType_COOPERATIVE_EXIT)

	leafPrivKeyMap, err := wallet.VerifyPendingTransfer(context.Background(), sspConfig, receiverTransfer)
	assert.NoError(t, err)
	assert.Equal(t, len(*leafPrivKeyMap), 1)
	assert.True(t, bytes.Equal((*leafPrivKeyMap)[transferNode.Leaf.Id], sspConfig.IdentityPrivateKey.Serialize()))

	// Fail to cancel
	_, err = wallet.CancelSendTransfer(context.Background(), config, senderTransfer)
	assert.Error(t, err, "expected error cancelling transfer after exit tx confirmed")

	// Succeed in claiming
	finalLeafPrivKey, err := secp256k1.GeneratePrivateKey()
	assert.NoError(t, err)
	claimingNode := wallet.LeafKeyTweak{
		Leaf:              senderTransfer.Leaves[0].Leaf,
		SigningPrivKey:    sspConfig.IdentityPrivateKey.Serialize(),
		NewSigningPrivKey: finalLeafPrivKey.Serialize(),
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
