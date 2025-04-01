package grpctest

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/txscript"
	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/lightsparkdev/spark-go/common"
	"github.com/lightsparkdev/spark-go/proto/spark"
	testutil "github.com/lightsparkdev/spark-go/test_util"
	"github.com/lightsparkdev/spark-go/wallet"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSwap(t *testing.T) {
	// Initiate sender
	senderConfig, err := testutil.TestWalletConfig()
	if err != nil {
		t.Fatalf("failed to create sender wallet config: %v", err)
	}

	senderLeafPrivKey, err := secp256k1.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("failed to create node signing private key: %v", err)
	}
	senderRootNode, err := testutil.CreateNewTree(senderConfig, faucet, senderLeafPrivKey, 100_000)
	if err != nil {
		t.Fatalf("failed to create new tree: %v", err)
	}

	// Initiate receiver
	receiverConfig, err := testutil.TestWalletConfig()
	if err != nil {
		t.Fatalf("failed to create sender wallet config: %v", err)
	}

	receiverLeafPrivKey, err := secp256k1.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("failed to create node signing private key: %v", err)
	}
	receiverRootNode, err := testutil.CreateNewTree(receiverConfig, faucet, receiverLeafPrivKey, 100_000)
	if err != nil {
		t.Fatalf("failed to create new tree: %v", err)
	}

	// Sender initiates transfer
	senderNewLeafPrivKey, err := secp256k1.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("failed to create new node signing private key: %v", err)
	}

	senderTransferNode := wallet.LeafKeyTweak{
		Leaf:              senderRootNode,
		SigningPrivKey:    senderLeafPrivKey.Serialize(),
		NewSigningPrivKey: senderNewLeafPrivKey.Serialize(),
	}
	senderLeavesToTransfer := [1]wallet.LeafKeyTweak{senderTransferNode}

	// Get signature for refunds (normal flow)
	senderTransfer, senderRefundSignatureMap, leafDataMap, err := wallet.StartSwapSignRefund(
		context.Background(),
		senderConfig,
		senderLeavesToTransfer[:],
		receiverConfig.IdentityPublicKey(),
		time.Now().Add(10*time.Minute),
	)
	assert.NoError(t, err)
	assert.Equal(t, 1, len(senderRefundSignatureMap), "expected 1 refund signature")
	signature := senderRefundSignatureMap[senderRootNode.Id]
	assert.NotNil(t, signature, "expected refund signature for root node")
	leafData := leafDataMap[senderRootNode.Id]
	assert.NotNil(t, leafData, "expected leaf data for root node")

	sighash, err := common.SigHashFromTx(leafData.RefundTx, 0, leafData.Tx.TxOut[leafData.Vout])
	assert.NoError(t, err)

	// Create adaptor from that signature
	adaptorAddedSignature, adaptorPrivKey, err := common.GenerateAdaptorFromSignature(signature)
	assert.NoError(t, err)
	_, adaptorPub := btcec.PrivKeyFromBytes(adaptorPrivKey)

	// Alice sends adaptor and signature to Bob, Bob validates the adaptor
	nodeVerifyingPubkey, err := secp256k1.ParsePubKey(senderRootNode.VerifyingPublicKey)
	assert.NoError(t, err)
	taprootKey := txscript.ComputeTaprootKeyNoScript(nodeVerifyingPubkey)
	err = common.ValidateOutboundAdaptorSignature(taprootKey, sighash, adaptorAddedSignature, adaptorPub.SerializeCompressed())
	assert.NoError(t, err)

	// Bob signs refunds with adaptor
	receiverNewLeafPrivKey, err := secp256k1.GeneratePrivateKey()
	assert.NoError(t, err)

	receiverTransferNode := wallet.LeafKeyTweak{
		Leaf:              receiverRootNode,
		SigningPrivKey:    receiverLeafPrivKey.Serialize(),
		NewSigningPrivKey: receiverNewLeafPrivKey.Serialize(),
	}
	receiverLeavesToTransfer := [1]wallet.LeafKeyTweak{receiverTransferNode}
	receiverTransfer, receiverRefundSignatureMap, leafDataMap, operatorSigningResults, err := wallet.CounterSwapSignRefund(
		context.Background(),
		receiverConfig,
		receiverLeavesToTransfer[:],
		senderConfig.IdentityPublicKey(),
		time.Now().Add(10*time.Minute),
		adaptorPub,
	)
	assert.NoError(t, err)

	// Alice verifies Bob's signatures
	receiverSighash, err := common.SigHashFromTx(leafDataMap[receiverLeavesToTransfer[0].Leaf.Id].RefundTx, 0, leafDataMap[receiverLeavesToTransfer[0].Leaf.Id].Tx.TxOut[leafDataMap[receiverLeavesToTransfer[0].Leaf.Id].Vout])
	assert.NoError(t, err)

	receiverKey, err := secp256k1.ParsePubKey(receiverLeavesToTransfer[0].Leaf.VerifyingPublicKey)
	assert.NoError(t, err)
	receiverTaprootKey := txscript.ComputeTaprootKeyNoScript(receiverKey)

	_, err = common.ApplyAdaptorToSignature(receiverTaprootKey, receiverSighash, receiverRefundSignatureMap[receiverLeavesToTransfer[0].Leaf.Id], adaptorPrivKey)
	assert.NoError(t, err)

	// Alice reveals adaptor secret to Bob, Bob combines with existing adaptor signatures to get valid signatures
	newReceiverRefundSignatureMap := make(map[string][]byte)
	for nodeID, signature := range receiverRefundSignatureMap {
		leafData := leafDataMap[nodeID]
		sighash, _ := common.SigHashFromTx(leafData.RefundTx, 0, leafData.Tx.TxOut[leafData.Vout])
		var verifyingPubkey *secp256k1.PublicKey
		for _, signingResult := range operatorSigningResults {
			if signingResult.LeafId == nodeID {
				verifyingPubkey, err = secp256k1.ParsePubKey(signingResult.VerifyingKey)
				assert.NoError(t, err)
			}
		}
		assert.NotNil(t, verifyingPubkey, "expected signing result for leaf %s", nodeID)
		taprootKey := txscript.ComputeTaprootKeyNoScript(verifyingPubkey)
		adaptorSig, err := common.ApplyAdaptorToSignature(taprootKey, sighash, signature, adaptorPrivKey)
		assert.NoError(t, err)
		newReceiverRefundSignatureMap[nodeID] = adaptorSig
	}

	// Alice provides key tweak, Bob claims alice's leaves
	senderTransfer, err = wallet.SendTransferTweakKey(
		context.Background(),
		senderConfig,
		senderTransfer,
		senderLeavesToTransfer[:],
		senderRefundSignatureMap,
	)
	if err != nil {
		t.Fatalf("failed to send transfer tweak key: %v", err)
	}

	receiverToken, err := wallet.AuthenticateWithServer(context.Background(), receiverConfig)
	if err != nil {
		t.Fatalf("failed to authenticate receiver: %v", err)
	}
	receiverCtx := wallet.ContextWithToken(context.Background(), receiverToken)
	pendingTransfer, err := wallet.QueryPendingTransfers(receiverCtx, receiverConfig)
	if err != nil {
		t.Fatalf("failed to query pending transfers: %v", err)
	}
	if len(pendingTransfer.Transfers) != 1 {
		t.Fatalf("expected 1 pending transfer, got %d", len(pendingTransfer.Transfers))
	}
	receiverPendingTransfer := pendingTransfer.Transfers[0]
	if receiverPendingTransfer.Id != senderTransfer.Id {
		t.Fatalf("expected transfer id %s, got %s", senderTransfer.Id, receiverPendingTransfer.Id)
	}
	require.Equal(t, receiverPendingTransfer.Type, spark.TransferType_SWAP)

	leafPrivKeyMap, err := wallet.VerifyPendingTransfer(context.Background(), receiverConfig, receiverPendingTransfer)
	if err != nil {
		t.Fatalf("unable to verify pending transfer: %v", err)
	}
	if len(*leafPrivKeyMap) != 1 {
		t.Fatalf("Expected 1 leaf to transfer, got %d", len(*leafPrivKeyMap))
	}
	if !bytes.Equal((*leafPrivKeyMap)[senderRootNode.Id], senderNewLeafPrivKey.Serialize()) {
		t.Fatalf("wrong leaf signing private key")
	}

	finalLeafPrivKey, err := secp256k1.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("failed to create new node signing private key: %v", err)
	}
	claimingNode := wallet.LeafKeyTweak{
		Leaf:              receiverPendingTransfer.Leaves[0].Leaf,
		SigningPrivKey:    senderNewLeafPrivKey.Serialize(),
		NewSigningPrivKey: finalLeafPrivKey.Serialize(),
	}
	leavesToClaim := [1]wallet.LeafKeyTweak{claimingNode}
	_, err = wallet.ClaimTransfer(
		receiverCtx,
		receiverPendingTransfer,
		receiverConfig,
		leavesToClaim[:],
	)
	if err != nil {
		t.Fatalf("failed to ClaimTransfer: %v", err)
	}

	// Bob provides key tweak, Alice claims bob's leaves
	_, err = wallet.SendTransferTweakKey(
		context.Background(),
		receiverConfig,
		receiverTransfer,
		receiverLeavesToTransfer[:],
		newReceiverRefundSignatureMap,
	)
	if err != nil {
		t.Fatalf("failed to send transfer tweak key: %v", err)
	}

	senderToken, err := wallet.AuthenticateWithServer(context.Background(), senderConfig)
	if err != nil {
		t.Fatalf("failed to authenticate receiver: %v", err)
	}
	senderCtx := wallet.ContextWithToken(context.Background(), senderToken)
	pendingTransfer, err = wallet.QueryPendingTransfers(senderCtx, senderConfig)
	if err != nil {
		t.Fatalf("failed to query pending transfers: %v", err)
	}
	if len(pendingTransfer.Transfers) != 1 {
		t.Fatalf("expected 1 pending transfer, got %d", len(pendingTransfer.Transfers))
	}
	senderPendingTransfer := pendingTransfer.Transfers[0]
	if receiverPendingTransfer.Id != senderTransfer.Id {
		t.Fatalf("expected transfer id %s, got %s", senderTransfer.Id, receiverPendingTransfer.Id)
	}
	require.Equal(t, senderPendingTransfer.Type, spark.TransferType_COUNTER_SWAP)

	leafPrivKeyMap, err = wallet.VerifyPendingTransfer(context.Background(), senderConfig, senderPendingTransfer)
	if err != nil {
		t.Fatalf("unable to verify pending transfer: %v", err)
	}
	if len(*leafPrivKeyMap) != 1 {
		t.Fatalf("Expected 1 leaf to transfer, got %d", len(*leafPrivKeyMap))
	}
	if !bytes.Equal((*leafPrivKeyMap)[receiverRootNode.Id], receiverNewLeafPrivKey.Serialize()) {
		t.Fatalf("wrong leaf signing private key")
	}

	finalLeafPrivKey, err = secp256k1.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("failed to create new node signing private key: %v", err)
	}
	claimingNode = wallet.LeafKeyTweak{
		Leaf:              senderPendingTransfer.Leaves[0].Leaf,
		SigningPrivKey:    receiverNewLeafPrivKey.Serialize(),
		NewSigningPrivKey: finalLeafPrivKey.Serialize(),
	}
	leavesToClaim = [1]wallet.LeafKeyTweak{claimingNode}
	_, err = wallet.ClaimTransfer(
		senderCtx,
		senderPendingTransfer,
		senderConfig,
		leavesToClaim[:],
	)
	if err != nil {
		t.Fatalf("failed to ClaimTransfer: %v", err)
	}
}
