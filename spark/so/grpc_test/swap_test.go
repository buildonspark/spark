package grpctest

import (
	"testing"
	"time"

	"github.com/lightsparkdev/spark/common/keys"

	"github.com/btcsuite/btcd/txscript"
	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/proto/spark"
	sparktesting "github.com/lightsparkdev/spark/testing"
	"github.com/lightsparkdev/spark/testing/wallet"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSwap(t *testing.T) {
	// Initiate sender
	senderConfig, err := sparktesting.TestWalletConfig()
	require.NoError(t, err, "failed to create sender wallet config")

	senderLeafPrivKey, err := keys.GeneratePrivateKey()
	require.NoError(t, err, "failed to create node signing private key")
	senderRootNode, err := sparktesting.CreateNewTree(senderConfig, faucet, senderLeafPrivKey, 100_000)
	require.NoError(t, err, "failed to create new tree")

	// Initiate receiver
	receiverConfig, err := sparktesting.TestWalletConfig()
	require.NoError(t, err, "failed to create sender wallet config")

	receiverLeafPrivKey, err := keys.GeneratePrivateKey()
	require.NoError(t, err, "failed to create node signing private key")
	receiverRootNode, err := sparktesting.CreateNewTree(receiverConfig, faucet, receiverLeafPrivKey, 100_000)
	require.NoError(t, err, "failed to create new tree")

	// Sender initiates transfer
	senderNewLeafPrivKey, err := keys.GeneratePrivateKey()
	require.NoError(t, err, "failed to create new node signing private key")

	senderTransferNode := wallet.LeafKeyTweak{
		Leaf:              senderRootNode,
		SigningPrivKey:    senderLeafPrivKey,
		NewSigningPrivKey: senderNewLeafPrivKey,
	}
	senderLeavesToTransfer := [1]wallet.LeafKeyTweak{senderTransferNode}

	// Get signature for refunds (normal flow)
	senderTransfer, senderRefundSignatureMap, leafDataMap, err := wallet.StartSwapSignRefund(
		t.Context(),
		senderConfig,
		senderLeavesToTransfer[:],
		receiverConfig.IdentityPublicKey(),
		time.Now().Add(10*time.Minute),
	)
	require.NoError(t, err)
	assert.Len(t, senderRefundSignatureMap, 1, "expected 1 refund signature")
	signature := senderRefundSignatureMap[senderRootNode.Id]
	assert.NotNil(t, signature, "expected refund signature for root node")
	leafData := leafDataMap[senderRootNode.Id]
	assert.NotNil(t, leafData, "expected leaf data for root node")

	sighash, err := common.SigHashFromTx(leafData.RefundTx, 0, leafData.Tx.TxOut[leafData.Vout])
	require.NoError(t, err)

	// Create adaptor from that signature
	adaptorAddedSignature, adaptorPrivKeyBytes, err := common.GenerateAdaptorFromSignature(signature)
	require.NoError(t, err)
	adaptorPrivKey, err := keys.ParsePrivateKey(adaptorPrivKeyBytes)
	require.NoError(t, err)
	adaptorPub := adaptorPrivKey.Public()
	// Alice sends adaptor and signature to Bob, Bob validates the adaptor
	nodeVerifyingPubkey, err := keys.ParsePublicKey(senderRootNode.VerifyingPublicKey)
	require.NoError(t, err)
	taprootKey := txscript.ComputeTaprootKeyNoScript(nodeVerifyingPubkey.ToBTCEC())
	err = common.ValidateOutboundAdaptorSignature(taprootKey, sighash, adaptorAddedSignature, adaptorPub.Serialize())
	require.NoError(t, err)

	// Bob signs refunds with adaptor
	receiverNewLeafPrivKey, err := keys.GeneratePrivateKey()
	require.NoError(t, err)

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
		adaptorPub,
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
		var verifyingPubKey keys.Public
		for _, signingResult := range operatorSigningResults {
			if signingResult.LeafId == nodeID {
				verifyingPubKey, err = keys.ParsePublicKey(signingResult.VerifyingKey)
				require.NoError(t, err)
			}
		}
		assert.NotNil(t, verifyingPubKey, "expected signing result for leaf %s", nodeID)
		taprootKey := txscript.ComputeTaprootKeyNoScript(verifyingPubKey.ToBTCEC())
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
	require.Equal(t, spark.TransferType_SWAP, receiverPendingTransfer.Type)

	leafPrivKeyMap, err := wallet.VerifyPendingTransfer(t.Context(), receiverConfig, receiverPendingTransfer)
	require.NoError(t, err, "unable to verify pending transfer")
	require.Len(t, leafPrivKeyMap, 1)
	require.Equal(t, senderNewLeafPrivKey.Serialize(), leafPrivKeyMap[senderRootNode.Id])

	finalLeafPrivKey, err := keys.GeneratePrivateKey()
	require.NoError(t, err, "failed to create new node signing private key")
	claimingNode := wallet.LeafKeyTweak{
		Leaf:              receiverPendingTransfer.Leaves[0].Leaf,
		SigningPrivKey:    senderNewLeafPrivKey,
		NewSigningPrivKey: finalLeafPrivKey,
	}
	leavesToClaim := [1]wallet.LeafKeyTweak{claimingNode}
	_, err = wallet.ClaimTransfer(
		receiverCtx,
		receiverPendingTransfer,
		receiverConfig,
		leavesToClaim[:],
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
	require.NoError(t, err, "failed to authenticate receiver")
	senderCtx := wallet.ContextWithToken(t.Context(), senderToken)
	pendingTransfer, err = wallet.QueryPendingTransfers(senderCtx, senderConfig)
	require.NoError(t, err, "failed to query pending transfers")
	require.Len(t, pendingTransfer.Transfers, 1, "expected 1 pending transfer")
	senderPendingTransfer := pendingTransfer.Transfers[0]
	require.Equal(t, senderTransfer.Id, receiverPendingTransfer.Id)
	require.Equal(t, spark.TransferType_COUNTER_SWAP, senderPendingTransfer.Type)

	leafPrivKeyMap, err = wallet.VerifyPendingTransfer(t.Context(), senderConfig, senderPendingTransfer)
	require.NoError(t, err, "unable to verify pending transfer")
	require.Len(t, leafPrivKeyMap, 1, "expected 1 leaf to transfer")
	require.Equal(t, receiverNewLeafPrivKey.Serialize(), leafPrivKeyMap[receiverRootNode.Id])

	finalLeafPrivKey, err = keys.GeneratePrivateKey()
	require.NoError(t, err, "failed to create new node signing private key")
	claimingNode = wallet.LeafKeyTweak{
		Leaf:              senderPendingTransfer.Leaves[0].Leaf,
		SigningPrivKey:    receiverNewLeafPrivKey,
		NewSigningPrivKey: finalLeafPrivKey,
	}
	leavesToClaim = [1]wallet.LeafKeyTweak{claimingNode}
	_, err = wallet.ClaimTransfer(
		senderCtx,
		senderPendingTransfer,
		senderConfig,
		leavesToClaim[:],
	)
	require.NoError(t, err, "failed to ClaimTransfer")
}

func TestSwapDeliverTransferPackageTwice(t *testing.T) {
	// Initiate sender
	senderConfig, err := sparktesting.TestWalletConfig()
	require.NoError(t, err, "failed to create sender wallet config")

	senderLeafPrivKey, err := keys.GeneratePrivateKey()
	require.NoError(t, err, "failed to create node signing private key")
	senderRootNode, err := sparktesting.CreateNewTree(senderConfig, faucet, senderLeafPrivKey, 100_000)
	require.NoError(t, err, "failed to create new tree")

	// Initiate receiver
	receiverConfig, err := sparktesting.TestWalletConfig()
	require.NoError(t, err, "failed to create sender wallet config")

	receiverLeafPrivKey, err := keys.GeneratePrivateKey()
	require.NoError(t, err, "failed to create node signing private key")
	receiverRootNode, err := sparktesting.CreateNewTree(receiverConfig, faucet, receiverLeafPrivKey, 100_000)
	require.NoError(t, err, "failed to create new tree")

	// Sender initiates transfer
	senderNewLeafPrivKey, err := keys.GeneratePrivateKey()
	require.NoError(t, err, "failed to create new node signing private key")

	senderTransferNode := wallet.LeafKeyTweak{
		Leaf:              senderRootNode,
		SigningPrivKey:    senderLeafPrivKey,
		NewSigningPrivKey: senderNewLeafPrivKey,
	}
	senderLeavesToTransfer := [1]wallet.LeafKeyTweak{senderTransferNode}

	// Get signature for refunds (normal flow)
	senderTransfer, senderRefundSignatureMap, leafDataMap, err := wallet.StartSwapSignRefund(
		t.Context(),
		senderConfig,
		senderLeavesToTransfer[:],
		receiverConfig.IdentityPublicKey(),
		time.Now().Add(10*time.Minute),
	)
	require.NoError(t, err)
	assert.Len(t, senderRefundSignatureMap, 1, "expected 1 refund signature")
	signature := senderRefundSignatureMap[senderRootNode.Id]
	assert.NotNil(t, signature, "expected refund signature for root node")
	leafData := leafDataMap[senderRootNode.Id]
	assert.NotNil(t, leafData, "expected leaf data for root node")

	sighash, err := common.SigHashFromTx(leafData.RefundTx, 0, leafData.Tx.TxOut[leafData.Vout])
	require.NoError(t, err)

	// Create adaptor from that signature
	adaptorAddedSignature, adaptorPrivKeyBytes, err := common.GenerateAdaptorFromSignature(signature)
	require.NoError(t, err)
	adaptorPrivKey, err := keys.ParsePrivateKey(adaptorPrivKeyBytes)
	require.NoError(t, err)
	adaptorPub := adaptorPrivKey.Public()

	// Alice sends adaptor and signature to Bob, Bob validates the adaptor
	nodeVerifyingPubKey, err := keys.ParsePublicKey(senderRootNode.VerifyingPublicKey)
	require.NoError(t, err)
	taprootKey := txscript.ComputeTaprootKeyNoScript(nodeVerifyingPubKey.ToBTCEC())
	err = common.ValidateOutboundAdaptorSignature(taprootKey, sighash, adaptorAddedSignature, adaptorPub.Serialize())
	require.NoError(t, err)

	// Bob signs refunds with adaptor
	receiverNewLeafPrivKey, err := keys.GeneratePrivateKey()
	require.NoError(t, err)

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
		adaptorPub,
	)
	require.NoError(t, err)

	// Alice verifies Bob's signatures
	receiverSighash, err := common.SigHashFromTx(leafDataMap[receiverLeavesToTransfer[0].Leaf.Id].RefundTx, 0, leafDataMap[receiverLeavesToTransfer[0].Leaf.Id].Tx.TxOut[leafDataMap[receiverLeavesToTransfer[0].Leaf.Id].Vout])
	require.NoError(t, err)

	receiverKey, err := keys.ParsePublicKey(receiverLeavesToTransfer[0].Leaf.VerifyingPublicKey)
	require.NoError(t, err)
	receiverTaprootKey := txscript.ComputeTaprootKeyNoScript(receiverKey.ToBTCEC())

	_, err = common.ApplyAdaptorToSignature(receiverTaprootKey, receiverSighash, receiverRefundSignatureMap[receiverLeavesToTransfer[0].Leaf.Id], adaptorPrivKey.Serialize())
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
		adaptorSig, err := common.ApplyAdaptorToSignature(taprootKey, sighash, signature, adaptorPrivKey.Serialize())
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
	// Second consecutive call
	_, err = wallet.DeliverTransferPackage(
		t.Context(),
		senderConfig,
		senderTransfer,
		senderLeavesToTransfer[:],
		senderRefundSignatureMap,
	)
	require.Error(t, err, "expected to receive error after consecutive call")

	receiverToken, err := wallet.AuthenticateWithServer(t.Context(), receiverConfig)
	require.NoError(t, err, "failed to authenticate receiver")
	receiverCtx := wallet.ContextWithToken(t.Context(), receiverToken)
	pendingTransfer, err := wallet.QueryPendingTransfers(receiverCtx, receiverConfig)
	require.NoError(t, err, "failed to query pending transfers")
	require.Len(t, pendingTransfer.Transfers, 1)
	receiverPendingTransfer := pendingTransfer.Transfers[0]
	require.Equal(t, senderTransfer.Id, receiverPendingTransfer.Id)
	require.Equal(t, spark.TransferType_SWAP, receiverPendingTransfer.Type)

	leafPrivKeyMap, err := wallet.VerifyPendingTransfer(t.Context(), receiverConfig, receiverPendingTransfer)
	require.NoError(t, err, "unable to verify pending transfer")
	require.Len(t, leafPrivKeyMap, 1)
	require.Equal(t, senderNewLeafPrivKey.Serialize(), leafPrivKeyMap[senderRootNode.Id])

	finalLeafPrivKey, err := keys.GeneratePrivateKey()
	require.NoError(t, err, "failed to create new node signing private key")
	claimingNode := wallet.LeafKeyTweak{
		Leaf:              receiverPendingTransfer.Leaves[0].Leaf,
		SigningPrivKey:    senderNewLeafPrivKey,
		NewSigningPrivKey: finalLeafPrivKey,
	}
	leavesToClaim := [1]wallet.LeafKeyTweak{claimingNode}
	_, err = wallet.ClaimTransfer(
		receiverCtx,
		receiverPendingTransfer,
		receiverConfig,
		leavesToClaim[:],
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
	// Second consecutive call
	_, err = wallet.DeliverTransferPackage(
		t.Context(),
		receiverConfig,
		receiverTransfer,
		receiverLeavesToTransfer[:],
		newReceiverRefundSignatureMap,
	)
	require.Error(t, err, "expected to receive error after consecutive call")

	senderToken, err := wallet.AuthenticateWithServer(t.Context(), senderConfig)
	require.NoError(t, err, "failed to authenticate receiver")
	senderCtx := wallet.ContextWithToken(t.Context(), senderToken)
	pendingTransfer, err = wallet.QueryPendingTransfers(senderCtx, senderConfig)
	require.NoError(t, err, "failed to query pending transfers")
	require.Len(t, pendingTransfer.Transfers, 1)
	senderPendingTransfer := pendingTransfer.Transfers[0]
	require.Equal(t, senderTransfer.Id, receiverPendingTransfer.Id)
	require.Equal(t, spark.TransferType_COUNTER_SWAP, senderPendingTransfer.Type)

	leafPrivKeyMap, err = wallet.VerifyPendingTransfer(t.Context(), senderConfig, senderPendingTransfer)
	require.NoError(t, err, "unable to verify pending transfer")
	require.Len(t, leafPrivKeyMap, 1)
	require.Equal(t, receiverNewLeafPrivKey.Serialize(), leafPrivKeyMap[receiverRootNode.Id])

	finalLeafPrivKey, err = keys.GeneratePrivateKey()
	require.NoError(t, err, "failed to create new node signing private key")
	claimingNode = wallet.LeafKeyTweak{
		Leaf:              senderPendingTransfer.Leaves[0].Leaf,
		SigningPrivKey:    receiverNewLeafPrivKey,
		NewSigningPrivKey: finalLeafPrivKey,
	}
	leavesToClaim = [1]wallet.LeafKeyTweak{claimingNode}

	_, err = wallet.ClaimTransfer(
		senderCtx,
		senderPendingTransfer,
		senderConfig,
		leavesToClaim[:],
	)
	require.NoError(t, err, "failed to ClaimTransfer")
}
