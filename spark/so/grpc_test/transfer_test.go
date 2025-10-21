package grpctest

import (
	"bytes"
	"crypto/sha256"
	"math/big"
	"math/rand/v2"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/keys"
	sparktesting "github.com/lightsparkdev/spark/testing"

	"github.com/btcsuite/btcd/btcec/v2/schnorr"
	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
	"github.com/lightsparkdev/spark"
	"github.com/lightsparkdev/spark/common"
	pb "github.com/lightsparkdev/spark/proto/spark"
	sparkpb "github.com/lightsparkdev/spark/proto/spark"
	"github.com/lightsparkdev/spark/so/objects"
	"github.com/lightsparkdev/spark/testing/wallet"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const amountSatsToSend = 100_000

func TestTransfer(t *testing.T) {
	// Sender initiates transfer
	senderConfig := wallet.NewTestWalletConfig(t)
	leafPrivKey := keys.GeneratePrivateKey()
	rootNode, err := wallet.CreateNewTree(senderConfig, faucet, leafPrivKey, amountSatsToSend)
	require.NoError(t, err, "failed to create new tree")

	newLeafPrivKey := keys.GeneratePrivateKey()

	receiverPrivKey := keys.GeneratePrivateKey()

	transferNode := wallet.LeafKeyTweak{
		Leaf:              rootNode,
		SigningPrivKey:    leafPrivKey,
		NewSigningPrivKey: newLeafPrivKey,
	}
	leavesToTransfer := [1]wallet.LeafKeyTweak{transferNode}

	conn, err := sparktesting.DangerousNewGRPCConnectionWithoutVerifyTLS(senderConfig.CoordinatorAddress(), nil)
	require.NoError(t, err, "failed to create grpc connection")
	defer conn.Close()

	authToken, err := wallet.AuthenticateWithServer(t.Context(), senderConfig)
	require.NoError(t, err, "failed to authenticate sender")
	senderCtx := wallet.ContextWithToken(t.Context(), authToken)

	senderTransfer, err := wallet.SendTransferWithKeyTweaks(
		senderCtx,
		senderConfig,
		leavesToTransfer[:],
		receiverPrivKey.Public(),
		time.Now().Add(10*time.Minute),
	)
	require.NoError(t, err, "failed to transfer tree node")

	// Receiver queries pending transfer
	receiverConfig := wallet.NewTestWalletConfigWithIdentityKey(t, receiverPrivKey)
	require.NoError(t, err, "failed to create wallet config")
	receiverToken, err := wallet.AuthenticateWithServer(t.Context(), receiverConfig)
	require.NoError(t, err, "failed to authenticate receiver")
	receiverCtx := wallet.ContextWithToken(t.Context(), receiverToken)
	pendingTransfer, err := wallet.QueryPendingTransfers(receiverCtx, receiverConfig)
	require.NoError(t, err, "failed to query pending transfers")
	require.Len(t, pendingTransfer.Transfers, 1)
	receiverTransfer := pendingTransfer.Transfers[0]
	require.Equal(t, senderTransfer.Id, receiverTransfer.Id)
	require.Equal(t, pb.TransferType_TRANSFER, receiverTransfer.Type)

	leafPrivKeyMap, err := wallet.VerifyPendingTransfer(t.Context(), receiverConfig, receiverTransfer)
	require.NoError(t, err)
	require.Equal(t, map[string]keys.Private{rootNode.Id: newLeafPrivKey}, leafPrivKeyMap)

	finalLeafPrivKey := keys.GeneratePrivateKey()
	claimingNode := wallet.LeafKeyTweak{
		Leaf:              receiverTransfer.Leaves[0].Leaf,
		SigningPrivKey:    newLeafPrivKey,
		NewSigningPrivKey: finalLeafPrivKey,
	}
	leavesToClaim := []wallet.LeafKeyTweak{claimingNode}
	res, err := wallet.ClaimTransfer(
		receiverCtx,
		receiverTransfer,
		receiverConfig,
		leavesToClaim,
	)
	require.NoError(t, err, "failed to ClaimTransfer")
	require.Equal(t, res[0].Id, claimingNode.Leaf.Id)
}

func TestQueryPendingTransferByNetwork(t *testing.T) {
	senderConfig := wallet.NewTestWalletConfig(t)
	leafPrivKey := keys.GeneratePrivateKey()
	rootNode, err := wallet.CreateNewTree(senderConfig, faucet, leafPrivKey, amountSatsToSend)
	require.NoError(t, err, "failed to create new tree")

	newLeafPrivKey := keys.GeneratePrivateKey()

	receiverPrivKey := keys.GeneratePrivateKey()

	transferNode := wallet.LeafKeyTweak{
		Leaf:              rootNode,
		SigningPrivKey:    leafPrivKey,
		NewSigningPrivKey: newLeafPrivKey,
	}
	leavesToTransfer := [1]wallet.LeafKeyTweak{transferNode}

	conn, err := sparktesting.DangerousNewGRPCConnectionWithoutVerifyTLS(senderConfig.CoordinatorAddress(), nil)
	require.NoError(t, err, "failed to create grpc connection")
	defer conn.Close()

	authToken, err := wallet.AuthenticateWithServer(t.Context(), senderConfig)
	require.NoError(t, err, "failed to authenticate sender")
	senderCtx := wallet.ContextWithToken(t.Context(), authToken)

	_, err = wallet.SendTransferWithKeyTweaks(
		senderCtx,
		senderConfig,
		leavesToTransfer[:],
		receiverPrivKey.Public(),
		time.Now().Add(10*time.Minute),
	)
	require.NoError(t, err, "failed to transfer tree node")

	receiverConfig := wallet.NewTestWalletConfigWithIdentityKey(t, receiverPrivKey)
	require.NoError(t, err, "failed to create wallet config")
	receiverToken, err := wallet.AuthenticateWithServer(t.Context(), receiverConfig)
	require.NoError(t, err, "failed to authenticate receiver")
	receiverCtx := wallet.ContextWithToken(t.Context(), receiverToken)
	pendingTransfer, err := wallet.QueryPendingTransfers(receiverCtx, receiverConfig)
	require.NoError(t, err, "failed to query pending transfers")
	require.Len(t, pendingTransfer.Transfers, 1)

	incorrectNetworkReceiverConfig := receiverConfig
	incorrectNetworkReceiverConfig.Network = common.Mainnet
	incorrectNetworkReceiverToken, err := wallet.AuthenticateWithServer(t.Context(), incorrectNetworkReceiverConfig)
	require.NoError(t, err, "failed to authenticate receiver")
	incorrectNetworkReceiverCtx := wallet.ContextWithToken(t.Context(), incorrectNetworkReceiverToken)
	pendingTransfer, err = wallet.QueryPendingTransfers(incorrectNetworkReceiverCtx, incorrectNetworkReceiverConfig)
	require.NoError(t, err, "failed to query pending transfers")
	require.Empty(t, pendingTransfer.Transfers)
}

func TestTransferInterrupt(t *testing.T) {
	// Sender initiates transfer
	senderConfig := wallet.NewTestWalletConfig(t)
	leafPrivKey := keys.GeneratePrivateKey()
	rootNode, err := wallet.CreateNewTree(senderConfig, faucet, leafPrivKey, amountSatsToSend)
	require.NoError(t, err, "failed to create new tree")

	newLeafPrivKey := keys.GeneratePrivateKey()
	receiverPrivKey := keys.GeneratePrivateKey()

	transferNode := wallet.LeafKeyTweak{
		Leaf:              rootNode,
		SigningPrivKey:    leafPrivKey,
		NewSigningPrivKey: newLeafPrivKey,
	}
	leavesToTransfer := [1]wallet.LeafKeyTweak{transferNode}

	conn, err := sparktesting.DangerousNewGRPCConnectionWithoutVerifyTLS(senderConfig.CoordinatorAddress(), nil)
	require.NoError(t, err, "failed to create grpc connection")
	defer conn.Close()

	authToken, err := wallet.AuthenticateWithServer(t.Context(), senderConfig)
	require.NoError(t, err, "failed to authenticate sender")
	senderCtx := wallet.ContextWithToken(t.Context(), authToken)

	senderTransfer, err := wallet.SendTransferWithKeyTweaks(
		senderCtx,
		senderConfig,
		leavesToTransfer[:],
		receiverPrivKey.Public(),
		time.Now().Add(10*time.Minute),
	)
	require.NoError(t, err, "failed to transfer tree node")

	// Receiver queries pending transfer
	receiverConfig := wallet.NewTestWalletConfigWithIdentityKey(t, receiverPrivKey)
	receiverToken, err := wallet.AuthenticateWithServer(t.Context(), receiverConfig)
	require.NoError(t, err, "failed to authenticate receiver")
	receiverCtx := wallet.ContextWithToken(t.Context(), receiverToken)
	pendingTransfer, err := wallet.QueryPendingTransfers(receiverCtx, receiverConfig)
	require.NoError(t, err, "failed to query pending transfers")
	require.Len(t, pendingTransfer.Transfers, 1)
	receiverTransfer := pendingTransfer.Transfers[0]
	require.Equal(t, senderTransfer.Id, receiverTransfer.Id)
	require.Equal(t, pb.TransferType_TRANSFER, receiverTransfer.Type)

	leafPrivKeyMap, err := wallet.VerifyPendingTransfer(t.Context(), receiverConfig, receiverTransfer)
	require.NoError(t, err)
	require.Equal(t, map[string]keys.Private{rootNode.Id: newLeafPrivKey}, leafPrivKeyMap)

	finalLeafPrivKey := keys.GeneratePrivateKey()
	claimingNode := wallet.LeafKeyTweak{
		Leaf:              receiverTransfer.Leaves[0].Leaf,
		SigningPrivKey:    newLeafPrivKey,
		NewSigningPrivKey: finalLeafPrivKey,
	}
	leavesToClaim := []wallet.LeafKeyTweak{claimingNode}
	proofs, err := wallet.ClaimTransferTweakKeys(receiverCtx, receiverTransfer, receiverConfig, leavesToClaim)
	require.NoError(t, err, "failed to ClaimTransferTweakKeys")

	// Bring SO 1 down and try to finish claiming.
	soController, err := sparktesting.NewSparkOperatorController(t)
	require.NoError(t, err, "failed to create SO controller")

	err = soController.DisableOperator(t, 1)
	require.NoError(t, err, "failed to disable operator 1")

	_, err = wallet.ClaimTransferSignRefunds(receiverCtx, receiverTransfer, receiverConfig, leavesToClaim, proofs)
	require.Error(t, err, "expected error when claiming transfer")

	err = soController.EnableOperator(t, 1)
	require.NoError(t, err, "failed to enable operator 1")

	attempts := 0
	var claimedNodes []*pb.TreeNode

	// In theory we should be able to claim right away, but in practice, depending on the state of
	// the SOs, it may take a few attempts for it to get back to the right state. Since changing the
	// SO is scary, just retry a few times with a delay.
	for attempts < 5 {
		pendingTransfer, err = wallet.QueryPendingTransfers(receiverCtx, receiverConfig)
		require.NoError(t, err, "failed to query pending transfers")
		require.Len(t, pendingTransfer.Transfers, 1)

		receiverTransfer = pendingTransfer.Transfers[0]
		require.Equal(t, senderTransfer.Id, receiverTransfer.Id)
		require.Equal(t, pb.TransferType_TRANSFER, receiverTransfer.Type)

		res, err := wallet.ClaimTransfer(receiverCtx, receiverTransfer, receiverConfig, leavesToClaim[:])
		if err != nil {
			t.Logf("Failed to ClaimTransfer: %v (attempt %d / 5)", err, attempts+1)
		} else {
			claimedNodes = res
			break
		}

		time.Sleep(1 * time.Second)
		attempts++
	}

	require.NotEmpty(t, claimedNodes, "failed to claim transfer after %d attempts", attempts)
	require.Equal(t, claimingNode.Leaf.Id, claimedNodes[0].Id)
}

func TestTransferRecoverFinalizeSignatures(t *testing.T) {
	// Sender initiates transfer
	senderConfig := wallet.NewTestWalletConfig(t)
	leafPrivKey := keys.GeneratePrivateKey()
	rootNode, err := wallet.CreateNewTree(senderConfig, faucet, leafPrivKey, amountSatsToSend)
	require.NoError(t, err, "failed to create new tree")
	newLeafPrivKey := keys.GeneratePrivateKey()
	receiverPrivKey := keys.GeneratePrivateKey()

	transferNode := wallet.LeafKeyTweak{
		Leaf:              rootNode,
		SigningPrivKey:    leafPrivKey,
		NewSigningPrivKey: newLeafPrivKey,
	}
	leavesToTransfer := []wallet.LeafKeyTweak{transferNode}

	senderTransfer, err := wallet.SendTransferWithKeyTweaks(
		t.Context(),
		senderConfig,
		leavesToTransfer,
		receiverPrivKey.Public(),
		time.Now().Add(10*time.Minute),
	)
	require.NoError(t, err, "failed to transfer tree node")

	// Receiver queries pending transfer
	receiverConfig := wallet.NewTestWalletConfigWithIdentityKey(t, receiverPrivKey)
	receiverToken, err := wallet.AuthenticateWithServer(t.Context(), receiverConfig)
	require.NoError(t, err, "failed to authenticate receiver")
	receiverCtx := wallet.ContextWithToken(t.Context(), receiverToken)
	pendingTransfer, err := wallet.QueryPendingTransfers(receiverCtx, receiverConfig)
	require.NoError(t, err, "failed to query pending transfers")
	require.Len(t, pendingTransfer.Transfers, 1)
	receiverTransfer := pendingTransfer.Transfers[0]
	require.Equal(t, senderTransfer.Id, receiverTransfer.Id)
	require.Equal(t, pb.TransferType_TRANSFER, receiverTransfer.Type)

	leafPrivKeyMap, err := wallet.VerifyPendingTransfer(t.Context(), receiverConfig, receiverTransfer)
	require.NoError(t, err)
	require.Equal(t, map[string]keys.Private{rootNode.Id: newLeafPrivKey}, leafPrivKeyMap)

	finalLeafPrivKey := keys.GeneratePrivateKey()
	claimingNode := wallet.LeafKeyTweak{
		Leaf:              receiverTransfer.Leaves[0].Leaf,
		SigningPrivKey:    newLeafPrivKey,
		NewSigningPrivKey: finalLeafPrivKey,
	}
	leavesToClaim := []wallet.LeafKeyTweak{claimingNode}
	_, err = wallet.ClaimTransferWithoutFinalizeSignatures(
		receiverCtx,
		receiverTransfer,
		receiverConfig,
		leavesToClaim,
	)
	require.NoError(t, err, "failed to ClaimTransfer")

	pendingTransfer, err = wallet.QueryPendingTransfers(receiverCtx, receiverConfig)
	require.NoError(t, err, "failed to query pending transfers")
	require.Len(t, pendingTransfer.Transfers, 1)
	receiverTransfer = pendingTransfer.Transfers[0]
	require.Equal(t, senderTransfer.Id, receiverTransfer.Id)

	res, err := wallet.ClaimTransfer(
		receiverCtx,
		receiverTransfer,
		receiverConfig,
		leavesToClaim,
	)
	require.NoError(t, err, "failed to ClaimTransfer")
	require.Equal(t, res[0].Id, claimingNode.Leaf.Id)
}

func TestTransferZeroLeaves(t *testing.T) {
	senderConfig := wallet.NewTestWalletConfig(t)
	receiverPrivKey := keys.GeneratePrivateKey()

	var leavesToTransfer []wallet.LeafKeyTweak
	_, err := wallet.SendTransferWithKeyTweaks(
		t.Context(),
		senderConfig,
		leavesToTransfer[:],
		receiverPrivKey.Public(),
		time.Now().Add(10*time.Minute),
	)
	require.Error(t, err, "expected error when transferring zero leaves")
}

func TestTransferWithSeparateSteps(t *testing.T) {
	// Sender initiates transfer
	senderConfig := wallet.NewTestWalletConfig(t)
	leafPrivKey := keys.GeneratePrivateKey()
	rootNode, err := wallet.CreateNewTree(senderConfig, faucet, leafPrivKey, amountSatsToSend)
	require.NoError(t, err, "failed to create new tree")
	newLeafPrivKey := keys.GeneratePrivateKey()
	receiverPrivKey := keys.GeneratePrivateKey()

	transferNode := wallet.LeafKeyTweak{
		Leaf:              rootNode,
		SigningPrivKey:    leafPrivKey,
		NewSigningPrivKey: newLeafPrivKey,
	}
	leavesToTransfer := [1]wallet.LeafKeyTweak{transferNode}
	senderTransfer, err := wallet.SendTransferWithKeyTweaks(
		t.Context(),
		senderConfig,
		leavesToTransfer[:],
		receiverPrivKey.Public(),
		time.Now().Add(10*time.Minute),
	)
	require.NoError(t, err, "failed to transfer tree node")

	// Receiver queries pending transfer
	receiverConfig := wallet.NewTestWalletConfigWithIdentityKey(t, receiverPrivKey)
	receiverToken, err := wallet.AuthenticateWithServer(t.Context(), receiverConfig)
	require.NoError(t, err, "failed to authenticate receiver")
	receiverCtx := wallet.ContextWithToken(t.Context(), receiverToken)
	pendingTransfer, err := wallet.QueryPendingTransfers(receiverCtx, receiverConfig)
	require.NoError(t, err, "failed to query pending transfers")
	require.Len(t, pendingTransfer.Transfers, 1)
	receiverTransfer := pendingTransfer.Transfers[0]
	require.Equal(t, senderTransfer.Id, receiverTransfer.Id)

	leafPrivKeyMap, err := wallet.VerifyPendingTransfer(t.Context(), receiverConfig, receiverTransfer)
	require.NoError(t, err)
	require.Equal(t, map[string]keys.Private{rootNode.Id: newLeafPrivKey}, leafPrivKeyMap)

	finalLeafPrivKey := keys.GeneratePrivateKey()
	claimingNode := wallet.LeafKeyTweak{
		Leaf:              receiverTransfer.Leaves[0].Leaf,
		SigningPrivKey:    newLeafPrivKey,
		NewSigningPrivKey: finalLeafPrivKey,
	}
	leavesToClaim := []wallet.LeafKeyTweak{claimingNode}

	_, err = wallet.ClaimTransferTweakKeys(
		receiverCtx,
		receiverTransfer,
		receiverConfig,
		leavesToClaim,
	)
	require.NoError(t, err, "failed to ClaimTransferTweakKeys")

	pendingTransfer, err = wallet.QueryPendingTransfers(receiverCtx, receiverConfig)
	require.NoError(t, err, "failed to query pending transfers")
	require.Len(t, pendingTransfer.Transfers, 1)
	receiverTransfer = pendingTransfer.Transfers[0]
	require.Equal(t, senderTransfer.Id, receiverTransfer.Id)

	leafPrivKeyMap, err = wallet.VerifyPendingTransfer(t.Context(), receiverConfig, receiverTransfer)
	require.NoError(t, err)
	require.Equal(t, map[string]keys.Private{rootNode.Id: newLeafPrivKey}, leafPrivKeyMap)

	_, err = wallet.ClaimTransferSignRefunds(
		receiverCtx,
		receiverTransfer,
		receiverConfig,
		leavesToClaim,
		nil,
	)
	require.NoError(t, err, "failed to ClaimTransferSignRefunds")

	pendingTransfer, err = wallet.QueryPendingTransfers(receiverCtx, receiverConfig)
	require.NoError(t, err, "failed to query pending transfers")
	require.Len(t, pendingTransfer.Transfers, 1)

	_, err = wallet.ClaimTransfer(
		receiverCtx,
		receiverTransfer,
		receiverConfig,
		leavesToClaim,
	)
	require.NoError(t, err, "failed to ClaimTransfer")
}

func TestCancelTransfer(t *testing.T) {
	// Sender initiates transfer
	senderConfig := wallet.NewTestWalletConfig(t)
	leafPrivKey := keys.GeneratePrivateKey()
	rootNode, err := wallet.CreateNewTree(senderConfig, faucet, leafPrivKey, amountSatsToSend)
	require.NoError(t, err, "failed to create new tree")

	newLeafPrivKey := keys.GeneratePrivateKey()
	receiverPrivKey := keys.GeneratePrivateKey()

	transferNode := wallet.LeafKeyTweak{
		Leaf:              rootNode,
		SigningPrivKey:    leafPrivKey,
		NewSigningPrivKey: newLeafPrivKey,
	}
	leavesToTransfer := []wallet.LeafKeyTweak{transferNode}
	expiryDelta := 2 * time.Second
	senderTransfer, _, _, err := wallet.SendTransferSignRefund(
		t.Context(),
		senderConfig,
		leavesToTransfer,
		receiverPrivKey.Public(),
		time.Now().Add(expiryDelta),
	)
	require.NoError(t, err, "failed to transfer tree node")

	// We don't need to wait for the expiry because we haven't
	// tweaked our key yet.
	_, err = wallet.CancelTransfer(t.Context(), senderConfig, senderTransfer)
	require.NoError(t, err, "failed to cancel transfer")

	for operator := range senderConfig.SigningOperators {
		senderConfig.CoordinatorIdentifier = operator
		transfers, _, err := wallet.QueryAllTransfers(t.Context(), senderConfig, 1, 0)
		require.NoError(t, err)
		require.Len(t, transfers, 1)
	}

	senderTransfer, err = wallet.SendTransferWithKeyTweaks(
		t.Context(),
		senderConfig,
		leavesToTransfer,
		receiverPrivKey.Public(),
		time.Now().Add(10*time.Minute),
	)
	require.NoError(t, err, "failed to transfer tree node")

	receiverConfig := wallet.NewTestWalletConfigWithIdentityKey(t, receiverPrivKey)
	receiverToken, err := wallet.AuthenticateWithServer(t.Context(), receiverConfig)
	require.NoError(t, err, "failed to authenticate receiver")
	receiverCtx := wallet.ContextWithToken(t.Context(), receiverToken)
	pendingTransfer, err := wallet.QueryPendingTransfers(receiverCtx, receiverConfig)
	require.NoError(t, err, "failed to query pending transfers")
	require.Len(t, pendingTransfer.Transfers, 1)
	receiverTransfer := pendingTransfer.Transfers[0]
	require.Equal(t, senderTransfer.Id, receiverTransfer.Id)

	leafPrivKeyMap, err := wallet.VerifyPendingTransfer(t.Context(), receiverConfig, receiverTransfer)
	require.NoError(t, err)
	require.Equal(t, map[string]keys.Private{rootNode.Id: newLeafPrivKey}, leafPrivKeyMap)

	finalLeafPrivKey := keys.GeneratePrivateKey()
	claimingNode := wallet.LeafKeyTweak{
		Leaf:              receiverTransfer.Leaves[0].Leaf,
		SigningPrivKey:    newLeafPrivKey,
		NewSigningPrivKey: finalLeafPrivKey,
	}
	_, err = wallet.ClaimTransfer(
		receiverCtx,
		receiverTransfer,
		receiverConfig,
		[]wallet.LeafKeyTweak{claimingNode},
	)
	require.NoError(t, err, "failed to ClaimTransfer")
}

func TestCancelTransferAfterTweak(t *testing.T) {
	// Sender initiates transfer
	senderConfig := wallet.NewTestWalletConfig(t)
	leafPrivKey := keys.GeneratePrivateKey()
	rootNode, err := wallet.CreateNewTree(senderConfig, faucet, leafPrivKey, amountSatsToSend)
	require.NoError(t, err, "failed to create new tree")

	newLeafPrivKey := keys.GeneratePrivateKey()
	receiverPrivKey := keys.GeneratePrivateKey()

	transferNode := wallet.LeafKeyTweak{
		Leaf:              rootNode,
		SigningPrivKey:    leafPrivKey,
		NewSigningPrivKey: newLeafPrivKey,
	}
	leavesToTransfer := []wallet.LeafKeyTweak{transferNode}
	expiryDuration := 1 * time.Second
	senderTransfer, err := wallet.SendTransferWithKeyTweaks(
		t.Context(),
		senderConfig,
		leavesToTransfer,
		receiverPrivKey.Public(),
		time.Now().Add(expiryDuration),
	)
	require.NoError(t, err, "failed to transfer tree node")

	// Make sure transfers can't be cancelled after key tweak even after
	// expiration
	time.Sleep(expiryDuration)

	_, err = wallet.CancelTransfer(t.Context(), senderConfig, senderTransfer)
	require.Error(t, err, "expected to fail but didn't")
}

func TestQueryTransfers(t *testing.T) {
	// Initiate sender
	senderConfig := wallet.NewTestWalletConfig(t)
	senderLeafPrivKey := keys.GeneratePrivateKey()
	senderRootNode, err := wallet.CreateNewTree(senderConfig, faucet, senderLeafPrivKey, amountSatsToSend)
	require.NoError(t, err, "failed to create new tree")

	// Initiate receiver
	receiverConfig := wallet.NewTestWalletConfig(t)
	receiverLeafPrivKey := keys.GeneratePrivateKey()
	receiverRootNode, err := wallet.CreateNewTree(receiverConfig, faucet, receiverLeafPrivKey, amountSatsToSend)
	require.NoError(t, err, "failed to create new tree")

	// Sender initiates transfer
	senderNewLeafPrivKey := keys.GeneratePrivateKey()

	senderTransferNode := wallet.LeafKeyTweak{
		Leaf:              senderRootNode,
		SigningPrivKey:    senderLeafPrivKey,
		NewSigningPrivKey: senderNewLeafPrivKey,
	}
	senderLeavesToTransfer := []wallet.LeafKeyTweak{senderTransferNode}

	// Get signature for refunds (normal flow)
	senderTransfer, senderRefundSignatureMap, leafDataMap, err := wallet.SendTransferSignRefund(
		t.Context(),
		senderConfig,
		senderLeavesToTransfer,
		receiverConfig.IdentityPublicKey(),
		time.Now().Add(10*time.Minute),
	)
	require.NoError(t, err)
	assert.Len(t, senderRefundSignatureMap, 1)
	signature := senderRefundSignatureMap[senderRootNode.Id]
	assert.NotNil(t, signature, "expected refund signature for root node")
	leafData := leafDataMap[senderRootNode.Id]
	require.NotNil(t, leafData, "expected leaf data for root node")
	require.NotNil(t, leafData.RefundTx, "expected refund tx")
	require.NotNil(t, leafData.Tx, "expected tx")
	require.NotNil(t, leafData.Tx.TxOut, "expected tx out")
	require.NotNil(t, leafData.Vout, "expected Vout")

	sighash, err := common.SigHashFromTx(leafData.RefundTx, 0, leafData.Tx.TxOut[leafData.Vout])
	require.NoError(t, err)

	// Create adaptor from that signature
	adaptorAddedSignature, adaptorPrivKeyBytes, err := common.GenerateAdaptorFromSignature(signature)
	require.NoError(t, err)
	adaptorPrivKey, err := keys.ParsePrivateKey(adaptorPrivKeyBytes)
	require.NoError(t, err)

	// Alice sends adaptor and signature to Bob, Bob validates the adaptor
	nodeVerifyingPubKey, err := keys.ParsePublicKey(senderRootNode.VerifyingPublicKey)
	require.NoError(t, err)
	taprootKey := txscript.ComputeTaprootKeyNoScript(nodeVerifyingPubKey.ToBTCEC())
	err = common.ValidateAdaptorSignature(taprootKey, sighash, adaptorAddedSignature, adaptorPrivKey.Public().Serialize())
	require.NoError(t, err)

	// Bob signs refunds with adaptor
	receiverNewLeafPrivKey := keys.GeneratePrivateKey()

	receiverTransferNode := wallet.LeafKeyTweak{
		Leaf:              receiverRootNode,
		SigningPrivKey:    receiverLeafPrivKey,
		NewSigningPrivKey: receiverNewLeafPrivKey,
	}
	receiverLeavesToTransfer := [1]wallet.LeafKeyTweak{receiverTransferNode}
	receiverTransfer, receiverRefundSignatureMap, leafDataMap, operatorSigningResults, err := wallet.CounterSwapSignRefund(
		t.Context(),
		receiverConfig,
		receiverLeavesToTransfer[:],
		senderConfig.IdentityPublicKey(),
		time.Now().Add(10*time.Minute),
		adaptorPrivKey.Public(),
	)
	require.NoError(t, err)

	// Alice verifies Bob's signatures
	receiverSighash, err := common.SigHashFromTx(leafDataMap[receiverLeavesToTransfer[0].Leaf.Id].RefundTx, 0, leafDataMap[receiverLeavesToTransfer[0].Leaf.Id].Tx.TxOut[leafDataMap[receiverLeavesToTransfer[0].Leaf.Id].Vout])
	require.NoError(t, err)

	receiverKey, err := keys.ParsePublicKey(receiverLeavesToTransfer[0].Leaf.VerifyingPublicKey)
	require.NoError(t, err)
	receiverTaprootKey := txscript.ComputeTaprootKeyNoScript(receiverKey.ToBTCEC())

	_, err = common.ApplyAdaptorToSignature(receiverTaprootKey, receiverSighash, receiverRefundSignatureMap[receiverLeavesToTransfer[0].Leaf.Id], adaptorPrivKeyBytes)
	require.NoError(t, err)

	// Alice reveals adaptor secret to Bob, Bob combines with existing adaptor signatures to get valid signatures
	newReceiverRefundSignatureMap := make(map[string][]byte)
	for nodeID, signature := range receiverRefundSignatureMap {
		leafData := leafDataMap[nodeID]
		sighash, _ := common.SigHashFromTx(leafData.RefundTx, 0, leafData.Tx.TxOut[leafData.Vout])
		var verifyingPubkey keys.Public
		for _, signingResult := range operatorSigningResults {
			if signingResult.LeafId == nodeID {
				verifyingPubkey, err = keys.ParsePublicKey(signingResult.VerifyingKey)
				require.NoError(t, err)
			}
		}
		assert.NotNil(t, verifyingPubkey, "expected signing result for leaf %s", nodeID)
		taprootKey := txscript.ComputeTaprootKeyNoScript(verifyingPubkey.ToBTCEC())
		adaptorSig, err := common.ApplyAdaptorToSignature(taprootKey, sighash, signature, adaptorPrivKeyBytes)
		require.NoError(t, err)
		newReceiverRefundSignatureMap[nodeID] = adaptorSig
	}

	// Alice provides key tweak, Bob claims alice's leaves
	senderTransfer, err = wallet.DeliverTransferPackage(
		t.Context(),
		senderConfig,
		senderTransfer,
		senderLeavesToTransfer[:],
		senderRefundSignatureMap,
	)
	require.NoError(t, err, "failed to send transfer tweak key")

	receiverToken, err := wallet.AuthenticateWithServer(t.Context(), receiverConfig)
	require.NoError(t, err, "failed to authenticate receiver")
	receiverCtx := wallet.ContextWithToken(t.Context(), receiverToken)
	pendingTransfer, err := wallet.QueryPendingTransfers(receiverCtx, receiverConfig)
	require.NoError(t, err, "failed to query pending transfers")
	require.Len(t, pendingTransfer.Transfers, 1)
	receiverPendingTransfer := pendingTransfer.Transfers[0]
	require.Equal(t, senderTransfer.Id, receiverPendingTransfer.Id)

	leafPrivKeyMap, err := wallet.VerifyPendingTransfer(t.Context(), receiverConfig, receiverPendingTransfer)
	require.NoError(t, err)
	require.Equal(t, map[string]keys.Private{senderRootNode.Id: senderNewLeafPrivKey}, leafPrivKeyMap)

	finalLeafPrivKey := keys.GeneratePrivateKey()
	claimingNode := wallet.LeafKeyTweak{
		Leaf:              receiverPendingTransfer.Leaves[0].Leaf,
		SigningPrivKey:    senderNewLeafPrivKey,
		NewSigningPrivKey: finalLeafPrivKey,
	}
	_, err = wallet.ClaimTransfer(
		receiverCtx,
		receiverPendingTransfer,
		receiverConfig,
		[]wallet.LeafKeyTweak{claimingNode},
	)
	require.NoError(t, err, "failed to ClaimTransfer")

	// Bob provides key tweak, Alice claims bob's leaves
	_, err = wallet.DeliverTransferPackage(
		t.Context(),
		receiverConfig,
		receiverTransfer,
		receiverLeavesToTransfer[:],
		newReceiverRefundSignatureMap,
	)
	require.NoError(t, err, "failed to send transfer tweak key")

	senderToken, err := wallet.AuthenticateWithServer(t.Context(), senderConfig)
	require.NoError(t, err, "failed to authenticate sender")
	senderCtx := wallet.ContextWithToken(t.Context(), senderToken)
	pendingTransfer, err = wallet.QueryPendingTransfers(senderCtx, senderConfig)
	require.NoError(t, err, "failed to query pending transfers")
	require.Len(t, pendingTransfer.Transfers, 1)
	senderPendingTransfer := pendingTransfer.Transfers[0]
	require.Equal(t, senderTransfer.Id, receiverPendingTransfer.Id)

	leafPrivKeyMap, err = wallet.VerifyPendingTransfer(t.Context(), senderConfig, senderPendingTransfer)
	require.NoError(t, err)
	require.Equal(t, map[string]keys.Private{receiverRootNode.Id: receiverNewLeafPrivKey}, leafPrivKeyMap)

	finalLeafPrivKey = keys.GeneratePrivateKey()
	claimingNode = wallet.LeafKeyTweak{
		Leaf:              senderPendingTransfer.Leaves[0].Leaf,
		SigningPrivKey:    receiverNewLeafPrivKey,
		NewSigningPrivKey: finalLeafPrivKey,
	}
	_, err = wallet.ClaimTransfer(
		senderCtx,
		senderPendingTransfer,
		senderConfig,
		[]wallet.LeafKeyTweak{claimingNode},
	)
	require.NoError(t, err, "failed to ClaimTransfer")

	transfers, offset, err := wallet.QueryAllTransfers(t.Context(), senderConfig, 1, 0)
	require.NoError(t, err, "failed to QueryAllTransfers")
	require.Len(t, transfers, 1)
	require.EqualValues(t, 1, offset)

	transfers, offset, err = wallet.QueryAllTransfers(t.Context(), senderConfig, 1, offset)
	require.NoError(t, err, "failed to QueryAllTransfers")
	require.Len(t, transfers, 1)
	require.EqualValues(t, 2, offset)

	transfers, _, err = wallet.QueryAllTransfers(t.Context(), senderConfig, 100, 0)
	require.NoError(t, err, "failed to QueryAllTransfers")
	require.Len(t, transfers, 2)

	typeCounts := make(map[pb.TransferType]int)
	for _, transfer := range transfers {
		typeCounts[transfer.Type]++
	}
	assert.Equal(t, 1, typeCounts[pb.TransferType_TRANSFER], "expected 1 transfer")
	assert.Equal(t, 1, typeCounts[pb.TransferType_COUNTER_SWAP], "expected 1 counter swap transfer")

	transfers, _, err = wallet.QueryAllTransfersWithTypes(t.Context(), senderConfig, 2, 0, []pb.TransferType{pb.TransferType_TRANSFER})
	require.NoError(t, err)
	assert.Len(t, transfers, 1)

	transfers, _, err = wallet.QueryAllTransfersWithTypes(t.Context(), senderConfig, 2, 0, []pb.TransferType{pb.TransferType_COUNTER_SWAP})
	require.NoError(t, err)
	assert.Len(t, transfers, 1)

	transfers, _, err = wallet.QueryAllTransfersWithTypes(t.Context(), senderConfig, 3, 0, []pb.TransferType{pb.TransferType_TRANSFER, pb.TransferType_COUNTER_SWAP})
	require.NoError(t, err)
	assert.Len(t, transfers, 2)
}

func TestDoubleClaimTransfer(t *testing.T) {
	// Sender initiates transfer
	senderConfig := wallet.NewTestWalletConfig(t)
	leafPrivKey := keys.GeneratePrivateKey()
	rootNode, err := wallet.CreateNewTree(senderConfig, faucet, leafPrivKey, amountSatsToSend)
	require.NoError(t, err, "failed to create new tree")

	newLeafPrivKey := keys.GeneratePrivateKey()
	receiverPrivKey := keys.GeneratePrivateKey()

	transferNode := wallet.LeafKeyTweak{
		Leaf:              rootNode,
		SigningPrivKey:    leafPrivKey,
		NewSigningPrivKey: newLeafPrivKey,
	}
	leavesToTransfer := []wallet.LeafKeyTweak{transferNode}
	senderTransfer, err := wallet.SendTransferWithKeyTweaks(
		t.Context(),
		senderConfig,
		leavesToTransfer,
		receiverPrivKey.Public(),
		time.Now().Add(10*time.Minute),
	)
	require.NoError(t, err, "failed to transfer tree node")

	// Receiver queries pending transfer
	receiverConfig := wallet.NewTestWalletConfigWithIdentityKey(t, receiverPrivKey)
	receiverToken, err := wallet.AuthenticateWithServer(t.Context(), receiverConfig)
	require.NoError(t, err, "failed to authenticate receiver")
	receiverCtx := wallet.ContextWithToken(t.Context(), receiverToken)
	pendingTransfer, err := wallet.QueryPendingTransfers(receiverCtx, receiverConfig)
	require.NoError(t, err, "failed to query pending transfers")
	require.Len(t, pendingTransfer.Transfers, 1)
	receiverTransfer := pendingTransfer.Transfers[0]
	require.Equal(t, senderTransfer.Id, receiverTransfer.Id)

	leafPrivKeyMap, err := wallet.VerifyPendingTransfer(t.Context(), receiverConfig, receiverTransfer)
	require.NoError(t, err)
	require.Equal(t, map[string]keys.Private{rootNode.Id: newLeafPrivKey}, leafPrivKeyMap)

	finalLeafPrivKey := keys.GeneratePrivateKey()
	claimingNode := wallet.LeafKeyTweak{
		Leaf:              receiverTransfer.Leaves[0].Leaf,
		SigningPrivKey:    newLeafPrivKey,
		NewSigningPrivKey: finalLeafPrivKey,
	}
	leavesToClaim := []wallet.LeafKeyTweak{claimingNode}

	errCount := 0
	wg := sync.WaitGroup{}
	for range 5 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err = wallet.ClaimTransfer(receiverCtx, receiverTransfer, receiverConfig, leavesToClaim)
			if err != nil {
				errCount++
			}
		}()
	}
	wg.Wait()

	if errCount == 5 {
		pendingTransfer, err = wallet.QueryPendingTransfers(receiverCtx, receiverConfig)
		require.NoError(t, err, "failed to query pending transfers")
		require.Len(t, pendingTransfer.Transfers, 1)
		receiverTransfer = pendingTransfer.Transfers[0]
		require.Equal(t, senderTransfer.Id, receiverTransfer.Id)

		res, err := wallet.ClaimTransfer(
			receiverCtx,
			receiverTransfer,
			receiverConfig,
			leavesToClaim,
		)
		if err != nil {
			// if the claim failed, the transfer should revert back to sender key tweaked status
			pendingTransfer, err = wallet.QueryPendingTransfers(receiverCtx, receiverConfig)
			require.NoError(t, err, "failed to query pending transfers")
			require.Len(t, pendingTransfer.Transfers, 1)
			receiverTransfer = pendingTransfer.Transfers[0]
			require.Equal(t, senderTransfer.Id, receiverTransfer.Id)

			res, err = wallet.ClaimTransfer(
				receiverCtx,
				receiverTransfer,
				receiverConfig,
				leavesToClaim,
			)
			require.NoError(t, err, "failed to ClaimTransfer")
		}

		require.Equal(t, res[0].Id, claimingNode.Leaf.Id)
	}
}

func TestValidSparkInvoiceTransfer(t *testing.T) {
	rng := rand.NewChaCha8(deterministicSeedFromTestName(t.Name()))
	invoiceUUID, err := uuid.NewV7FromReader(rng)
	require.NoError(t, err)
	amountToSend := uint64(amountSatsToSend)
	memoString := "test memo"
	senderPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	senderPublicKey := senderPrivKey.Public()
	receiverPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	receiverPublicKey := receiverPrivKey.Public()
	tenMinutesFromNow := time.Now().Add(10 * time.Minute)
	network := common.Regtest

	amountSats := &amountToSend
	expiryTime := &tenMinutesFromNow
	memo := &memoString

	invoiceFields := common.CreateSatsSparkInvoiceFields(
		invoiceUUID[:],
		amountSats,
		memo,
		senderPublicKey,
		expiryTime,
	)

	invoiceHash, err := common.HashSparkInvoiceFields(invoiceFields, network, receiverPublicKey)
	require.NoError(t, err)
	sig, err := schnorr.Sign(receiverPrivKey.ToBTCEC(), invoiceHash)
	require.NoError(t, err)
	sigBytes := sig.Serialize()

	invoice, err := common.EncodeSparkAddressWithSignature(
		receiverPublicKey.Serialize(),
		network,
		invoiceFields,
		sigBytes,
	)
	require.NoError(t, err)

	// Should succeed on first attempt.
	testTransferWithInvoice(t, invoice, senderPrivKey, receiverPrivKey)

	// Single Use Invoice.
	// Should fail on second attempt.
	_, _, _, err = sendTransferWithInvoice(t, invoice, senderPrivKey, receiverPrivKey)
	require.Error(t, err)
}

func TestValidSparkInvoiceTransferEmptySenderPublicKey(t *testing.T) {
	rng := rand.NewChaCha8(deterministicSeedFromTestName(t.Name()))
	invoiceUUID, err := uuid.NewV7FromReader(rng)
	require.NoError(t, err)
	amountSats := uint64(amountSatsToSend)
	memo := "test memo"
	receiverPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	receiverPublicKey := receiverPrivKey.Public()
	senderPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	tenMinutesFromNow := time.Now().Add(10 * time.Minute)
	network := common.Regtest

	emptySenderPublicKey := keys.Public{}
	invoiceFields := common.CreateSatsSparkInvoiceFields(
		invoiceUUID[:],
		&amountSats,
		&memo,
		emptySenderPublicKey,
		&tenMinutesFromNow,
	)

	invoiceHash, err := common.HashSparkInvoiceFields(invoiceFields, network, receiverPublicKey)
	require.NoError(t, err)
	sig, err := schnorr.Sign(receiverPrivKey.ToBTCEC(), invoiceHash)
	require.NoError(t, err)
	sigBytes := sig.Serialize()

	invoice, err := common.EncodeSparkAddressWithSignature(
		receiverPublicKey.Serialize(),
		network,
		invoiceFields,
		sigBytes,
	)
	require.NoError(t, err)

	// Should succeed on first attempt.
	testTransferWithInvoice(t, invoice, senderPrivKey, receiverPrivKey)

	// Single Use Invoice.
	// Should fail on second attempt.
	_, _, _, err = sendTransferWithInvoice(t, invoice, senderPrivKey, receiverPrivKey)
	require.Error(t, err)
}

func TestValidSparkInvoiceTransferEmptyExpiry(t *testing.T) {
	rng := rand.NewChaCha8(deterministicSeedFromTestName(t.Name()))
	invoiceUUID, err := uuid.NewV7FromReader(rng)
	require.NoError(t, err)
	amountSats := uint64(amountSatsToSend)
	memo := "test memo"
	receiverPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	receiverPublicKey := receiverPrivKey.Public()
	senderPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	senderPublicKey := senderPrivKey.Public()
	network := common.Regtest

	invoiceFields := common.CreateSatsSparkInvoiceFields(
		invoiceUUID[:],
		&amountSats,
		&memo,
		senderPublicKey,
		nil,
	)

	invoiceHash, err := common.HashSparkInvoiceFields(invoiceFields, network, receiverPublicKey)
	require.NoError(t, err)
	sig, err := schnorr.Sign(receiverPrivKey.ToBTCEC(), invoiceHash)
	require.NoError(t, err)
	sigBytes := sig.Serialize()

	invoice, err := common.EncodeSparkAddressWithSignature(
		receiverPublicKey.Serialize(),
		network,
		invoiceFields,
		sigBytes,
	)
	require.NoError(t, err)

	// Should succeed on first attempt.
	testTransferWithInvoice(t, invoice, senderPrivKey, receiverPrivKey)

	// Single Use Invoice.
	// Should fail on second attempt.
	_, _, _, err = sendTransferWithInvoice(t, invoice, senderPrivKey, receiverPrivKey)
	require.Error(t, err)
}

func TestValidSparkInvoiceTransferEmptyMemo(t *testing.T) {
	rng := rand.NewChaCha8(deterministicSeedFromTestName(t.Name()))
	invoiceUUID, err := uuid.NewV7FromReader(rng)
	require.NoError(t, err)
	amountSats := uint64(amountSatsToSend)
	receiverPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	receiverPublicKey := receiverPrivKey.Public()
	senderPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	senderPublicKey := senderPrivKey.Public()
	network := common.Regtest
	tenMinutesFromNow := time.Now().Add(10 * time.Minute)

	invoiceFields := common.CreateSatsSparkInvoiceFields(
		invoiceUUID[:],
		&amountSats,
		nil,
		senderPublicKey,
		&tenMinutesFromNow,
	)

	invoiceHash, err := common.HashSparkInvoiceFields(invoiceFields, network, receiverPublicKey)
	require.NoError(t, err)
	sig, err := schnorr.Sign(receiverPrivKey.ToBTCEC(), invoiceHash)
	require.NoError(t, err)
	sigBytes := sig.Serialize()

	invoice, err := common.EncodeSparkAddressWithSignature(
		receiverPublicKey.Serialize(),
		network,
		invoiceFields,
		sigBytes,
	)
	require.NoError(t, err)

	// Should succeed on first attempt.
	testTransferWithInvoice(t, invoice, senderPrivKey, receiverPrivKey)

	// Single Use Invoice.
	// Should fail on second attempt.
	_, _, _, err = sendTransferWithInvoice(t, invoice, senderPrivKey, receiverPrivKey)
	require.Error(t, err)
}

func TestValidSparkInvoiceTransferEmptyAmount(t *testing.T) {
	rng := rand.NewChaCha8(deterministicSeedFromTestName(t.Name()))
	invoiceUUID, err := uuid.NewV7FromReader(rng)
	require.NoError(t, err)
	receiverPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	receiverPublicKey := receiverPrivKey.Public()
	memoString := "test memo"
	senderPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	senderPublicKey := senderPrivKey.Public()
	network := common.Regtest
	tenMinutesFromNow := time.Now().Add(10 * time.Minute)

	invoiceFields := common.CreateSatsSparkInvoiceFields(
		invoiceUUID[:],
		nil,
		&memoString,
		senderPublicKey,
		&tenMinutesFromNow,
	)

	invoiceHash, err := common.HashSparkInvoiceFields(invoiceFields, network, receiverPublicKey)
	require.NoError(t, err)
	sig, err := schnorr.Sign(receiverPrivKey.ToBTCEC(), invoiceHash)
	require.NoError(t, err)
	sigBytes := sig.Serialize()

	invoice, err := common.EncodeSparkAddressWithSignature(
		receiverPublicKey.Serialize(),
		network,
		invoiceFields,
		sigBytes,
	)
	require.NoError(t, err)

	// Should succeed on first attempt.
	testTransferWithInvoice(t, invoice, senderPrivKey, receiverPrivKey)

	// Single Use Invoice.
	// Should fail on second attempt.
	_, _, _, err = sendTransferWithInvoice(t, invoice, senderPrivKey, receiverPrivKey)
	require.Error(t, err)
}

func TestValidSparkInvoiceTransferEmptySignature(t *testing.T) {
	rng := rand.NewChaCha8(deterministicSeedFromTestName(t.Name()))
	invoiceUUID, err := uuid.NewV7FromReader(rng)
	require.NoError(t, err)
	receiverPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	receiverPublicKey := receiverPrivKey.Public()
	memoString := "test memo"
	senderPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	senderPublicKey := senderPrivKey.Public()
	network := common.Regtest
	tenMinutesFromNow := time.Now().Add(10 * time.Minute)

	invoiceFields := common.CreateSatsSparkInvoiceFields(
		invoiceUUID[:],
		nil,
		&memoString,
		senderPublicKey,
		&tenMinutesFromNow,
	)

	invoice, err := common.EncodeSparkAddressWithSignature(
		receiverPublicKey.Serialize(),
		network,
		invoiceFields,
		nil,
	)
	require.NoError(t, err)

	// Should succeed on first attempt.
	testTransferWithInvoice(t, invoice, senderPrivKey, receiverPrivKey)

	// Single Use Invoice.
	// Should fail on second attempt.
	_, _, _, err = sendTransferWithInvoice(t, invoice, senderPrivKey, receiverPrivKey)
	require.Error(t, err)
}

func TestNonCanonicalInvoiceShouldError(t *testing.T) {
	nonCanonicalInvoice := "sprt1pgssx2ndesmr2cm86s6ylgsx7rqed58p5l4skcw69e2kzqqxgg79j2fszgsqsqgjzqqe364u4mehdy9wur7lc64al4sjypqg5zxsv2syw3jhxaq6gpanrus3aq8sy6c27zj008mjas6x7akw2pt7expuhmsnpmxrakjmrjeep56gqehrh6gwvq9g9nlcy2587n2m9kehdq446t483nnyar5rgasyvl"
	rng := rand.NewChaCha8(deterministicSeedFromTestName(t.Name()))
	decoded, err := common.DecodeSparkAddress(nonCanonicalInvoice)
	require.NoError(t, err)
	reEncoded, err := common.EncodeSparkAddressWithSignature(
		decoded.SparkAddress.IdentityPublicKey,
		decoded.Network,
		decoded.SparkAddress.SparkInvoiceFields,
		decoded.SparkAddress.Signature,
	)
	require.NoError(t, err)
	require.NotEqual(t, nonCanonicalInvoice, reEncoded)
	senderPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	receiverPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)

	_, _, _, err = sendTransferWithInvoice(t, nonCanonicalInvoice, senderPrivKey, receiverPrivKey)
	require.Error(t, err)
}

func TestInvalidSparkInvoiceTransferShouldErrorWithMismatchedSender(t *testing.T) {
	rng := rand.NewChaCha8(deterministicSeedFromTestName(t.Name()))
	invoiceUUID, err := uuid.NewV7FromReader(rng)
	require.NoError(t, err)
	amountToSend := uint64(amountSatsToSend)
	amountSats := &amountToSend
	memo := "test memo"
	senderPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	receiverPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	receiverPublicKey := receiverPrivKey.Public()
	expiryTime := time.Now().Add(10 * time.Minute)
	network := common.Regtest

	mismatchedSender := keys.MustGeneratePrivateKeyFromRand(rng)
	invoiceFields := common.CreateSatsSparkInvoiceFields(
		invoiceUUID[:],
		amountSats,
		&memo,
		mismatchedSender.Public(),
		&expiryTime,
	)

	invoiceHash, err := common.HashSparkInvoiceFields(invoiceFields, network, receiverPublicKey)
	require.NoError(t, err)
	sig, err := schnorr.Sign(receiverPrivKey.ToBTCEC(), invoiceHash)
	require.NoError(t, err)

	invoice, err := common.EncodeSparkAddressWithSignature(
		receiverPublicKey.Serialize(),
		network,
		invoiceFields,
		sig.Serialize(),
	)
	require.NoError(t, err)

	_, _, _, err = sendTransferWithInvoice(t, invoice, senderPrivKey, receiverPrivKey)
	require.Error(t, err)
}

func TestInvalidSparkInvoiceTransferShouldErrorWithMismatchedReceiver(t *testing.T) {
	rng := rand.NewChaCha8(deterministicSeedFromTestName(t.Name()))
	invoiceUUID, err := uuid.NewV7FromReader(rng)
	require.NoError(t, err)
	amountToSend := uint64(amountSatsToSend)
	amountSats := &amountToSend
	memo := "test memo"
	senderPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	senderPublicKey := senderPrivKey.Public()
	receiverPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	receiverPublicKey := receiverPrivKey.Public()
	expiryTime := time.Now().Add(10 * time.Minute)
	network := common.Regtest

	invoiceFields := common.CreateSatsSparkInvoiceFields(
		invoiceUUID[:],
		amountSats,
		&memo,
		senderPublicKey,
		&expiryTime,
	)

	invoiceHash, err := common.HashSparkInvoiceFields(invoiceFields, network, receiverPublicKey)
	require.NoError(t, err)
	sig, err := schnorr.Sign(receiverPrivKey.ToBTCEC(), invoiceHash)
	require.NoError(t, err)

	mismatchedReceiver := keys.MustGeneratePrivateKeyFromRand(rng)
	invoice, err := common.EncodeSparkAddressWithSignature(
		mismatchedReceiver.Public().Serialize(),
		network,
		invoiceFields,
		sig.Serialize(),
	)
	require.NoError(t, err)

	_, _, _, err = sendTransferWithInvoice(t, invoice, senderPrivKey, receiverPrivKey)
	require.Error(t, err)
}

func TestInvalidSparkInvoiceTransferShouldErrorWithInvoiceAmountLessThanSentAmount(t *testing.T) {
	rng := rand.NewChaCha8(deterministicSeedFromTestName(t.Name()))
	invoiceUUID, err := uuid.NewV7FromReader(rng)
	require.NoError(t, err)
	memo := "test memo"
	senderPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	senderPublicKey := senderPrivKey.Public()
	receiverPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	receiverPublicKey := receiverPrivKey.Public()
	expiryTime := time.Now().Add(10 * time.Minute)
	network := common.Regtest

	lessThanSentAmount := uint64(amountSatsToSend - 1)

	invoiceFields := common.CreateSatsSparkInvoiceFields(
		invoiceUUID[:],
		&lessThanSentAmount,
		&memo,
		senderPublicKey,
		&expiryTime,
	)

	invoiceHash, err := common.HashSparkInvoiceFields(invoiceFields, network, receiverPublicKey)
	require.NoError(t, err)
	sig, err := schnorr.Sign(receiverPrivKey.ToBTCEC(), invoiceHash)
	require.NoError(t, err)

	invoice, err := common.EncodeSparkAddressWithSignature(
		receiverPublicKey.Serialize(),
		network,
		invoiceFields,
		sig.Serialize(),
	)
	require.NoError(t, err)

	_, _, _, err = sendTransferWithInvoice(t, invoice, senderPrivKey, receiverPrivKey)
	require.Error(t, err)
}

func TestInvalidSparkInvoiceTransferShouldErrorWithInvoiceAmountGreaterThanSentAmount(t *testing.T) {
	rng := rand.NewChaCha8(deterministicSeedFromTestName(t.Name()))
	invoiceUUID, err := uuid.NewV7FromReader(rng)
	require.NoError(t, err)
	memo := "test memo"
	senderPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	senderPublicKey := senderPrivKey.Public()
	receiverPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	receiverPublicKey := receiverPrivKey.Public()
	expiryTime := time.Now().Add(10 * time.Minute)
	network := common.Regtest

	greaterThanSentAmount := uint64(amountSatsToSend + 1)

	invoiceFields := common.CreateSatsSparkInvoiceFields(
		invoiceUUID[:],
		&greaterThanSentAmount,
		&memo,
		senderPublicKey,
		&expiryTime,
	)

	invoiceHash, err := common.HashSparkInvoiceFields(invoiceFields, network, receiverPublicKey)
	require.NoError(t, err)
	sig, err := schnorr.Sign(receiverPrivKey.ToBTCEC(), invoiceHash)
	require.NoError(t, err)

	invoice, err := common.EncodeSparkAddressWithSignature(
		receiverPublicKey.Serialize(),
		network,
		invoiceFields,
		sig.Serialize(),
	)
	require.NoError(t, err)

	_, _, _, err = sendTransferWithInvoice(t, invoice, senderPrivKey, receiverPrivKey)
	require.Error(t, err)
}

func TestInvalidSparkInvoiceTransferShouldErrorWithExpiredInvoice(t *testing.T) {
	rng := rand.NewChaCha8(deterministicSeedFromTestName(t.Name()))
	invoiceUUID, err := uuid.NewV7FromReader(rng)
	require.NoError(t, err)
	amountToSend := uint64(amountSatsToSend)
	amountSats := &amountToSend
	memo := "test memo"
	senderPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	senderPublicKey := senderPrivKey.Public()
	receiverPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	receiverPublicKey := receiverPrivKey.Public()
	network := common.Regtest

	expiryInThePast := time.Now().Add(-10 * time.Minute)
	invoiceFields := common.CreateSatsSparkInvoiceFields(
		invoiceUUID[:],
		amountSats,
		&memo,
		senderPublicKey,
		&expiryInThePast,
	)

	invoiceHash, err := common.HashSparkInvoiceFields(invoiceFields, network, receiverPublicKey)
	require.NoError(t, err)
	sig, err := schnorr.Sign(receiverPrivKey.ToBTCEC(), invoiceHash)
	require.NoError(t, err)

	invoice, err := common.EncodeSparkAddressWithSignature(
		receiverPublicKey.Serialize(),
		network,
		invoiceFields,
		sig.Serialize(),
	)
	require.NoError(t, err)

	_, _, _, err = sendTransferWithInvoice(t, invoice, senderPrivKey, receiverPrivKey)
	require.Error(t, err)
}

func TestInvalidSparkInvoiceTransferShouldErrorWithInvalidSignature(t *testing.T) {
	rng := rand.NewChaCha8(deterministicSeedFromTestName(t.Name()))
	invoiceUUID, err := uuid.NewV7FromReader(rng)
	require.NoError(t, err)
	amountToSend := uint64(amountSatsToSend)
	amountSats := &amountToSend
	memo := "test memo"
	senderPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	senderPublicKey := senderPrivKey.Public()
	receiverPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	receiverPublicKey := receiverPrivKey.Public()
	expiryTime := time.Now().Add(10 * time.Minute)
	network := common.Regtest

	invoiceFields := common.CreateSatsSparkInvoiceFields(
		invoiceUUID[:],
		amountSats,
		&memo,
		senderPublicKey,
		&expiryTime,
	)

	invoiceHash, err := common.HashSparkInvoiceFields(invoiceFields, network, receiverPublicKey)
	require.NoError(t, err)
	// Sign with sender instead of receiver private key.
	sig, err := schnorr.Sign(senderPrivKey.ToBTCEC(), invoiceHash)
	require.NoError(t, err)

	invoice, err := common.EncodeSparkAddressWithSignature(
		receiverPublicKey.Serialize(),
		network,
		invoiceFields,
		sig.Serialize(),
	)
	require.NoError(t, err)

	_, _, _, err = sendTransferWithInvoice(t, invoice, senderPrivKey, receiverPrivKey)
	require.Error(t, err)
}

func TestInvalidSparkInvoiceTransferShouldErrorWithMismatchedNetwork(t *testing.T) {
	rng := rand.NewChaCha8(deterministicSeedFromTestName(t.Name()))
	invoiceUUID, err := uuid.NewV7FromReader(rng)
	require.NoError(t, err)
	amountToSend := uint64(amountSatsToSend)
	amountSats := &amountToSend
	memo := "test memo"
	senderPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	senderPublicKey := senderPrivKey.Public()
	receiverPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	receiverPublicKey := receiverPrivKey.Public()
	expiryTime := time.Now().Add(10 * time.Minute)
	mismatchedNetwork := common.Mainnet

	invoiceFields := common.CreateSatsSparkInvoiceFields(
		invoiceUUID[:],
		amountSats,
		&memo,
		senderPublicKey,
		&expiryTime,
	)

	invoiceHash, err := common.HashSparkInvoiceFields(invoiceFields, mismatchedNetwork, receiverPublicKey)
	require.NoError(t, err)
	sig, err := schnorr.Sign(receiverPrivKey.ToBTCEC(), invoiceHash)
	require.NoError(t, err)

	invoice, err := common.EncodeSparkAddressWithSignature(
		receiverPublicKey.Serialize(),
		mismatchedNetwork,
		invoiceFields,
		sig.Serialize(),
	)
	require.NoError(t, err)

	_, _, _, err = sendTransferWithInvoice(t, invoice, senderPrivKey, receiverPrivKey)
	require.Error(t, err)
}

func TestInvalidSparkInvoiceTransferShouldErrorWithTokensInvoice(t *testing.T) {
	rng := rand.NewChaCha8(deterministicSeedFromTestName(t.Name()))
	invoiceUUID, err := uuid.NewV7FromReader(rng)
	require.NoError(t, err)
	amountToSend := uint64(amountSatsToSend)
	amountSats := &amountToSend
	memo := "test memo"
	senderPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	senderPublicKey := senderPrivKey.Public()
	receiverPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	receiverPublicKey := receiverPrivKey.Public()
	expiryTime := time.Now().Add(10 * time.Minute)
	network := common.Regtest

	amountBytes := new(big.Int).SetUint64(*amountSats).Bytes()
	invoiceFields := common.CreateTokenSparkInvoiceFields(
		invoiceUUID[:],
		[]byte{},
		amountBytes,
		&memo,
		senderPublicKey,
		&expiryTime,
	)

	invoiceHash, err := common.HashSparkInvoiceFields(invoiceFields, network, receiverPublicKey)
	require.NoError(t, err)
	sig, err := schnorr.Sign(receiverPrivKey.ToBTCEC(), invoiceHash)
	require.NoError(t, err)

	invoice, err := common.EncodeSparkAddressWithSignature(
		receiverPublicKey.Serialize(),
		network,
		invoiceFields,
		sig.Serialize(),
	)
	require.NoError(t, err)

	_, _, _, err = sendTransferWithInvoice(t, invoice, senderPrivKey, receiverPrivKey)
	require.Error(t, err)
}

func testTransferWithInvoice(t *testing.T, invoice string, senderPrivKey keys.Private, receiverPrivKey keys.Private) {
	senderTransfer, rootNode, newLeafPrivKey, err := sendTransferWithInvoice(t, invoice, senderPrivKey, receiverPrivKey)
	require.NoError(t, err, "failed to send transfer with invoice")

	senderConfig := wallet.NewTestWalletConfigWithIdentityKey(t, senderPrivKey)
	authToken, err := wallet.AuthenticateWithServer(t.Context(), senderConfig)
	require.NoError(t, err, "failed to authenticate sender")
	senderCtx := wallet.ContextWithToken(t.Context(), authToken)
	invoiceResponse, err := wallet.QuerySparkInvoicesByRawString(
		senderCtx,
		senderConfig,
		[]string{invoice},
	)
	require.NoError(t, err, "failed to query spark invoices")
	transferID, err := uuid.Parse(senderTransfer.Id)
	require.NoError(t, err, "failed to parse transfer ID")

	require.Len(t, invoiceResponse.InvoiceStatuses, 1)
	require.Equal(t, invoice, invoiceResponse.InvoiceStatuses[0].Invoice)
	require.Equal(t, sparkpb.InvoiceStatus_FINALIZED, invoiceResponse.InvoiceStatuses[0].Status)
	require.Equal(t, &sparkpb.InvoiceResponse_SatsTransfer{
		SatsTransfer: &sparkpb.SatsTransfer{
			TransferId: transferID[:],
		},
	}, invoiceResponse.InvoiceStatuses[0].TransferType)

	// Receiver queries pending transfer
	receiverConfig := wallet.NewTestWalletConfigWithIdentityKey(t, receiverPrivKey)
	receiverToken, err := wallet.AuthenticateWithServer(t.Context(), receiverConfig)
	require.NoError(t, err, "failed to authenticate receiver")
	receiverCtx := wallet.ContextWithToken(t.Context(), receiverToken)
	pendingTransfer, err := wallet.QueryPendingTransfers(receiverCtx, receiverConfig)
	require.NoError(t, err, "failed to query pending transfers")
	require.NotEmpty(t, pendingTransfer.Transfers)
	// With deterministic private key generation, when the test is retried on failure,
	// transfers from the previous failed run will come back as a pending transfer.
	// Find the one that matches this run so we can pass retry.
	var receiverTransfer *pb.Transfer
	for _, t := range pendingTransfer.Transfers {
		if t.Id == senderTransfer.Id {
			receiverTransfer = t
			break
		}
	}
	require.NotNil(t, receiverTransfer)
	require.Equal(t, pb.TransferType_TRANSFER, receiverTransfer.Type)
	require.Equal(t, invoice, receiverTransfer.GetSparkInvoice())

	leafPrivKeyMap, err := wallet.VerifyPendingTransfer(t.Context(), receiverConfig, receiverTransfer)
	require.NoError(t, err)
	require.Equal(t, map[string]keys.Private{rootNode.Id: newLeafPrivKey}, leafPrivKeyMap)

	finalLeafPrivKey := keys.GeneratePrivateKey()
	claimingNode := wallet.LeafKeyTweak{
		Leaf:              receiverTransfer.Leaves[0].Leaf,
		SigningPrivKey:    newLeafPrivKey,
		NewSigningPrivKey: finalLeafPrivKey,
	}
	leavesToClaim := []wallet.LeafKeyTweak{claimingNode}
	res, err := wallet.ClaimTransfer(
		receiverCtx,
		receiverTransfer,
		receiverConfig,
		leavesToClaim,
	)
	require.NoError(t, err, "failed to ClaimTransfer")
	require.Equal(t, res[0].Id, claimingNode.Leaf.Id)
}

func sendTransferWithInvoice(
	t *testing.T,
	invoice string,
	senderPrivKey keys.Private,
	receiverPrivKey keys.Private,
) (senderTransfer *pb.Transfer, rootNode *pb.TreeNode, newLeafPrivKey keys.Private, err error) {
	senderConfig := wallet.NewTestWalletConfigWithIdentityKey(t, senderPrivKey)

	// Sender initiates transfer
	leafPrivKey := keys.GeneratePrivateKey()
	rootNode, err = wallet.CreateNewTree(senderConfig, faucet, leafPrivKey, amountSatsToSend)
	require.NoError(t, err, "failed to create new tree")

	newLeafPrivKey = keys.GeneratePrivateKey()
	transferNode := wallet.LeafKeyTweak{
		Leaf:              rootNode,
		SigningPrivKey:    leafPrivKey,
		NewSigningPrivKey: newLeafPrivKey,
	}
	leavesToTransfer := [1]wallet.LeafKeyTweak{transferNode}

	conn, err := sparktesting.DangerousNewGRPCConnectionWithoutVerifyTLS(senderConfig.CoordinatorAddress(), nil)
	require.NoError(t, err, "failed to create grpc connection")
	defer conn.Close()
	authToken, err := wallet.AuthenticateWithServer(t.Context(), senderConfig)
	require.NoError(t, err, "failed to authenticate sender")
	senderCtx := wallet.ContextWithToken(t.Context(), authToken)

	senderTransfer, err = wallet.SendTransferWithKeyTweaksAndInvoice(
		senderCtx,
		senderConfig,
		leavesToTransfer[:],
		receiverPrivKey.Public(),
		time.Now().Add(10*time.Minute),
		invoice,
	)
	return senderTransfer, rootNode, newLeafPrivKey, err
}

func TestQuerySparkInvoicesForUnknownInvoiceReturnsNotFound(t *testing.T) {
	rng := rand.NewChaCha8(deterministicSeedFromTestName(t.Name()))
	invoiceUUID, err := uuid.NewV7FromReader(rng)
	require.NoError(t, err)
	amountToSend := uint64(amountSatsToSend)
	memoString := "test memo"
	senderPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	senderPublicKey := senderPrivKey.Public()
	receiverPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	receiverPublicKey := receiverPrivKey.Public()
	tenMinutesFromNow := time.Now().Add(10 * time.Minute)
	network := common.Regtest

	amountSats := &amountToSend
	expiryTime := &tenMinutesFromNow
	memo := &memoString

	invoiceFields := common.CreateSatsSparkInvoiceFields(
		invoiceUUID[:],
		amountSats,
		memo,
		senderPublicKey,
		expiryTime,
	)

	invoiceHash, err := common.HashSparkInvoiceFields(invoiceFields, network, receiverPublicKey)
	require.NoError(t, err)
	sig, err := schnorr.Sign(receiverPrivKey.ToBTCEC(), invoiceHash)
	require.NoError(t, err)
	sigBytes := sig.Serialize()

	invoice, err := common.EncodeSparkAddressWithSignature(
		receiverPublicKey.Serialize(),
		network,
		invoiceFields,
		sigBytes,
	)
	require.NoError(t, err)

	senderConfig := wallet.NewTestWalletConfig(t)
	authToken, err := wallet.AuthenticateWithServer(t.Context(), senderConfig)
	require.NoError(t, err, "failed to authenticate sender")
	senderCtx := wallet.ContextWithToken(t.Context(), authToken)
	invoiceResponse, err := wallet.QuerySparkInvoicesByRawString(
		senderCtx,
		senderConfig,
		[]string{invoice},
	)
	require.NoError(t, err, "failed to query spark invoices")
	require.Len(t, invoiceResponse.InvoiceStatuses, 1)
	require.Equal(t, sparkpb.InvoiceStatus_NOT_FOUND, invoiceResponse.InvoiceStatuses[0].Status)
}

func TestTransferWithDirectFromCpfpRefundOnly(t *testing.T) {
	// --- setup ---

	config := wallet.NewTestWalletConfig(t)
	conn, err := sparktesting.DangerousNewGRPCConnectionWithoutVerifyTLS(config.CoordinatorAddress(), nil)
	require.NoError(t, err)
	defer conn.Close()

	token, err := wallet.AuthenticateWithConnection(t.Context(), config, conn)
	require.NoError(t, err)
	ctx := wallet.ContextWithToken(t.Context(), token)

	// Deposit funds so that they can be transferred.
	leafPrivKey := keys.GeneratePrivateKey()
	rootNode, err := wallet.CreateNewTree(config, faucet, leafPrivKey, amountSatsToSend)
	require.NoError(t, err, "failed to create new tree")

	receiverPrivKey := keys.GeneratePrivateKey()
	// newLeafPrivKey := keys.GeneratePrivateKey()

	// --- test ---
	//
	// Test that transfer works even when the direct from CPFP refund transaction
	// is the only direct transaction passed.

	sparkClient := pb.NewSparkServiceClient(conn)

	// Create a direct from CPFP refund transaction
	nodeTx, err := common.TxFromRawTxBytes(rootNode.NodeTx)
	require.NoError(t, err)
	nodeOutPoint := &wire.OutPoint{Hash: nodeTx.TxHash(), Index: 0}

	// Get the next sequence from the existing refund transaction
	currRefundTx, err := common.TxFromRawTxBytes(rootNode.RefundTx)
	require.NoError(t, err)
	nextSequence, err := spark.NextSequence(currRefundTx.TxIn[0].Sequence)
	require.NoError(t, err)

	refundTx, directFromCpfpRefundTx, err := wallet.CreateRefundTxs(
		nextSequence,
		nodeOutPoint,
		nodeTx.TxOut[0].Value,
		receiverPrivKey.Public(),
		true,
	)
	require.NoError(t, err)

	response, err := sparkClient.StartTransfer(ctx, &pb.StartTransferRequest{
		TransferId:                uuid.NewString(),
		OwnerIdentityPublicKey:    config.IdentityPublicKey().Serialize(),
		ReceiverIdentityPublicKey: receiverPrivKey.Public().Serialize(),
		LeavesToSend: []*pb.LeafRefundTxSigningJob{
			{
				LeafId:                           rootNode.Id,
				RefundTxSigningJob:               createSigningJobFromTx(t, leafPrivKey.Public(), refundTx),
				DirectFromCpfpRefundTxSigningJob: createSigningJobFromTx(t, leafPrivKey.Public(), directFromCpfpRefundTx),
			},
		},
		ExpiryTime: timestamppb.New(time.Now().Add(10 * time.Minute)),
	})

	require.NoError(t, err, "Expected StartTransfer to succeed with only direct from CPFP refund transaction")

	// Verify that the response contains the expected signing results
	require.Len(t, response.SigningResults, 1, "Expected exactly one signing result")
	signingResult := response.SigningResults[0]
	require.Equal(t, rootNode.Id, signingResult.LeafId, "Expected signing result for correct leaf")

	// Verify that the regular refund tx signing result is present
	require.NotNil(t, signingResult.RefundTxSigningResult, "Expected RefundTxSigningResult to be present")

	// Verify that the direct from CPFP refund tx signing result is present (this is the new behavior)
	require.NotNil(t, signingResult.DirectFromCpfpRefundTxSigningResult, "Expected DirectFromCpfpRefundTxSigningResult to be present when DirectFromCpfpRefundTxSigningJob is provided")
}

// TODO: remove in favor of signingJobFromTx (identical impl in an unmerged branch)
func createSigningJobFromTx(t *testing.T, publicKey keys.Public, tx *wire.MsgTx) *pb.SigningJob {
	var txBuf bytes.Buffer
	require.NoError(t, tx.Serialize(&txBuf))

	nonce, err := objects.RandomSigningNonce()
	require.NoError(t, err)
	nonceCommitmentProto, err := nonce.SigningCommitment().MarshalProto()
	require.NoError(t, err)

	return &pb.SigningJob{
		RawTx:                  txBuf.Bytes(),
		SigningPublicKey:       publicKey.Serialize(),
		SigningNonceCommitment: nonceCommitmentProto,
	}
}

func deterministicSeedFromTestName(testName string) [32]byte {
	hash := sha256.Sum256([]byte(testName))
	return hash
}
